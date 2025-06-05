package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	"github.com/openshift/instaslice-operator/pkg/deviceplugin"
	versioned "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// StartDevicePlugins starts device managers, gRPC servers, and registrars for each resource.
func StartDevicePlugins(ctx context.Context, kubeConfig *rest.Config) error {

	nodeName := os.Getenv("NODE_NAME")

	klog.InfoS("Starting discovery of MIG-enabled GPUs", "node", nodeName)
	if nodeName == "" {
		err := fmt.Errorf("NODE_NAME environment variable is required")
		klog.ErrorS(err, "NODE_NAME environment variable is required")
		return err
	}
	// If emulation is enabled in the InstasliceOperator CR, use fake discovery
	csOp, err := versioned.NewForConfig(kubeConfig)
	if err != nil {
		klog.ErrorS(err, "Failed to create operator client", "node", nodeName)
		return err
	}
	opClient := csOp.OpenShiftOperatorV1alpha1().InstasliceOperators(instasliceNamespace)
	instOp, err := opClient.Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to get InstasliceOperator", "node", nodeName)
		return err
	}

	emulatedMode := instOp.Spec.EmulatedMode
	var discoverer MigGpuDiscoverer
	if emulatedMode == instav1.EmulatedModeEnabled {
		discoverer = &EmulatedMigGpuDiscoverer{
			ctx:         ctx,
			nodeName:    nodeName,
			instaClient: csOp.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace),
		}
	} else {
		discoverer = &RealMigGpuDiscoverer{
			ctx:         ctx,
			nodeName:    nodeName,
			instaClient: csOp.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace),
		}
	}
	if err := discoverer.Discover(); err != nil {
		klog.ErrorS(err, "Failed to discover MIG-enabled GPUs")
		return fmt.Errorf("failed to discover MIG GPUs: %w", err)
	}
	klog.InfoS("MIG GPU discovery completed")
	// define the extended resources to serve
	// TODO : load these from the instaslice CR
	resourceNames := []string{
		// TODO - rename these to nvidia.instaslice.com/mig-1g.5gb, etc.
		"instaslice.com/mig-1g.5gb",
		"instaslice.com/mig-2g.10gb",
		"instaslice.com/mig-7g.40gb",
	}
	const socketDir = "/var/lib/kubelet/device-plugins"

	for _, res := range resourceNames {
		mgr := deviceplugin.NewManager(res)

		sanitized := strings.ReplaceAll(res, "/", "_")
		endpoint := sanitized + ".sock"
		socketPath := filepath.Join(socketDir, endpoint)

		srv, err := deviceplugin.NewServer(mgr, socketPath, kubeConfig)
		if err != nil {
			return fmt.Errorf("failed to create device plugin server for resource %q: %w", res, err)
		}
		if err := srv.Start(ctx); err != nil {
			return fmt.Errorf("device plugin server for resource %q failed: %w", res, err)
		}

		reg := deviceplugin.NewRegistrar(socketPath, res)
		go reg.Start(ctx)
	}
	return nil
}
