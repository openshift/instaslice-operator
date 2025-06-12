package daemonset

import (
	"context"
	"fmt"
	"time"

	deviceplugins "github.com/openshift/instaslice-operator/pkg/daemonset/deviceplugins"
	"github.com/openshift/instaslice-operator/pkg/daemonset/watcher"
	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"tags.cncf.io/container-device-interface/pkg/cdi"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
	klog.InfoS("RunDaemonset started")
	// Configure the CDI cache once so both the device plugin and watcher
	// operate on the same instance.
	if err := cdi.Configure(cdi.WithSpecDirs(cdi.DefaultStaticDir)); err != nil {
		klog.ErrorS(err, "Failed to configure CDI cache")
		return err
	}
	// Patch node with max MIG placements capacity
	// Patch node with max MIG placements capacity
	// if err := addMigCapacityToNode(ctx, cc.KubeConfig); err != nil {
	// 	klog.ErrorS(err, "Failed to patch MIG capacity on node")
	// 	return fmt.Errorf("failed to patch MIG capacity on node: %w", err)
	// }
	// klog.InfoS("Node MIG capacity patch completed")
	// Start device plugins
	if err := deviceplugins.StartDevicePlugins(ctx, cc.KubeConfig); err != nil {
		klog.ErrorS(err, "Failed to start device plugins")
		return err
	}
	klog.InfoS("Device plugins started")

	// Prepare client for watcher and controllers
	cfg := rest.CopyConfig(cc.KubeConfig)
	cfg.AcceptContentTypes = "application/json"
	cfg.ContentType = "application/json"
	opClientset, err := instaclient.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create instaslice operator client: %w", err)
	}

	// Setup CDI spec watcher using the same CDI cache as the device plugin
	cdiCache := watcher.NewCDICache(cdi.GetDefaultCache())
	if err := watcher.SetupCDIDeletionWatcher(ctx, cdi.DefaultStaticDir, cdiCache, opClientset); err != nil {
		klog.ErrorS(err, "Failed to setup CDI watcher")
		return err
	}
	klog.InfoS("CDI watcher setup completed")

	// Set up operator config informers for dynamic log level
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
	opInformerFactory.Start(ctx.Done())
	klog.InfoS("Starting log level controller", "namespace", operatorNamespace)
	go loglevel.NewClusterOperatorLoggingController(opClient, cc.EventRecorder).Run(ctx, 1)

	<-ctx.Done()
	klog.InfoS("RunDaemonset completed, shutting down")
	return nil
}
