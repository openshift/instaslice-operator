package daemonset

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	nvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	versioned "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	utils "github.com/openshift/instaslice-operator/test/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	instasliceNamespace = "instaslice-system"
	quotaResourceName   = "instaslice.redhat.com/accelerator-memory-quota"
)

type resPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

// emulateDiscovery populates Instaslice CR with fake data when emulation is on.
func emulateDiscovery(ctx context.Context, config *rest.Config) error {
	nodeName := os.Getenv("NODE_NAME")
	klog.InfoS("Starting emulated discovery", "node", nodeName)
	cs, err := versioned.NewForConfig(config)
	if err != nil {
		return err
	}
	instaClient := cs.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace)
	// Ensure Instaslice CR exists
	instaslice, err := instaClient.Get(ctx, nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		instaslice = &instav1.Instaslice{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: instasliceNamespace},
		}
		instaslice, err = instaClient.Create(ctx, instaslice, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
 	fake := utils.GenerateFakeCapacity(nodeName)
 	// Update spec only; status subresource is updated separately
 	instaslice.Spec = fake.Spec
 	updated, err := instaClient.Update(ctx, instaslice, metav1.UpdateOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to update instaslice spec during emulation", "node", nodeName)
		return err
	}
	klog.V(2).InfoS("Updated instaslice CR spec during emulation", "node", nodeName)
	// Update status subresource with full fake status
	updated.Status = fake.Status
	_, err = instaClient.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to update instaslice status during emulation", "node", nodeName)
		return err
	}
	klog.InfoS("Emulated discovery complete", "node", nodeName)
	return nil
}

func createPatchData(resourceName string, quantity resource.Quantity) ([]byte, error) {
	patch := []resPatchOperation{{
		Op:    "add",
		Path:  fmt.Sprintf("/status/capacity/%s", strings.ReplaceAll(resourceName, "/", "~1")),
		Value: quantity.String(),
	}}
	return json.Marshal(patch)
}

func getMigMemorySizeInGB(totalDeviceMemory, migMemorySizeMB uint64) uint64 {
	const fracDenominator = 8
	const oneMB = 1024 * 1024
	const oneGB = 1024 * 1024 * 1024
	fractional := (float64(migMemorySizeMB) * oneMB) / float64(totalDeviceMemory)
	fractional = math.Ceil(fractional*fracDenominator) / fracDenominator
	totalGB := float64((totalDeviceMemory + oneGB - 1) / oneGB)
	return uint64(math.Round(fractional * totalGB))
}

// CalculateTotalMemoryGB sums up GPU memory in GB.
func CalculateTotalMemoryGB(gpus []instav1.DiscoveredGPU) (float64, error) {
	klog.V(2).InfoS("Calculating total GPU memory in GB", "gpuCount", len(gpus))
	var total float64
	for _, gpu := range gpus {
		total += float64(gpu.GPUMemory.AsApproximateFloat64() / (1024 * 1024 * 1024))
	}
	klog.V(2).InfoS("Calculated total GPU memory", "totalGB", total)
	return total, nil
}

