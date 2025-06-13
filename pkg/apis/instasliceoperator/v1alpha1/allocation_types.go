package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient
// +kubebuilder:resource:shortName=alloc
// +kubebuilder:subresource:status
// AllocationClaim is the Schema for GPU slice allocation custom resource.
type AllocationClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired allocation. This is a runtime.RawExtension to
	// allow different accelerator vendors to define their own spec objects.
	// For NVIDIA MIG the object is an AllocationClaimSpec struct.
	// +optional
	Spec runtime.RawExtension `json:"spec,omitempty"`

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
