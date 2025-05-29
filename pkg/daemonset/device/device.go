package device

import (
   "context"
   "path/filepath"
   "strings"

   "github.com/openshift/instaslice-operator/pkg/deviceplugin"
)

// StartDevicePlugins starts device managers, gRPC servers, and registrars for each resource.
func StartDevicePlugins(ctx context.Context) error {
   // define the extended resources to serve
   resourceNames := []string{
       "instaslice.com/1g5gb",
       "instaslice.com/2g.10gb",
       "instaslice.com/7g.40gb",
   }
   const socketDir = "/var/lib/kubelet/device-plugins"

   for _, res := range resourceNames {
       mgr := deviceplugin.NewManager(res)
       go mgr.Start(ctx)

       sanitized := strings.ReplaceAll(res, "/", "_")
       endpoint := sanitized + ".sock"
       socketPath := filepath.Join(socketDir, endpoint)

       srv := deviceplugin.NewServer(mgr, socketPath)
       go func(s *deviceplugin.Server) {
           if err := s.Start(ctx); err != nil {
               // TODO: handle server error
           }
       }(srv)

       reg := deviceplugin.NewRegistrar(socketPath, res)
       go reg.Start(ctx)
   }
   return nil
}