package deviceplugin

import (
	"context"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"tags.cncf.io/container-device-interface/pkg/cdi"
	// UUID generator for unique spec filenames
	utiluuid "k8s.io/apimachinery/pkg/util/uuid"
)

var _ pluginapi.DevicePluginServer = (*Server)(nil)

type Server struct {
	pluginapi.UnimplementedDevicePluginServer
	Manager    *Manager
	SocketPath string
}

func NewServer(mgr *Manager, socketPath string) *Server {
	return &Server{Manager: mgr, SocketPath: socketPath}
}

func (s *Server) Start(ctx context.Context) error {
	klog.InfoS("Starting device plugin server", "socket", s.SocketPath)
	// configure CDI cache to write specs to /etc/cdi only (persistent across reboots)
	if err := cdi.Configure(cdi.WithSpecDirs(cdi.DefaultDynamicDir)); err != nil {
		klog.ErrorS(err, "failed to configure CDI spec directories")
		return fmt.Errorf("failed to configure CDI spec directories: %w", err)
	}
	// remove existing socket file, if any
	if err := os.Remove(s.SocketPath); err != nil {
		if os.IsNotExist(err) {
			klog.InfoS("Socket file does not exist, skipping removal", "socket", s.SocketPath)
		} else {
			klog.ErrorS(err, "Failed to remove existing socket file", "socket", s.SocketPath)
			return fmt.Errorf("failed to remove existing socket %q: %w", s.SocketPath, err)
		}
	} else {
		klog.InfoS("Removed existing socket file", "socket", s.SocketPath)
	}
	lis, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		klog.ErrorS(err, "Failed to listen on socket", "socket", s.SocketPath)
		return fmt.Errorf("failed to listen on socket %q: %w", s.SocketPath, err)
	}
	klog.InfoS("Listening on socket", "socket", s.SocketPath)
	grpcServer := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(grpcServer, s)
	klog.InfoS("Registered device plugin server", "socket", s.SocketPath)
	klog.InfoS("Starting device manager", "resource", s.Manager.ResourceName)
	go s.Manager.Start(ctx)
	klog.InfoS("Starting gRPC server", "socket", s.SocketPath)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			klog.ErrorS(err, "gRPC server stopped unexpectedly", "socket", s.SocketPath)
		} else {
			klog.InfoS("gRPC server stopped", "socket", s.SocketPath)
		}
	}()
	go func() {
		<-ctx.Done()
		klog.InfoS("Shutting down device plugin server", "socket", s.SocketPath)
		grpcServer.Stop()
	}()
	return nil
}

// GetDevicePluginOptions returns the options supported by the device plugin.
func (s *Server) GetDevicePluginOptions(ctx context.Context, req *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

// ListAndWatch streams the list of devices, sending initial list and subsequent updates.
func (s *Server) ListAndWatch(req *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	klog.InfoS("ListAndWatch started", "resource", s.Manager.ResourceName)
	// send initial device list
	select {
	case devs := <-s.Manager.Updates():
		if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: devs}); err != nil {
			return fmt.Errorf("failed to send initial device list: %w", err)
		}
	case <-stream.Context().Done():
		return nil
	}
	// stream updates
	for {
		select {
		case <-stream.Context().Done():
			klog.InfoS("ListAndWatch stopped", "resource", s.Manager.ResourceName)
			return nil
		case devs := <-s.Manager.Updates():
			klog.InfoS("ListAndWatch sending update", "resource", s.Manager.ResourceName, "devices", devs)
			if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: devs}); err != nil {
				return fmt.Errorf("failed to send device update: %w", err)
			}
		}
	}
}

func (s *Server) Allocate(ctx context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	klog.InfoS("Received Allocate request", "containerRequests", req.GetContainerRequests())
	count := len(req.GetContainerRequests())
	resp := &pluginapi.AllocateResponse{
		ContainerResponses: make([]*pluginapi.ContainerAllocateResponse, count),
	}

	for i := range count {
		resp.ContainerResponses[i] = &pluginapi.ContainerAllocateResponse{}

		id := utiluuid.NewUUID()
		cdiDevices, err := WriteCDISpecForResource(s.Manager.ResourceName, string(id))
		if err != nil {
			return nil, err
		}
		resp.ContainerResponses[i].CDIDevices = cdiDevices
		resp.ContainerResponses[i].Envs = map[string]string{
			"NVIDIA_VISIBLE_DEVICES": "test",
			"CUDA_VISIBLE_DEVICES":   "test",
		}

	}

	return resp, nil
}

func (s *Server) PreStartContainer(ctx context.Context, req *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
