package daemonset

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/instaslice-operator/pkg/daemonset/device"
	"github.com/openshift/instaslice-operator/pkg/daemonset/watcher"
	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"k8s.io/klog/v2"
	"tags.cncf.io/container-device-interface/pkg/cdi"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
	klog.InfoS("RunDaemonset started")
	// Patch node with max MIG placements capacity
	// Patch node with max MIG placements capacity
	// if err := addMigCapacityToNode(ctx, cc.KubeConfig); err != nil {
	// 	klog.ErrorS(err, "Failed to patch MIG capacity on node")
	// 	return fmt.Errorf("failed to patch MIG capacity on node: %w", err)
	// }
	// klog.InfoS("Node MIG capacity patch completed")
	// Start device plugins
	if err := device.StartDevicePlugins(ctx, cc.KubeConfig); err != nil {
		klog.ErrorS(err, "Failed to start device plugins")
		return err
	}
	klog.InfoS("Device plugins started")

	// Setup CDI spec watcher
	cdiCache := watcher.NewCDICache()
	if err := watcher.SetupCDIDeletionWatcher(ctx, cdi.DefaultDynamicDir, cdiCache); err != nil {
		klog.ErrorS(err, "Failed to setup CDI watcher")
		return err
	}
	klog.InfoS("CDI watcher setup completed")

	// Set up operator config informers for dynamic log level
	opClientset, err := instaclient.NewForConfig(cc.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create instaslice operator client: %w", err)
	}
	operatorNamespace := cc.OperatorNamespace
	if operatorNamespace == "openshift-config-managed" {
		operatorNamespace = "instaslice-system"
	}
	opInformerFactory := instainformers.NewSharedInformerFactory(opClientset, 10*time.Minute)
	opClient := &operatorclient.InstasliceOperatorSetClient{
		Ctx:               ctx,
		SharedInformer:    opInformerFactory.OpenShiftOperator().V1alpha1().InstasliceOperators().Informer(),
		Lister:            opInformerFactory.OpenShiftOperator().V1alpha1().InstasliceOperators().Lister(),
		OperatorClient:    opClientset.OpenShiftOperatorV1alpha1(),
		OperatorNamespace: operatorNamespace,
	}
	opInformerFactory.Start(ctx.Done())
	klog.InfoS("Starting log level controller", "namespace", operatorNamespace)
	go loglevel.NewClusterOperatorLoggingController(opClient, cc.EventRecorder).Run(ctx, 1)

	<-ctx.Done()
	klog.InfoS("RunDaemonset completed, shutting down")
	return nil
}
