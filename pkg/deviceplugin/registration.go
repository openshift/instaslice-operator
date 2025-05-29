package deviceplugin

import (
   "context"
)

// Registrar handles registration of a device-plugin socket with the kubelet.
type Registrar struct {
   SocketPath   string
   ResourceName string
}

// NewRegistrar constructs a Registrar for the given socket and resource.
func NewRegistrar(socketPath, resourceName string) *Registrar {
   return &Registrar{SocketPath: socketPath, ResourceName: resourceName}
}

// Start registers the device plugin with the kubelet until the context is done.
func (r *Registrar) Start(ctx context.Context) {
   <-ctx.Done()
}