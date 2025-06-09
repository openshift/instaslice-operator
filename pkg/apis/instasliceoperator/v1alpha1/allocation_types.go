package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AllocationClaimSpec defines the desired state for a GPU slice allocation. It
// combines fields from AllocationRequest excluding Resources.
type AllocationClaimSpec struct {
	// profile specifies the MIG slice profile for allocation
	// +optional
	Profile string `json:"profile,omitempty"`

	// podRef is a reference to the gated Pod requesting the allocation
	// +optional
	PodRef corev1.ObjectReference `json:"podRef,omitempty"`

	// migPlacement specifies the MIG placement details
	// +required
	MigPlacement Placement `json:"migPlacement"`

	// gpuUUID represents the UUID of the selected GPU
	// +required
	GPUUUID string `json:"gpuUUID"`

	// nodename represents the name of the selected node
	// +required
	Nodename types.NodeName `json:"nodename"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient
// +kubebuilder:resource:shortName=alloc
// +kubebuilder:subresource:status
// AllocationClaim is the Schema for GPU slice allocation custom resource.
type AllocationClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired allocation
	// +optional
	Spec AllocationClaimSpec `json:"spec,omitempty"`

	// status describes the current allocation state
	// +optional
	Status AllocationClaimStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// AllocationClaimList contains a list of AllocationClaim resources
// +kubebuilder:object:root=true
// +optional
type AllocationClaimList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AllocationClaim `json:"items"`
}

// AllocationClaimStatus represents the state of an AllocationClaim.
type AllocationClaimStatus string

const (
	AllocationClaimStatusCreated    AllocationClaimStatus = "created"
	AllocationClaimStatusProcessing AllocationClaimStatus = "processing"
	AllocationClaimStatusInUse      AllocationClaimStatus = "inUse"
	AllocationClaimStatusOrphaned   AllocationClaimStatus = "orphaned"
)
