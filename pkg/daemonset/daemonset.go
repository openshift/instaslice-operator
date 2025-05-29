package daemonset

import (
   "context"
   "path/filepath"
   "strings"

   "github.com/openshift/library-go/pkg/controller/controllercmd"
   "github.com/openshift/instaslice-operator/pkg/deviceplugin"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
   // define the extended resources to serve
   resourceNames := []string{
       "instaslice.com/1g5gb",
       "instaslice.com/2g.10gb",
       "instaslice.com/7g.40gb",
   }
   const socketDir = "/var/lib/kubelet/device-plugins"

   for _, res := range resourceNames {
       // start device manager
       mgr := deviceplugin.NewManager(res)
       go mgr.Start(ctx)

       // compute socket path for this resource
       sanitized := strings.ReplaceAll(res, "/", "_")
       endpoint := sanitized + ".sock"
       socketPath := filepath.Join(socketDir, endpoint)

       // start gRPC server
       srv := deviceplugin.NewServer(mgr, socketPath)
       go func(s *deviceplugin.Server) {
           if err := s.Start(ctx); err != nil {
               // TODO: handle server error
           }
       }(srv)

       // register with kubelet
       reg := deviceplugin.NewRegistrar(socketPath, res)
       go reg.Start(ctx)
   }

   <-ctx.Done()
   return nil
}
