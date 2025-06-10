package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type NodeAcceleratorSpec struct {
	// podAllocationRequests specifies the allocation requests per pod
	// +optional
	PodAllocationRequests *map[types.UID]AllocationRequest `json:"podAllocationRequests"`
}

// +k8s:openapi-gen=true
type NodeAcceleratorStatus struct {
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
	// kubebuilder:validation:Optional
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions"`

	// PodAllocationResults specify the allocation results per pod
	// kubebuilder:validation:Optional
	// +optional
	PodAllocationResults map[string]AllocationResult `json:"podAllocationResults"`

	// nodeResources specifies the discovered resources of the node
	NodeResources DiscoveredNodeResources `json:"nodeResources"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeAccelerator is the Schema for the nodeaccelerators API
// +k8s:openapi-gen=true
// +genclient
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
type NodeAccelerator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// spec specifies the GPU slice requirements by workload pods
	// +optional
	Spec NodeAcceleratorSpec `json:"spec"`

	// status provides the information about provisioned allocations and health of the NodeAccelerator object
	Status NodeAcceleratorStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// NodeAcceleratorList contains a list of NodeAccelerator resources
// +optional
type NodeAcceleratorList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// items provides the list of NodeAccelerator objects in the cluster
	// +optional
	Items []NodeAccelerator `json:"items"`
}
