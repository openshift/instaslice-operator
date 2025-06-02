package deviceplugin

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// Manager tracks and monitors devices for a specific extended-resource.
// It drives discovery and health monitoring and pushes device updates.
type Manager struct {
	// ResourceName is the extended-resource string (e.g. "instaslice.com/1g.5gb").
	ResourceName string
	// updates sends the list of available devices whenever they change.
	updates chan []*pluginapi.Device
}

// NewManager creates a Manager for the given extended-resource name.
func NewManager(resourceName string) *Manager {
	return &Manager{
		ResourceName: resourceName,
		updates:      make(chan []*pluginapi.Device, 1),
	}
}

// Updates returns a channel that emits the latest available devices for ListAndWatch.
func (m *Manager) Updates() <-chan []*pluginapi.Device {
	return m.updates
}

// Start begins device discovery and health monitoring, pushing initial updates
// and then running until the context is done.
func (m *Manager) Start(ctx context.Context) {
	klog.InfoS("Starting device manager", "resource", m.ResourceName)
	// Send initial device list: advertise two healthy fake devices for the resource profile
	parts := strings.SplitN(m.ResourceName, "/", 2)
	profile := parts[len(parts)-1]
	devs := []*pluginapi.Device{
		{ID: fmt.Sprintf("%s-0", profile), Health: pluginapi.Healthy},
		{ID: fmt.Sprintf("%s-1", profile), Health: pluginapi.Healthy},
	}
	select {
	case m.updates <- devs:
	default:
	}
	<-ctx.Done()
	klog.InfoS("Device manager stopped", "resource", m.ResourceName)
}
