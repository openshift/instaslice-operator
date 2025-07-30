package deviceplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	nvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	applyconfigurationdasoperatorv1alpha1 "github.com/openshift/instaslice-operator/pkg/generated/applyconfiguration/dasoperator/v1alpha1"
	"github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/typed/dasoperator/v1alpha1"
	utils "github.com/openshift/instaslice-operator/test/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	applyconfigurationmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	dynamic "k8s.io/client-go/dynamic"
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
	// Update status subresource with full fake status and set Ready condition
	updated.Status = fake.Status
	ready := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "GPUsAccessible",
		Message:            "All discovered GPUs are accessible and the driver is healthy.",
		ObservedGeneration: updated.Generation,
	}
	meta.SetStatusCondition(&updated.Status.Conditions, ready)
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
	ctx           context.Context
	nodeName      string
	instaClient   v1alpha1.NodeAcceleratorInterface
	dynamicClient dynamic.Interface
}

var _ MigGpuDiscoverer = &RealMigGpuDiscoverer{}

func (r *RealMigGpuDiscoverer) Discover() (*instav1.NodeAccelerator, error) {

	if err := EnsureNvmlInitialized(); err != nil {
		klog.ErrorS(err, "NVML initialization failed")
		return nil, err
	}

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
	discoverProfiles := true
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
		if discoverProfiles {
			for j := 0; j < nvml.GPU_INSTANCE_PROFILE_COUNT; j++ {
				giInfo, ret := dev.GetGpuInstanceProfileInfo(j)
				if ret == nvml.ERROR_NOT_SUPPORTED || ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					err := fmt.Errorf("error getting GI profile info %d: %v", j, ret)
					klog.ErrorS(err, "Error getting GI profile info", "index", j, "returnCode", ret)
					return nil, err
				}
				profile := NewMigProfile(j, j, nvml.COMPUTE_INSTANCE_ENGINE_PROFILE_SHARED, giInfo.SliceCount, giInfo.SliceCount, giInfo.MemorySizeMB, memInfo.Total)
				giPlacements, ret := dev.GetGpuInstancePossiblePlacements(&giInfo)
				if ret == nvml.ERROR_NOT_SUPPORTED || ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					err := fmt.Errorf("error getting placements for GI profile %d: %v", j, ret)
					klog.ErrorS(err, "Error getting GI placements", "index", j, "returnCode", ret)
					return nil, err
				}
				placements := []instav1.Placement{}
				for _, p := range giPlacements {
					placements = append(placements, instav1.Placement{Size: int32(p.Size), Start: int32(p.Start)})
				}
				migResources.MigPlacement[profile.String()] = instav1.Mig{
					Placements:     placements,
					GIProfileID:    int32(j),
					CIProfileID:    int32(profile.CIProfileID),
					CIEngProfileID: int32(profile.CIEngProfileID),
				}
			}
			discoverProfiles = false
		}
		discovered = append(discovered, uuid)
		klog.InfoS("Discovered MIG-enabled GPU", "uuid", uuid, "name", name, "memoryBytes", memInfo.Total)
	}
	klog.InfoS("Completed GPU discovery", "discoveredCount", len(migResources.NodeGPUs))

	raw, err := json.Marshal(&migResources)
	if err != nil {
		return nil, err
	}
	instaslice.Status.NodeResources = runtime.RawExtension{Raw: raw}

	ready := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "GPUsAccessible",
		Message:            "All discovered GPUs are accessible and the driver is healthy.",
		ObservedGeneration: instaslice.Generation,
	}
	meta.SetStatusCondition(&instaslice.Status.Conditions, ready)

	// Use server-side apply to avoid resource version conflicts.
	applyConfig := applyconfigurationdasoperatorv1alpha1.NodeAccelerator(instaslice.Name, instaslice.Namespace)
	applyConfig.WithStatus(&applyconfigurationdasoperatorv1alpha1.NodeAcceleratorStatusApplyConfiguration{})
	applyConfig.Status.WithNodeResources(instaslice.Status.NodeResources)

	now := metav1.Now()
	applyConfig.Status.WithConditions(&applyconfigurationmetav1.ConditionApplyConfiguration{
		Type:               &ready.Type,
		Status:             &ready.Status,
		Reason:             &ready.Reason,
		Message:            &ready.Message,
		ObservedGeneration: &ready.ObservedGeneration,
		LastTransitionTime: &now,
	})

	instaslice, err = r.instaClient.ApplyStatus(r.ctx, applyConfig, metav1.ApplyOptions{FieldManager: "das-daemonset"})
	if err != nil {
		return nil, err
	}

	klog.InfoS("Discovered MIG-enabled GPUs on node", "node", r.nodeName, "uuids", discovered)
	return instaslice, nil
}

// MigProfile represents a MIG profile in a human readable format. The NVML
// library provides integer identifiers which are cumbersome to work with
// directly.
type MigProfile struct {
	C              int
	G              int
	GB             int
	GIProfileID    int
	CIProfileID    int
	CIEngProfileID int
}

const (
	// AttributeMediaExtensions indicates the MIG profile supports media
	// extensions. This mirrors the upstream constant used in the older
	// controller implementation.
	AttributeMediaExtensions = "me"
)

// NewMigProfile constructs a new MigProfile using details from the GPU and
// compute instance profiles.
func NewMigProfile(giProfileID, ciProfileID, ciEngProfileID int, giSliceCount, ciSliceCount uint32, migMemorySizeMB, totalDeviceMemoryBytes uint64) *MigProfile {
	return &MigProfile{
		C:              int(ciSliceCount),
		G:              int(giSliceCount),
		GB:             int(getMigMemorySizeInGB(totalDeviceMemoryBytes, migMemorySizeMB)),
		GIProfileID:    giProfileID,
		CIProfileID:    ciProfileID,
		CIEngProfileID: ciEngProfileID,
	}
}

// getMigMemorySizeInGB converts the given MIG memory size in MB to a fractional
// portion of the total device memory expressed in GB. This matches the logic
// from the controller on the main branch.
func getMigMemorySizeInGB(totalDeviceMemory, migMemorySizeMB uint64) uint64 {
	const fracDenominator = 8
	const oneMB = 1024 * 1024
	const oneGB = 1024 * 1024 * 1024
	fractionalGpuMem := (float64(migMemorySizeMB) * oneMB) / float64(totalDeviceMemory)
	fractionalGpuMem = math.Ceil(fractionalGpuMem*fracDenominator) / fracDenominator
	totalMemGB := float64((totalDeviceMemory + oneGB - 1) / oneGB)
	return uint64(math.Round(fractionalGpuMem * totalMemGB))
}

// String returns the canonical string representation of the MIG profile, e.g.
// "1g.5gb".
func (m MigProfile) String() string {
	var suffix string
	if len(m.Attributes()) > 0 {
		suffix = "+" + strings.Join(m.Attributes(), ",")
	}
	if m.C == m.G {
		return fmt.Sprintf("%dg.%dgb%s", m.G, m.GB, suffix)
	}
	return fmt.Sprintf("%dc.%dg.%dgb%s", m.C, m.G, m.GB, suffix)
}

// Attributes returns any attribute strings associated with this profile.
func (m MigProfile) Attributes() []string {
	var attr []string
	switch m.GIProfileID {
	case nvml.GPU_INSTANCE_PROFILE_1_SLICE_REV1:
		attr = append(attr, AttributeMediaExtensions)
	}
	return attr
}