// discoverAvailableProfilesOnGpus populates instaslice.Status.NodeResources.NodeGPUs
// with MIG-enabled GPUs and returns their UUIDs.
func discoverAvailableProfilesOnGpus(instaslice *instav1.Instaslice) (*instav1.Instaslice, []string, error) {
	klog.InfoS("Discovering available MIG-enabled GPUs")
	var discovered []string
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		err := fmt.Errorf("error getting device count: %v", ret)
		klog.ErrorS(err, "Failed to get device count", "returnCode", ret)
		return instaslice, discovered, err
	}
	klog.V(2).InfoS("Number of GPUs detected", "count", count)
	instaslice.Status.NodeResources.NodeGPUs = []instav1.DiscoveredGPU{}
	instaslice.Status.NodeResources.MigPlacement = make(map[string]instav1.Mig)
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			err := fmt.Errorf("error getting handle for GPU %d: %v", i, ret)
			klog.ErrorS(err, "Error getting GPU handle", "index", i, "returnCode", ret)
			return instaslice, discovered, err
		}
		mode, _, ret := dev.GetMigMode()
		if ret != nvml.SUCCESS {
			err := fmt.Errorf("error getting MIG mode for GPU %d: %v", i, ret)
			klog.ErrorS(err, "Error getting MIG mode", "index", i, "returnCode", ret)
			return instaslice, discovered, err
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
			return instaslice, discovered, err
		}
		instaslice.Status.NodeResources.NodeGPUs = append(instaslice.Status.NodeResources.NodeGPUs, instav1.DiscoveredGPU{
			GPUUUID:   uuid,
			GPUName:   name,
			GPUMemory: *resource.NewQuantity(int64(memInfo.Total), resource.BinarySI),
		})
		discovered = append(discovered, uuid)
		klog.V(2).InfoS("Discovered MIG-enabled GPU", "uuid", uuid, "name", name, "memoryBytes", memInfo.Total)
	}
	klog.InfoS("Completed GPU discovery", "discoveredCount", len(instaslice.Status.NodeResources.NodeGPUs))
	return instaslice, discovered, nil
}

// patchNodeStatusForNode patches the node status capacity for the custom quota.
func patchNodeStatusForNode(ctx context.Context, kubeClient kubernetes.Interface, nodeName string, totalMemoryGB int) error {
	quantity := resource.MustParse(fmt.Sprintf("%vGi", totalMemoryGB))
	klog.InfoS("Patching node status capacity", "node", nodeName, "quantity", quantity.String())
	patchData, err := createPatchData(quotaResourceName, quantity)
	if err != nil {
		klog.ErrorS(err, "Failed to create patch data for node status capacity", "node", nodeName)
		return err
	}
	_, err = kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, patchData, metav1.PatchOptions{}, "status")
	if err != nil {
		klog.ErrorS(err, "Failed to patch node status capacity", "node", nodeName)
		return err
	}
	klog.InfoS("Successfully patched node status capacity", "node", nodeName, "quantity", quantity.String())
	return nil
}

// discoverMigEnabledGpuWithSlices discovers MIG-enabled GPUs on the current node,
// creates or updates the Instaslice CR, and patches node capacity.
func discoverMigEnabledGpuWithSlices(ctx context.Context, config *rest.Config) error {
	nodeName := os.Getenv("NODE_NAME")
	klog.InfoS("Starting discovery of MIG-enabled GPUs", "node", nodeName)
	if nodeName == "" {
		err := fmt.Errorf("NODE_NAME environment variable is required")
		klog.ErrorS(err, "NODE_NAME environment variable is required")
		return err
	}
	// If emulation is enabled in the InstasliceOperator CR, use fake discovery
	csOp, err := versioned.NewForConfig(config)
	if err != nil {
		klog.ErrorS(err, "Failed to create operator client", "node", nodeName)
		return err
	}
	opClient := csOp.OpenShiftOperatorV1alpha1().InstasliceOperators(instasliceNamespace)
	instOp, err := opClient.Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to get InstasliceOperator", "node", nodeName)
		return err
	}
	if instOp.Spec.EmulatedMode == instav1.EmulatedModeEnabled {
		klog.InfoS("Emulated mode enabled, using fake discovery", "node", nodeName)
		if err := emulateDiscovery(ctx, config); err != nil {
			klog.ErrorS(err, "Emulated discovery failed", "node", nodeName)
			return err
		}
		klog.InfoS("Emulated discovery successful", "node", nodeName)
		return nil
	}
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		err := fmt.Errorf("unable to initialize NVML: %v", ret)
		klog.ErrorS(err, "NVML initialization failed", "returnCode", ret)
		return err
	}
	klog.InfoS("NVML initialized successfully")
	defer nvml.Shutdown()

	// CRD client for Instaslice
	cs, err := versioned.NewForConfig(config)
	if err != nil {
		klog.ErrorS(err, "Failed to create Instaslice CR client", "node", nodeName)
		return err
	}
	instaClient := cs.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace)

	// Ensure Instaslice CR exists
	instaslice := &instav1.Instaslice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: instasliceNamespace,
		},
	}
	klog.V(2).InfoS("Ensuring Instaslice CR exists", "node", nodeName)
	_, err = instaClient.Create(ctx, instaslice, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		klog.ErrorS(err, "Failed to create Instaslice CR", "node", nodeName)
		return err
	}
	if apierrors.IsAlreadyExists(err) {
		klog.V(2).InfoS("Instaslice CR already exists", "node", nodeName)
		instaslice, err = instaClient.Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to get existing Instaslice CR", "node", nodeName)
			return err
		}
	} else {
		klog.V(2).InfoS("Created Instaslice CR", "node", nodeName)
	}

	// Discover GPUs
	instaslice, discovered, err := discoverAvailableProfilesOnGpus(instaslice)
	if err != nil {
		return err
	}

	// Compute total memory and update status
	totalMem, err := CalculateTotalMemoryGB(instaslice.Status.NodeResources.NodeGPUs)
	if err != nil {
		return err
	}
	// Populate NodeResources.NodeResources with node allocatable
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	node, err := kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	instaslice.Status.NodeResources.NodeResources = node.Status.Allocatable

	_, err = instaClient.UpdateStatus(ctx, instaslice, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	// Patch node capacity
	if err := patchNodeStatusForNode(ctx, kubeClient, nodeName, int(totalMem)); err != nil {
		return err
	}
	klog.InfoS("Discovered MIG-enabled GPUs on node", "node", nodeName, "uuids", discovered)
	return nil
}

