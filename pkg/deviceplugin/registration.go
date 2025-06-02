package deviceplugin

import (
	"context"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
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
	klog.InfoS("Starting device plugin registrar", "socket", r.SocketPath, "resource", r.ResourceName)
	endpoint := filepath.Base(r.SocketPath)
	// retry registration until successful or context is done
	for {
		select {
		case <-ctx.Done():
			klog.InfoS("Device plugin registrar stopped", "socket", r.SocketPath, "resource", r.ResourceName)
			return
		default:
		}
		cc, err := grpc.NewClient("unix://"+pluginapi.KubeletSocket, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			klog.ErrorS(err, "Failed to create gRPC client; retrying", "socket", pluginapi.KubeletSocket)
			time.Sleep(time.Second)
			continue
		}
		// attempt to connect within timeout
		dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
		cc.Connect()
		if !cc.WaitForStateChange(dialCtx, connectivity.Idle) {
			dialCancel()
			cc.Close()
			klog.ErrorS(dialCtx.Err(), "Failed to connect to kubelet socket; retrying", "socket", pluginapi.KubeletSocket)
			time.Sleep(time.Second)
			continue
		}
		dialCancel()
		client := pluginapi.NewRegistrationClient(cc)
		req := &pluginapi.RegisterRequest{
			Version:      pluginapi.Version,
			Endpoint:     endpoint,
			ResourceName: r.ResourceName,
		}
		regCtx, regCancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = client.Register(regCtx, req)
		regCancel()
		cc.Close()
		if err != nil {
			klog.ErrorS(err, "Registration failed; retrying", "socket", r.SocketPath)
			time.Sleep(time.Second)
			continue
		}
		klog.InfoS("Successfully registered device plugin", "resource", r.ResourceName, "endpoint", endpoint)
		return
	}
}
