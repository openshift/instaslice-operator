package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AllocationSpec defines the desired state for a GPU slice allocation. It
// combines fields from AllocationRequest excluding Resources.
type AllocationSpec struct {
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
// Allocation is the Schema for GPU slice allocation custom resource.
type Allocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired allocation
	// +optional
	Spec AllocationSpec `json:"spec,omitempty"`

        // status describes the current allocation state
        // +optional
        Status AllocationStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// AllocationList contains a list of Allocation resources
// +kubebuilder:object:root=true
// +optional
type AllocationList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Allocation `json:"items"`
}
