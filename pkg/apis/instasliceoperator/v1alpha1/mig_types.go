package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type DiscoveredGPU struct {
	// gpuUuid represents the UUID of the GPU
	// +required
	GPUUUID string `json:"gpuUuid"`

	// gpuName represents the name of the GPU
	// +required
	GPUName string `json:"gpuName"`

	// gpuMemory represents the memory capacity of the GPU
	// +required
	GPUMemory resource.Quantity `json:"gpuMemory"`
}

type DiscoveredNodeResources struct {
	// nodeGpus represents the discovered mig enabled GPUs on the node
	// +required
	NodeGPUs []DiscoveredGPU `json:"nodeGpus"`

	// migPlacement represents GPU instance, compute instance with placement for a profile
	// +required
	MigPlacement map[string]Mig `json:"migPlacement"`

	// nodeResources represents the resource list of the node at boot time
	// +required
	NodeResources corev1.ResourceList `json:"nodeResources"`
}

type Mig struct {
	// placements specify vendor profile indexes and sizes
	// +required
	Placements []Placement `json:"placements"`

	// giProfileId provides the GPU instance ID of a profile
	// +required
	GIProfileID int32 `json:"giProfileId"`

	// ciProfileId provides the compute instance ID of a profile
	// +required
	CIProfileID int32 `json:"ciProfileId"`

	// ciEngProfileId provides the compute instance engineering ID of a profile
	// +optional
	CIEngProfileID int32 `json:"ciEngProfileId,omitempty"`
}

type Placement struct {
	// size represents slots consumed by a profile on GPU
	// +required
	Size int32 `json:"size"`

	// start represents the starting index driven by size for a profile
	// +required
	Start int32 `json:"start"`
}
