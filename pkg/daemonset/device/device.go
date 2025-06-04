package device

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openshift/instaslice-operator/pkg/deviceplugin"
	"k8s.io/client-go/rest"
)

// StartDevicePlugins starts device managers, gRPC servers, and registrars for each resource.
func StartDevicePlugins(ctx context.Context, kubeConfig *rest.Config) error {
	// define the extended resources to serve
	resourceNames := []string{
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
