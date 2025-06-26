package utils

import (
	"encoding/json"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/google/uuid"
)

// GenerateFakeCapacity returns a fake NodeAccelerator CR for emulated mode.
func GenerateFakeCapacity(nodeName string) *instav1.NodeAccelerator {
	return GenerateFakeCapacityA100PCIE40GB(nodeName)
}

// migPlacementA100 describes MIG placements for A100 GPUs.
func migPlacementA100() map[string]instav1.Mig {
	return map[string]instav1.Mig{
		"1g.5gb": {
			CIProfileID:    0,
			CIEngProfileID: 0,
			GIProfileID:    0,
			Placements: []instav1.Placement{
				{Size: 1, Start: 0},
				{Size: 1, Start: 1},
				{Size: 1, Start: 2},
				{Size: 1, Start: 3},
				{Size: 1, Start: 4},
				{Size: 1, Start: 5},
				{Size: 1, Start: 6},
			},
		},
		"2g.10gb": {
			CIProfileID:    1,
			CIEngProfileID: 0,
			GIProfileID:    1,
			Placements:     []instav1.Placement{{Size: 2, Start: 0}, {Size: 2, Start: 2}, {Size: 2, Start: 4}},
		},
		"3g.20gb": {
			CIProfileID:    2,
			CIEngProfileID: 0,
			GIProfileID:    2,
			Placements:     []instav1.Placement{{Size: 4, Start: 0}, {Size: 4, Start: 4}},
		},
		"4g.20gb": {
			CIProfileID:    3,
			CIEngProfileID: 0,
			GIProfileID:    3,
			Placements:     []instav1.Placement{{Size: 4, Start: 0}},
		},
		"7g.40gb": {
			CIProfileID:    4,
			CIEngProfileID: 0,
			GIProfileID:    4,
			Placements:     []instav1.Placement{{Size: 8, Start: 0}},
		},
		"1g.5gb+me": {
			CIProfileID:    7,
			CIEngProfileID: 0,
			GIProfileID:    7,
			Placements: []instav1.Placement{
				{Size: 1, Start: 0},
				{Size: 1, Start: 1},
				{Size: 1, Start: 2},
				{Size: 1, Start: 3},
				{Size: 1, Start: 4},
				{Size: 1, Start: 5},
				{Size: 1, Start: 6},
			},
		},
		"1g.10gb": {
			CIProfileID:    9,
			CIEngProfileID: 0,
			GIProfileID:    9,
			Placements:     []instav1.Placement{{Size: 2, Start: 0}, {Size: 2, Start: 2}, {Size: 2, Start: 4}, {Size: 2, Start: 6}},
		},
	}
}

// migPlacementH100 provides placements for H100 80GB class GPUs.
func migPlacementH100() map[string]instav1.Mig {
	return map[string]instav1.Mig{
		"1g.10gb": {
			CIProfileID: 0, CIEngProfileID: 0, GIProfileID: 0,
			Placements: []instav1.Placement{{1, 0}, {1, 1}, {1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}},
		},
		"1g.10gb+me": {
			CIProfileID: 7, CIEngProfileID: 0, GIProfileID: 7,
			Placements: []instav1.Placement{{1, 0}, {1, 1}, {1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}},
		},
		"1g.20gb": {
			CIProfileID: 9, CIEngProfileID: 0, GIProfileID: 9,
			Placements: []instav1.Placement{{2, 0}, {2, 2}, {2, 4}, {2, 6}},
		},
		"2g.20gb": {
			CIProfileID: 1, CIEngProfileID: 0, GIProfileID: 1,
			Placements: []instav1.Placement{{2, 0}, {2, 2}, {2, 4}},
		},
		"3g.40gb": {
			CIProfileID: 2, CIEngProfileID: 0, GIProfileID: 2,
			Placements: []instav1.Placement{{4, 0}, {4, 4}},
		},
		"4g.40gb": {
			CIProfileID: 3, CIEngProfileID: 0, GIProfileID: 3,
			Placements: []instav1.Placement{{4, 0}},
		},
		"7g.80gb": {
			CIProfileID: 4, CIEngProfileID: 0, GIProfileID: 4,
			Placements: []instav1.Placement{{8, 0}},
		},
	}
}

// migPlacementH200 returns placements for H200 GPUs.
func migPlacementH200() map[string]instav1.Mig {
	return map[string]instav1.Mig{
		"1g.18gb": {
			CIProfileID: 0, CIEngProfileID: 0, GIProfileID: 0,
			Placements: []instav1.Placement{{1, 0}, {1, 1}, {1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}},
		},
		"1g.18gb+me": {
			CIProfileID: 7, CIEngProfileID: 0, GIProfileID: 7,
			Placements: []instav1.Placement{{1, 0}, {1, 1}, {1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}},
		},
		"1g.35gb": {
			CIProfileID: 9, CIEngProfileID: 0, GIProfileID: 9,
			Placements: []instav1.Placement{{2, 0}, {2, 2}, {2, 4}, {2, 6}},
		},
		"2g.35gb": {
			CIProfileID: 1, CIEngProfileID: 0, GIProfileID: 1,
			Placements: []instav1.Placement{{2, 0}, {2, 2}, {2, 4}},
		},
		"3g.71gb": {
			CIProfileID: 2, CIEngProfileID: 0, GIProfileID: 2,
			Placements: []instav1.Placement{{4, 0}, {4, 4}},
		},
		"4g.71gb": {
			CIProfileID: 3, CIEngProfileID: 0, GIProfileID: 3,
			Placements: []instav1.Placement{{4, 0}},
		},
		"7g.141gb": {
			CIProfileID: 4, CIEngProfileID: 0, GIProfileID: 4,
			Placements: []instav1.Placement{{8, 0}},
		},
	}
}

