package device

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openshift/instaslice-operator/pkg/deviceplugin"
)

// StartDevicePlugins starts device managers, gRPC servers, and registrars for each resource.
func StartDevicePlugins(ctx context.Context) error {
	// define the extended resources to serve
	resourceNames := []string{
		"instaslice.com/1g.5gb",
		"instaslice.com/2g.10gb",
		"instaslice.com/7g.40gb",
	}
	const socketDir = "/var/lib/kubelet/device-plugins"

	for _, res := range resourceNames {
		mgr := deviceplugin.NewManager(res)

		sanitized := strings.ReplaceAll(res, "/", "_")
		endpoint := sanitized + ".sock"
		socketPath := filepath.Join(socketDir, endpoint)

		srv := deviceplugin.NewServer(mgr, socketPath)
		if err := srv.Start(ctx); err != nil {
			return fmt.Errorf("device plugin server for resource %q failed: %w", res, err)
		}

		reg := deviceplugin.NewRegistrar(socketPath, res)
		go reg.Start(ctx)
	}
	return nil
}
