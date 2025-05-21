package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type (
	AllocationStatusDaemonset  string
	AllocationStatusController string
)

const (
	AllocationStatusDeleted  AllocationStatusDaemonset  = "deleted"
	AllocationStatusDeleting AllocationStatusController = "deleting"
	AllocationStatusUngated  AllocationStatusController = "ungated"
	AllocationStatusCreating AllocationStatusController = "creating"
	AllocationStatusCreated  AllocationStatusDaemonset  = "created"
)

type AllocationRequest struct {
	// profile specifies the MIG slice profile for allocation
	// +optional
	Profile string `json:"profile"`

	// resources specifies resource requirements for the allocation
	// +optional
	Resources corev1.ResourceRequirements `json:"resources"`

	// podRef is a reference to the gated Pod requesting the allocation
	// +optional
	PodRef corev1.ObjectReference `json:"podRef"`
}

type AllocationStatus struct {
	// allocationStatusDaemonset represents the current status of the allocation from the DaemonSet's perspective
	// +optional
	AllocationStatusDaemonset string `json:"allocationStatusDaemonset"`

	// allocationStatusDaemonset represents the current status of the allocation from the Controller's perspective
	// +optional
	AllocationStatusController string `json:"allocationStatusController"`
}

type AllocationResult struct {
	// conditions provide additional information about the allocation
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// migPlacement specifies the MIG placement details
	// +required
	MigPlacement Placement `json:"migPlacement"`

	// gpuUUID represents the UUID of the selected GPU
	// +required
	GPUUUID string `json:"gpuUUID"`

	// nodename represents the name of the selected node
	// +required
	Nodename types.NodeName `json:"nodename"`

	// allocationStatus represents the current status of the allocation
	// +required
	AllocationStatus AllocationStatus `json:"allocationStatus"`

	// configMapResourceIdentifier represents the UUID used for creating the ConfigMap resource
	// +required
	ConfigMapResourceIdentifier types.UID `json:"configMapResourceIdentifier"`
}

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
