package scheduler

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/featuregate"
	logsapi "k8s.io/component-base/logs/api/v1"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	scheduleroptions "k8s.io/kubernetes/cmd/kube-scheduler/app/options"

	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/loglevel"

	"github.com/spf13/pflag"

	mig "github.com/openshift/instaslice-operator/pkg/scheduler/plugins/mig"
	_ "sigs.k8s.io/scheduler-plugins/apis/config/scheme"
)

// RunScheduler starts the scheduler and a dynamic log level controller.
func RunScheduler(ctx context.Context, cc *controllercmd.ControllerContext, opts *scheduleroptions.Options) error {
	klog.InfoS("RunScheduler started")
	cfg := rest.CopyConfig(cc.KubeConfig)
	cfg.AcceptContentTypes = "application/json"
	cfg.ContentType = "application/json"

	opClientset, err := instaclient.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create instaslice operator client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	operatorNamespace := cc.OperatorNamespace
	if operatorNamespace == "openshift-config-managed" {
		operatorNamespace = "das-operator"
	}

	opInformerFactory := instainformers.NewSharedInformerFactory(opClientset, 10*time.Minute)
	opClient := &operatorclient.DASOperatorSetClient{
		Ctx:               ctx,
		SharedInformer:    opInformerFactory.OpenShiftOperator().V1alpha1().DASOperators().Informer(),
		Lister:            opInformerFactory.OpenShiftOperator().V1alpha1().DASOperators().Lister(),
		OperatorClient:    opClientset.OpenShiftOperatorV1alpha1(),
		OperatorNamespace: operatorNamespace,
	}

	eventRecorder := events.NewKubeRecorderWithOptions(
		kubeClient.CoreV1().Events(operatorNamespace),
		events.RecommendedClusterSingletonCorrelatorOptions(),
		"das-scheduler",
		&corev1.ObjectReference{Namespace: operatorNamespace, Name: "das-scheduler"},
		clock.RealClock{},
	)

	opInformerFactory.Start(ctx.Done())
	klog.InfoS("Starting log level controller", "namespace", operatorNamespace)
	go loglevel.NewClusterOperatorLoggingController(opClient, eventRecorder).Run(ctx, 1)

	fg := opts.ComponentGlobalsRegistry.FeatureGateFor(featuregate.DefaultKubeComponent)
	if err := logsapi.ValidateAndApply(opts.Logs, fg); err != nil {
		return err
	}
	fs := pflag.NewFlagSet("scheduler", pflag.ExitOnError)
	for _, name := range opts.Flags.Order {
		fs.AddFlagSet(opts.Flags.FlagSet(name))
	}
	cliflag.PrintFlags(fs)

	cfgCompleted, sched, err := app.Setup(ctx, opts, app.WithPlugin(mig.Name, mig.New))
	if err != nil {
		return err
	}
	fg.(featuregate.MutableFeatureGate).AddMetrics()
	return app.Run(ctx, cfgCompleted, sched)
}
