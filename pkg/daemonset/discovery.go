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
	instaslice.Spec = fake.Spec
	instaslice.Status = fake.Status
	instaslice, err = instaClient.Update(ctx, instaslice, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	_, err = instaClient.UpdateStatus(ctx, instaslice, metav1.UpdateOptions{})
	return err
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
	var total float64
	for _, gpu := range gpus {
		total += float64(gpu.GPUMemory.AsApproximateFloat64() / (1024 * 1024 * 1024))
	}
	return total, nil
}

// discoverAvailableProfilesOnGpus populates instaslice.Status.NodeResources.NodeGPUs
// with MIG-enabled GPUs and returns their UUIDs.
func discoverAvailableProfilesOnGpus(instaslice *instav1.Instaslice) (*instav1.Instaslice, []string, error) {
	var discovered []string
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return instaslice, discovered, fmt.Errorf("error getting device count: %v", ret)
	}
	instaslice.Status.NodeResources.NodeGPUs = []instav1.DiscoveredGPU{}
	instaslice.Status.NodeResources.MigPlacement = make(map[string]instav1.Mig)
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return instaslice, discovered, fmt.Errorf("error getting handle for GPU %d: %v", i, ret)
		}
		mode, _, ret := dev.GetMigMode()
		if ret != nvml.SUCCESS {
			return instaslice, discovered, fmt.Errorf("error getting MIG mode for GPU %d: %v", i, ret)
		}
		if mode != nvml.DEVICE_MIG_ENABLE {
			continue
		}
		uuid, _ := dev.GetUUID()
		name, _ := dev.GetName()
		memInfo, ret := dev.GetMemoryInfo()
		if ret != nvml.SUCCESS {
			return instaslice, discovered, fmt.Errorf("error getting memory for GPU %s: %v", uuid, ret)
		}
		instaslice.Status.NodeResources.NodeGPUs = append(instaslice.Status.NodeResources.NodeGPUs, instav1.DiscoveredGPU{
			GPUUUID:   uuid,
			GPUName:   name,
			GPUMemory: *resource.NewQuantity(int64(memInfo.Total), resource.BinarySI),
		})
		discovered = append(discovered, uuid)
	}
	return instaslice, discovered, nil
}

// patchNodeStatusForNode patches the node status capacity for the custom quota.
func patchNodeStatusForNode(ctx context.Context, kubeClient kubernetes.Interface, nodeName string, totalMemoryGB int) error {
	quantity := resource.MustParse(fmt.Sprintf("%vGi", totalMemoryGB))
	patchData, err := createPatchData(quotaResourceName, quantity)
	if err != nil {
		return err
	}
	_, err = kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, patchData, metav1.PatchOptions{}, "status")
	return err
}

// discoverMigEnabledGpuWithSlices discovers MIG-enabled GPUs on the current node,
// creates or updates the Instaslice CR, and patches node capacity.
func discoverMigEnabledGpuWithSlices(ctx context.Context, config *rest.Config) error {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return fmt.Errorf("NODE_NAME environment variable is required")
	}
	// If emulation is enabled in the InstasliceOperator CR, use fake discovery
	csOp, err := versioned.NewForConfig(config)
	if err != nil {
		return err
	}
	opClient := csOp.OpenShiftOperatorV1alpha1().InstasliceOperators(instasliceNamespace) // TODO: use the correct name
	instOp, err := opClient.Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if instOp.Spec.EmulatedMode == instav1.EmulatedModeEnabled {
		return emulateDiscovery(ctx, config)
	}
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return fmt.Errorf("unable to initialize NVML: %v", ret)
	}
	defer nvml.Shutdown()

	// CRD client for Instaslice
	cs, err := versioned.NewForConfig(config)
	if err != nil {
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
	_, err = instaClient.Create(ctx, instaslice, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	if apierrors.IsAlreadyExists(err) {
		instaslice, err = instaClient.Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
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
	fmt.Printf("Discovered MIG-enabled GPUs on node %s: %v\n", nodeName, discovered)
	return nil
}

// addMigCapacityToNode patches node.status.capacity for each MIG profile.
func addMigCapacityToNode(ctx context.Context, config *rest.Config) error {
	nodeName := os.Getenv("NODE_NAME")
	cs, err := versioned.NewForConfig(config)
	if err != nil {
		return err
	}
	instaClient := cs.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace)
	instaslice, err := instaClient.Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
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
		return nil
	}
	patchData, err := json.Marshal(patches)
	if err != nil {
		return fmt.Errorf("failed to marshal MIG capacity patch: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	_, err = kubeClient.CoreV1().Nodes().Patch(ctx, nodeName, types.JSONPatchType, patchData, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to patch node MIG capacity: %v", err)
	}
	fmt.Printf("Patched node %s MIG capacity: %v\n", nodeName, profileCounts)
	return nil
}
