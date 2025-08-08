package operator

import (
	"context"
	"os"
	"time"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	apiextclientv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/klog/v2"

	slicev1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	operatorconfigclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	operatorclientinformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
)

var operatorNamespace = "das-operator"

func RunOperator(ctx context.Context, cc *controllercmd.ControllerContext) error {
	kubeClient, err := kubernetes.NewForConfig(cc.ProtoKubeConfig)
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(cc.ProtoKubeConfig)
	if err != nil {
		return err
	}

	apiextensionClient, err := apiextclientv1.NewForConfig(cc.KubeConfig)
	if err != nil {
		return err
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cc.KubeConfig)
	if err != nil {
		return err
	}

	appsClient, err := appsv1client.NewForConfig(cc.KubeConfig)
	if err != nil {
		return err
	}

	operatorConfigClient, err := operatorconfigclient.NewForConfig(cc.KubeConfig)
	if err != nil {
		return err
	}

	namespace := cc.OperatorNamespace
	if namespace == "openshift-config-managed" {
		// we need to fall back to our default namespace rather than library-go's when running outside the cluster
		namespace = operatorNamespace
	}

	operatorConfigInformers := operatorclientinformers.NewSharedInformerFactoryWithOptions(operatorConfigClient, 10*time.Minute, operatorclientinformers.WithNamespace(namespace))

	// KubeInformer for Instaslice namespace
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, "", namespace)

	instasliceClient := &operatorclient.DASOperatorSetClient{
		Ctx:               ctx,
		SharedInformer:    operatorConfigInformers.OpenShiftOperator().V1alpha1().DASOperators().Informer(),
		Lister:            operatorConfigInformers.OpenShiftOperator().V1alpha1().DASOperators().Lister(),
		OperatorClient:    operatorConfigClient.OpenShiftOperatorV1alpha1(),
		OperatorNamespace: namespace,
	}

	emulatedMode := os.Getenv("EMULATED_MODE")
	var emMode slicev1alpha1.EmulatedMode
	if emulatedMode == "" {
		emMode = slicev1alpha1.EmulatedModeDisabled
	} else {
		emMode = slicev1alpha1.EmulatedMode(emulatedMode)
	}

	targetConfigReconciler := NewTargetConfigReconciler(
		emMode,
		os.Getenv("RELATED_IMAGE_DAEMONSET_IMAGE"),
		os.Getenv("RELATED_IMAGE_WEBHOOK_IMAGE"),
		os.Getenv("RELATED_IMAGE_SCHEDULER_IMAGE"),
		namespace,
		operatorConfigClient.OpenShiftOperatorV1alpha1().DASOperators(namespace),
		operatorConfigInformers.OpenShiftOperator().V1alpha1().DASOperators(),
		kubeInformersForNamespaces,
		appsClient,
		instasliceClient,
		dynamicClient,
		discoveryClient,
		kubeClient,
		apiextensionClient,
		cc.EventRecorder,
	)

	// Create the log controller
	logLevelController := loglevel.NewClusterOperatorLoggingController(instasliceClient, cc.EventRecorder)

	klog.Infof("Starting informers")
	operatorConfigInformers.Start(ctx.Done())
	kubeInformersForNamespaces.Start(ctx.Done())

	klog.Infof("Starting log level controller")
	go logLevelController.Run(ctx, 1)
	klog.Infof("Starting target config reconciler")
	go targetConfigReconciler.Run(ctx, 1)

	<-ctx.Done()

	return nil
}
