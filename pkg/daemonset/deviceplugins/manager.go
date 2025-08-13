package deviceplugins

import (
	"context"
	"fmt"
	"strings"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	"github.com/openshift/instaslice-operator/pkg/constants"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// Manager tracks and monitors devices for a specific extended-resource.
// It drives discovery and health monitoring and pushes device updates.
type Manager struct {
	// ResourceName is the extended-resource string (e.g. "mig.das.com/1g.5gb").
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

// countGPUMemoryDevices calculates the total GPU memory in GB across all GPUs
// Each "device" represents 1GB of memory
func countGPUMemoryDevices(resources instav1.DiscoveredNodeResources) int {
	totalMemoryGB := int64(0)
	for _, gpu := range resources.NodeGPUs {
		// Convert memory from bytes to GB
		memoryBytes := gpu.GPUMemory.Value()
		memoryGB := memoryBytes / (1024 * 1024 * 1024)
		totalMemoryGB += memoryGB
	}
	return int(totalMemoryGB)
}

// Updates returns a channel that emits the latest available devices for ListAndWatch.
func (m *Manager) Updates() <-chan []*pluginapi.Device {
	return m.updates
}

// Start begins device discovery and health monitoring, pushing initial updates
// and then running until the context is done.
func (m *Manager) Start(ctx context.Context) {
	klog.InfoS("Starting device manager", "resource", m.ResourceName)

	var devs []*pluginapi.Device

	// Handle GPU memory resource differently
	if m.ResourceName == constants.GPUMemoryResource {
		num := countGPUMemoryDevices(m.resources)
		devs = make([]*pluginapi.Device, 0, num)
		for i := 0; i < num; i++ {
			devs = append(devs, &pluginapi.Device{
				ID:     fmt.Sprintf("gpu-mem-%d", i),
				Health: pluginapi.Healthy,
			})
		}
		klog.InfoS("Advertising GPU memory devices", "resource", m.ResourceName, "count", num)
	} else {
		// Handle MIG profile resources
		parts := strings.SplitN(m.ResourceName, "/", 2)
		if len(parts) < 2 {
			klog.ErrorS(nil, "invalid resource name", "resource", m.ResourceName)
			return
		}
		profile := unsanitizeProfileName(parts[1])
		num := countDevices(m.resources, profile)
		devs = make([]*pluginapi.Device, 0, num)
		for i := 0; i < num; i++ {
			devs = append(devs, &pluginapi.Device{
				ID:     fmt.Sprintf("%s-%d", profile, i),
				Health: pluginapi.Healthy,
			})
		}
		klog.InfoS("Advertising MIG profile devices", "resource", m.ResourceName, "profile", profile, "count", num)
	}

	select {
	case m.updates <- devs:
	default:
	}
	<-ctx.Done()
	klog.InfoS("Device manager stopped", "resource", m.ResourceName)
}
