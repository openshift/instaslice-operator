package utils

import (
	"github.com/openshift/instaslice-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type NodeConfig struct {
	Nodes []Node `json:"Nodes"`
}

type Node struct {
	Name string `json:"name"`
	Num  string `json:"num"`
}

func GenerateFakeCapacity(nodeName string) *v1alpha1.Instaslice {
	return &v1alpha1.Instaslice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: "instaslice-system",
		},
		Spec: v1alpha1.InstasliceSpec{
			PodAllocationRequests: map[types.UID]v1alpha1.AllocationRequest{},
		},
		Status: v1alpha1.InstasliceStatus{
			PodAllocationResults: map[types.UID]v1alpha1.AllocationResult{},
			NodeResources: v1alpha1.DiscoveredNodeResources{
				NodeGPUs: []v1alpha1.DiscoveredGPU{
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
				MigPlacement: map[string]v1alpha1.Mig{
					"1g.5gb": {
						CIProfileID:    0,
						CIEngProfileID: 0,
						GIProfileID:    0,
						Placements: []v1alpha1.Placement{
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
						Placements: []v1alpha1.Placement{
							{Size: 2, Start: 0},
							{Size: 2, Start: 2},
							{Size: 2, Start: 4},
						},
					},
					"3g.20gb": {
						CIProfileID:    2,
						CIEngProfileID: 0,
						GIProfileID:    2,
						Placements: []v1alpha1.Placement{
							{Size: 4, Start: 0},
							{Size: 4, Start: 4},
						},
					},
					"4g.20gb": {
						CIProfileID:    3,
						CIEngProfileID: 0,
						GIProfileID:    3,
						Placements: []v1alpha1.Placement{
							{Size: 4, Start: 0},
						},
					},
					"7g.40gb": {
						CIProfileID:    4,
						CIEngProfileID: 0,
						GIProfileID:    4,
						Placements: []v1alpha1.Placement{
							{Size: 8, Start: 0},
						},
					},
					"1g.5gb+me": {
						CIProfileID:    7,
						CIEngProfileID: 0,
						GIProfileID:    7,
						Placements: []v1alpha1.Placement{
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
						Placements: []v1alpha1.Placement{
							{Size: 2, Start: 0},
							{Size: 2, Start: 2},
							{Size: 2, Start: 4},
							{Size: 2, Start: 6},
						},
					},
				},
				NodeResources: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("72"),
					v1.ResourceMemory: resource.MustParse("1000000000"),
				},
			},
		},
	}
}

func GenerateFakeCapacitySim(nodes *NodeConfig) []*v1alpha1.Instaslice {

	var instaslices []*v1alpha1.Instaslice

	for i := 0; i < len(nodes.Nodes); i++ {

		node := v1alpha1.Instaslice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nodes.Nodes[i].Name,
				Namespace: "instaslice-system",
			},
			Spec: v1alpha1.InstasliceSpec{
				PodAllocationRequests: map[types.UID]v1alpha1.AllocationRequest{},
			},
			Status: v1alpha1.InstasliceStatus{
				PodAllocationResults: map[types.UID]v1alpha1.AllocationResult{},
				NodeResources: v1alpha1.DiscoveredNodeResources{
					NodeGPUs: []v1alpha1.DiscoveredGPU{
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
					MigPlacement: map[string]v1alpha1.Mig{
						"1g.5gb": {
							CIProfileID:    0,
							CIEngProfileID: 0,
							GIProfileID:    0,
							Placements: []v1alpha1.Placement{
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
							Placements: []v1alpha1.Placement{
								{Size: 2, Start: 0},
								{Size: 2, Start: 2},
								{Size: 2, Start: 4},
							},
						},
						"3g.20gb": {
							CIProfileID:    2,
							CIEngProfileID: 0,
							GIProfileID:    2,
							Placements: []v1alpha1.Placement{
								{Size: 4, Start: 0},
								{Size: 4, Start: 4},
							},
						},
						"4g.20gb": {
							CIProfileID:    3,
							CIEngProfileID: 0,
							GIProfileID:    3,
							Placements: []v1alpha1.Placement{
								{Size: 4, Start: 0},
							},
						},
						"7g.40gb": {
							CIProfileID:    4,
							CIEngProfileID: 0,
							GIProfileID:    4,
							Placements: []v1alpha1.Placement{
								{Size: 8, Start: 0},
							},
						},
						"1g.5gb+me": {
							CIProfileID:    7,
							CIEngProfileID: 0,
							GIProfileID:    7,
							Placements: []v1alpha1.Placement{
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
							Placements: []v1alpha1.Placement{
								{Size: 2, Start: 0},
								{Size: 2, Start: 2},
								{Size: 2, Start: 4},
								{Size: 2, Start: 6},
							},
						},
					},
					NodeResources: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("72"),
						v1.ResourceMemory: resource.MustParse("1000000000"),
					},
				},
			},
		}

		instaslices = append(instaslices, &node)
	}

	return instaslices
}
