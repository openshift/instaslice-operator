package deviceplugins

import (
	"context"
	"fmt"
	"strings"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// Manager tracks and monitors devices for a specific extended-resource.
// It drives discovery and health monitoring and pushes device updates.
type Manager struct {
	// ResourceName is the extended-resource string (e.g. "instaslice.com/1g.5gb").
	ResourceName string
	// resources contains the discovered node information used to determine
	// the number of devices to advertise for the resource.
	resources instav1.DiscoveredNodeResources
	// updates sends the list of available devices whenever they change.
	updates chan []*pluginapi.Device
}

// NewManager creates a Manager for the given extended-resource name.
func NewManager(resourceName string, resources instav1.DiscoveredNodeResources) *Manager {
	return &Manager{
		ResourceName: resourceName,
		resources:    resources,
		updates:      make(chan []*pluginapi.Device, 1),
	}
}

func countDevices(resources instav1.DiscoveredNodeResources, profile string) int {
	mig, ok := resources.MigPlacement[profile]
	if !ok {
		return 0
	}
	count := 0
	for _, p := range mig.Placements {
		if p.Size > 0 {
			count++
		}
	}
	return count * len(resources.NodeGPUs)
}

// Updates returns a channel that emits the latest available devices for ListAndWatch.
func (m *Manager) Updates() <-chan []*pluginapi.Device {
	return m.updates
}

// Start begins device discovery and health monitoring, pushing initial updates
// and then running until the context is done.
func (m *Manager) Start(ctx context.Context) {
	klog.InfoS("Starting device manager", "resource", m.ResourceName)
	parts := strings.SplitN(m.ResourceName, "/", 2)
	profile := strings.TrimPrefix(parts[len(parts)-1], "mig-")
	profile = unsanitizeProfileName(profile)
	num := countDevices(m.resources, profile)
	devs := make([]*pluginapi.Device, 0, num)
	for i := 0; i < num; i++ {
		devs = append(devs, &pluginapi.Device{ID: fmt.Sprintf("%s-%d", profile, i), Health: pluginapi.Healthy})
	}
	select {
	case m.updates <- devs:
	default:
	}
	<-ctx.Done()
	klog.InfoS("Device manager stopped", "resource", m.ResourceName)
}