// addMigCapacityToNode patches node.status.capacity for each MIG profile.
func addMigCapacityToNode(ctx context.Context, config *rest.Config) error {
	nodeName := os.Getenv("NODE_NAME")
	klog.InfoS("Adding MIG capacity to node", "node", nodeName)
	cs, err := versioned.NewForConfig(config)
	if err != nil {
		return err
	}
	instaClient := cs.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace)
	instaslice, err := instaClient.Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to get instaslice CR for MIG capacity patch", "node", nodeName)
		return err
	}
	klog.V(2).InfoS("Retrieved instaslice CR", "node", nodeName)
	// Compute counts per MIG profile
	profileCounts := make(map[string]int)
	for profile, mig := range instaslice.Status.NodeResources.MigPlacement {
		cnt := 0
		for _, p := range mig.Placements {
			if p.Size > 0 {
				cnt++
			}
		}
		profileCounts[profile] = cnt
	}
	numGPUs := len(instaslice.Status.NodeResources.NodeGPUs)
	klog.V(2).InfoS("Computed MIG profile counts", "profileCounts", profileCounts, "numGPUs", numGPUs)
	// Build JSON patches
	patches := []map[string]interface{}{}
	for profile, cnt := range profileCounts {
		total := cnt * numGPUs
		resName := "instaslice.redhat.com/mig-" + profile
		patches = append(patches, map[string]interface{}{
			"op":    "replace",
			"path":  "/status/capacity/" + strings.ReplaceAll(resName, "/", "~1"),
			"value": fmt.Sprintf("%d", total),
		})
	}
	if len(patches) == 0 {
		klog.InfoS("No MIG capacity patches to apply", "node", nodeName)
		return nil
	}
	patchData, err := json.Marshal(patches)
	if err != nil {
		klog.ErrorS(err, "Failed to marshal MIG capacity patch", "node", nodeName)
		return fmt.Errorf("failed to marshal MIG capacity patch: %v", err)
	}
	klog.V(3).InfoS("MIG capacity patch data", "patchData", string(patchData))
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	_, err = kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, patchData, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to patch node MIG capacity: %v", err)
	}
	klog.InfoS("Patched node MIG capacity", "node", nodeName, "profileCounts", profileCounts)
	return nil
}