// migPlacementA30 returns placements for A30 GPUs.
func migPlacementA30() map[string]instav1.Mig {
	return map[string]instav1.Mig{
		"1g.6gb": {
			CIProfileID: 0, CIEngProfileID: 0, GIProfileID: 0,
			Placements: []instav1.Placement{{1, 0}, {1, 1}, {1, 2}, {1, 3}},
		},
		"2g.12gb": {
			CIProfileID: 1, CIEngProfileID: 0, GIProfileID: 1,
			Placements: []instav1.Placement{{2, 0}, {2, 2}},
		},
		"3g.24gb": {
			CIProfileID: 2, CIEngProfileID: 0, GIProfileID: 2,
			Placements: []instav1.Placement{{4, 0}},
		},
		"4g.24gb": {
			CIProfileID: 3, CIEngProfileID: 0, GIProfileID: 3,
			Placements: []instav1.Placement{{4, 0}},
		},
	}
}

// generateFakeCapacityBase is a helper that builds a NodeAccelerator object for
// the provided GPU name and memory size.
func generateFakeCapacityBase(nodeName, gpuName, memory string, placement map[string]instav1.Mig) *instav1.NodeAccelerator {
	return &instav1.NodeAccelerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: "das-operator",
		},
		Spec: instav1.NodeAcceleratorSpec{
			AcceleratorType: "nvidia-mig",
		},
		Status: instav1.NodeAcceleratorStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "GPUsAccessible",
					Message:            "All discovered GPUs are accessible and the driver is healthy.",
				},
			},
			NodeResources: func() runtime.RawExtension {
				gpu1 := "GPU-" + uuid.NewString()
				gpu2 := "GPU-" + uuid.NewString()
				res := instav1.DiscoveredNodeResources{
					NodeGPUs: []instav1.DiscoveredGPU{
						{
							GPUUUID:   gpu1,
							GPUName:   gpuName,
							GPUMemory: resource.MustParse(memory),
						},
						{
							GPUUUID:   gpu2,
							GPUName:   gpuName,
							GPUMemory: resource.MustParse(memory),
						},
					},
					MigPlacement:  placement,
					NodeResources: corev1.ResourceList{},
				}
				raw, _ := json.Marshal(&res)
				return runtime.RawExtension{Raw: raw}
			}(),
		},
	}
}

// GenerateFakeCapacityA100PCIE40GB returns capacity for an A100 PCIe 40GB GPU.
func GenerateFakeCapacityA100PCIE40GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA A100-PCIE-40GB", "40Gi", migPlacementA100())
}

// GenerateFakeCapacityA100PCIE80GB returns capacity for an A100 PCIe 80GB GPU.
func GenerateFakeCapacityA100PCIE80GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA A100-PCIE-80GB", "80Gi", migPlacementA100())
}

// GenerateFakeCapacityA100SXM440GB returns capacity for an A100 SXM4 40GB GPU.
func GenerateFakeCapacityA100SXM440GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA A100-SXM4-40GB", "40Gi", migPlacementA100())
}

// GenerateFakeCapacityA100SXM480GB returns capacity for an A100 SXM4 80GB GPU.
func GenerateFakeCapacityA100SXM480GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA A100-SXM4-80GB", "80Gi", migPlacementA100())
}

// GenerateFakeCapacityH100SXM580GB returns capacity for an H100 SXM5 80GB GPU.
func GenerateFakeCapacityH100SXM580GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA H100-SXM5-80GB", "80Gi", migPlacementH100())
}

// GenerateFakeCapacityH100PCIE80GB returns capacity for an H100 PCIe 80GB GPU.
func GenerateFakeCapacityH100PCIE80GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA H100-PCIE-80GB", "80Gi", migPlacementH100())
}

// GenerateFakeCapacityH100SXM594GB returns capacity for an H100 SXM5 94GB GPU.
func GenerateFakeCapacityH100SXM594GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA H100-SXM5-94GB", "94Gi", migPlacementH100())
}

// GenerateFakeCapacityH100PCIE94GB returns capacity for an H100 PCIe 94GB GPU.
func GenerateFakeCapacityH100PCIE94GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA H100-PCIE-94GB", "94Gi", migPlacementH100())
}

// GenerateFakeCapacityH100GH20096GB returns capacity for an H100 on GH200 96GB GPU.
func GenerateFakeCapacityH100GH20096GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA H100-GH200-96GB", "96Gi", migPlacementH100())
}

// GenerateFakeCapacityH200SXM5141GB returns capacity for an H200 SXM5 141GB GPU.
func GenerateFakeCapacityH200SXM5141GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA H200-SXM5-141GB", "141Gi", migPlacementH200())
}

// GenerateFakeCapacityA30PCIE24GB returns capacity for an A30 24GB GPU.
func GenerateFakeCapacityA30PCIE24GB(nodeName string) *instav1.NodeAccelerator {
	return generateFakeCapacityBase(nodeName, "NVIDIA A30-24GB", "24Gi", migPlacementA30())
}
