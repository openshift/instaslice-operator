package utils

import (
	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// GenerateFakeCapacity returns a fake Instaslice CR for emulated mode.
func GenerateFakeCapacity(nodeName string) *instav1.Instaslice {
	return &instav1.Instaslice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: "instaslice-system",
		},
		Spec: instav1.InstasliceSpec{
			PodAllocationRequests: &map[types.UID]instav1.AllocationRequest{},
		},
		Status: instav1.InstasliceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "GPUsAccessible",
					Message:            "All discovered GPUs are accessible and the driver is healthy.",
				},
			},
			PodAllocationResults: map[string]instav1.AllocationResult{},
			NodeResources: instav1.DiscoveredNodeResources{
				NodeGPUs: []instav1.DiscoveredGPU{
					{
						GPUUUID:   "GPU-8d042338-e67f-9c48-92b4-5b55c7e5133c",
						GPUName:   "NVIDIA A100-PCIE-40GB",
						GPUMemory: resource.MustParse("40Gi"),
					},
					{
						GPUUUID:   "GPU-31cfe05c-ed13-cd17-d7aa-c63db5108c24",
						GPUName:   "NVIDIA A100-PCIE-40GB",
						GPUMemory: resource.MustParse("40Gi"),
					},
				},
				MigPlacement: map[string]instav1.Mig{
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
						Placements: []instav1.Placement{
							{Size: 2, Start: 0},
							{Size: 2, Start: 2},
							{Size: 2, Start: 4},
						},
					},
					"3g.20gb": {
						CIProfileID:    2,
						CIEngProfileID: 0,
						GIProfileID:    2,
						Placements: []instav1.Placement{
							{Size: 4, Start: 0},
							{Size: 4, Start: 4},
						},
					},
					"4g.20gb": {
						CIProfileID:    3,
						CIEngProfileID: 0,
						GIProfileID:    3,
						Placements: []instav1.Placement{
							{Size: 4, Start: 0},
						},
					},
					"7g.40gb": {
						CIProfileID:    4,
						CIEngProfileID: 0,
						GIProfileID:    4,
						Placements: []instav1.Placement{
							{Size: 8, Start: 0},
						},
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
						Placements: []instav1.Placement{
							{Size: 2, Start: 0},
							{Size: 2, Start: 2},
							{Size: 2, Start: 4},
							{Size: 2, Start: 6},
						},
					},
				},
				NodeResources: corev1.ResourceList{},
			},
		},
	}
}
