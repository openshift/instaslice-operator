package daemonset

import (
   "context"
   "fmt"
   "time"

   "github.com/openshift/instaslice-operator/pkg/daemonset/device"
   "github.com/openshift/instaslice-operator/pkg/daemonset/watcher"
   "github.com/openshift/library-go/pkg/controller/controllercmd"
   instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
   instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
   "github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
   "github.com/openshift/library-go/pkg/operator/loglevel"
   "k8s.io/klog/v2"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
	// Discover MIG-enabled GPUs and populate Instaslice CR before starting plugins
	if err := discoverMigEnabledGpuWithSlices(ctx, cc.KubeConfig); err != nil {
		return fmt.Errorf("failed to discover MIG GPUs: %w", err)
	}
	// Patch node with max MIG placements capacity
	if err := addMigCapacityToNode(ctx, cc.KubeConfig); err != nil {
		return fmt.Errorf("failed to patch MIG capacity on node: %w", err)
	}
	if err := device.StartDevicePlugins(ctx); err != nil {
		return err
	}

	if err := watcher.SetupPodDeletionWatcher(ctx, cc.KubeConfig); err != nil {
		return err
	}

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
    klog.Info("Starting log level controller")
    go loglevel.NewClusterOperatorLoggingController(opClient, cc.EventRecorder).Run(ctx, 1)

	<-ctx.Done()
	return nil
}
