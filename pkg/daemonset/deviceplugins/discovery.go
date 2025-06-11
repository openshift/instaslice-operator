package deviceplugins

import (
	"context"
	"encoding/json"
	"fmt"

	nvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	"github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/typed/instasliceoperator/v1alpha1"
	utils "github.com/openshift/instaslice-operator/test/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
)

const (
	instasliceNamespace = "das-operator"
)

// MigGpuDiscoverer is an interface for MIG GPU discovery implementations.
// Discover returns the NodeAccelerator object containing the discovered
// resources for the node.
type MigGpuDiscoverer interface {
	Discover() (*instav1.NodeAccelerator, error)
}

// EmulatedMigGpuDiscoverer implements MigGpuDiscoverer for emulated mode.
type EmulatedMigGpuDiscoverer struct {
	ctx         context.Context
	nodeName    string
	instaClient v1alpha1.NodeAcceleratorInterface
}

func (e *EmulatedMigGpuDiscoverer) Discover() (*instav1.NodeAccelerator, error) {
	// Ensure Instaslice CR exists
	instaslice, err := e.instaClient.Get(e.ctx, e.nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		instaslice = &instav1.NodeAccelerator{
			ObjectMeta: metav1.ObjectMeta{Name: e.nodeName, Namespace: instasliceNamespace},
		}
		instaslice, err = e.instaClient.Create(e.ctx, instaslice, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	fake := utils.GenerateFakeCapacity(e.nodeName)
	// Update spec only; status subresource is updated separately
	instaslice.Spec = fake.Spec
	instaslice.Spec.AcceleratorType = "nvidia-mig"
	updated, err := e.instaClient.Update(e.ctx, instaslice, metav1.UpdateOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to update instaslice spec during emulation", "node", e.nodeName)
		return nil, err
	}
	klog.V(2).InfoS("Updated instaslice CR spec during emulation", "node", e.nodeName)
	// Update status subresource with full fake status
	updated.Status = fake.Status
	instaslice, err = e.instaClient.UpdateStatus(e.ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to update instaslice status during emulation", "node", e.nodeName)
		return nil, err
	}
	klog.InfoS("Emulated discovery complete", "node", e.nodeName)
	return instaslice, nil
}

// RealMigGpuDiscoverer implements MigGpuDiscoverer for real hardware.
type RealMigGpuDiscoverer struct {
	ctx         context.Context
	nodeName    string
	instaClient v1alpha1.NodeAcceleratorInterface
}

var _ MigGpuDiscoverer = &RealMigGpuDiscoverer{}

func (r *RealMigGpuDiscoverer) Discover() (*instav1.NodeAccelerator, error) {
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		err := fmt.Errorf("unable to initialize NVML: %v", ret)
		klog.ErrorS(err, "NVML initialization failed", "returnCode", ret)
		return nil, err
	}
	klog.InfoS("NVML initialized successfully")
	defer nvml.Shutdown()

	// Ensure Instaslice CR exists
	instaslice := &instav1.NodeAccelerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.nodeName,
			Namespace: instasliceNamespace,
		},
		Spec: instav1.NodeAcceleratorSpec{
			AcceleratorType: "nvidia-mig",
		},
	}
	klog.V(2).InfoS("Ensuring Instaslice CR exists", "node", r.nodeName)
	_, err := r.instaClient.Create(r.ctx, instaslice, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		klog.ErrorS(err, "Failed to create Instaslice CR", "node", r.nodeName)
		return nil, err
	}
	if apierrors.IsAlreadyExists(err) {
		klog.V(2).InfoS("Instaslice CR already exists", "node", r.nodeName)
		instaslice, err = r.instaClient.Get(r.ctx, r.nodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to get existing Instaslice CR", "node", r.nodeName)
			return nil, err
		}
		if instaslice.Spec.AcceleratorType == "" {
			instaslice.Spec.AcceleratorType = "nvidia-mig"
			if _, err := r.instaClient.Update(r.ctx, instaslice, metav1.UpdateOptions{}); err != nil {
				klog.ErrorS(err, "Failed to update accelerator type", "node", r.nodeName)
				return nil, err
			}
		}
	} else {
		klog.V(2).InfoS("Created Instaslice CR", "node", r.nodeName)
	}

	// Discover GPUs (moved from discoverAvailableProfilesOnGpus)
	klog.InfoS("Discovering available MIG-enabled GPUs")
	var discovered []string
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		err := fmt.Errorf("error getting device count: %v", ret)
		klog.ErrorS(err, "Failed to get device count", "returnCode", ret)
		return nil, err
	}
	klog.V(2).InfoS("Number of GPUs detected", "count", count)
	migResources := instav1.DiscoveredNodeResources{
		NodeGPUs:      []instav1.DiscoveredGPU{},
		MigPlacement:  make(map[string]instav1.Mig),
		NodeResources: corev1.ResourceList{},
	}
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			err := fmt.Errorf("error getting handle for GPU %d: %v", i, ret)
			klog.ErrorS(err, "Error getting GPU handle", "index", i, "returnCode", ret)
			return nil, err
		}
		mode, _, ret := dev.GetMigMode()
		if ret != nvml.SUCCESS {
			err := fmt.Errorf("error getting MIG mode for GPU %d: %v", i, ret)
			klog.ErrorS(err, "Error getting MIG mode", "index", i, "returnCode", ret)
			return nil, err
		}
		if mode != nvml.DEVICE_MIG_ENABLE {
			klog.V(5).InfoS("Skipping GPU: MIG not enabled", "index", i)
			continue
		}
		uuid, _ := dev.GetUUID()
		name, _ := dev.GetName()
		memInfo, ret := dev.GetMemoryInfo()
		if ret != nvml.SUCCESS {
			err := fmt.Errorf("error getting memory for GPU %s: %v", uuid, ret)
			klog.ErrorS(err, "Error getting memory info for GPU", "uuid", uuid, "returnCode", ret)
			return nil, err
		}
		migResources.NodeGPUs = append(migResources.NodeGPUs, instav1.DiscoveredGPU{
			GPUUUID:   uuid,
			GPUName:   name,
			GPUMemory: *resource.NewQuantity(int64(memInfo.Total), resource.BinarySI),
		})
		discovered = append(discovered, uuid)
		klog.V(2).InfoS("Discovered MIG-enabled GPU", "uuid", uuid, "name", name, "memoryBytes", memInfo.Total)
	}
	klog.InfoS("Completed GPU discovery", "discoveredCount", len(migResources.NodeGPUs))

	raw, err := json.Marshal(&migResources)
	if err != nil {
		return nil, err
	}
	instaslice.Status.NodeResources = runtime.RawExtension{Raw: raw}

	instaslice, err = r.instaClient.UpdateStatus(r.ctx, instaslice, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}

	klog.InfoS("Discovered MIG-enabled GPUs on node", "node", r.nodeName, "uuids", discovered)
	return instaslice, nil
}
