/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
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

	// podRef is a reference to the gated Pod requesting the allocation
	// +optional
	PodRef corev1.ObjectReference `json:"podRef"`
}

type AllocationStatus struct {
	// allocationStatusDaemonset represents the current status of the allocation from the DaemonSet's perspective
	// +optional
	AllocationStatusDaemonset `json:"allocationStatusDaemonset"`

	// allocationStatusDaemonset represents the current status of the allocation from the Controller's perspective
	// +optional
	AllocationStatusController `json:"allocationStatusController"`
}
type AllocationResult struct {
	// conditions provide additional information about the allocation
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

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

	// bootId represents the current boot id of the node
	// +kubebuilder:validation:Required
	BootID string `json:"bootId"`
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

type InstasliceSpec struct {
	// podAllocationRequests specifies the allocation requests per pod
	// +optional
	PodAllocationRequests map[types.UID]AllocationRequest `json:"podAllocationRequests"`
}

type InstasliceStatus struct {
	// conditions represent the observed state of the Instaslice object
	// For example:
	//   conditions:
	//   - type: Ready
	//     status: "True"
	//     lastTransitionTime: "2025-01-22T12:34:56Z"
	//     reason: "GPUsAccessible"
	//     message: "All discovered GPUs are accessible and the driver is healthy."
	//
	// Or, in an error scenario (driver not responding):
	//   conditions:
	//   - type: Ready
	//     status: "False"
	//     lastTransitionTime: "2025-01-22T12:34:56Z"
	//     reason: "DriverError"
	//     message: "Could not communicate with the GPU driver on the node."
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// podAllocationResults specify the allocation results per pod
	// +optional
	PodAllocationResults map[types.UID]AllocationResult `json:"podAllocationResults"`

	// nodeResources specifies the discovered resources of the node
	// +optional
	NodeResources DiscoveredNodeResources `json:"nodeResources"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Instaslice is the Schema for the instaslices API
// +kubebuilder:validation:Required
// +kubebuilder:subresource:status
type Instaslice struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec specifies the GPU slice requirements by workload pods
	// +optional
	Spec InstasliceSpec `json:"spec"`

	// status provides the information about provisioned allocations and health of the instaslice object
	// +optional
	Status InstasliceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstasliceList contains a list of Instaslice resources
// +optional
type InstasliceList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// items provides the list of instaslice objects in the cluster
	// +optional
	Items []Instaslice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instaslice{}, &InstasliceList{})
}
