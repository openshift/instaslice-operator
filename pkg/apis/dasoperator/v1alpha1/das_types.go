package v1alpha1

import (
	operatorv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type (
	EmulatedMode string
)

const (
	EmulatedModeEmpty    = ""
	EmulatedModeEnabled  = "enabled"
	EmulatedModeDisabled = "disabled"
	EmulatedModeDefault  = EmulatedModeDisabled
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DASOperator is the Schema for the DASOperator API
// +k8s:openapi-gen=true
// +genclient
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
type DASOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// spec holds user settable values for configuration
	// +required
	Spec DASOperatorSpec `json:"spec"`
	// status holds observed values from the cluster. They may not be overridden.
	// +optional
	Status DASOperatorStatus `json:"status"`
}

// DASOperatorSpec defines the desired state of DASOperator
type DASOperatorSpec struct {
	operatorv1.OperatorSpec `json:",inline"`

	// NodeSelector allows additional node label filters for the daemonset pods
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

// DASOperatorStatus defines the observed state of DASOperator
type DASOperatorStatus struct {
	operatorv1.OperatorStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DASOperatorList contains a list of DASOperator
type DASOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DASOperator `json:"items"`
}

type NodeAcceleratorSpec struct {
	// AcceleratorType describes the accelerator class or vendor.
	// Examples: "nvidia-mig", "amd-mi300", "intel-xe".
	// +optional
	AcceleratorType string `json:"acceleratorType,omitempty"`
}

// +k8s:openapi-gen=true
type NodeAcceleratorStatus struct {
	// kubebuilder:validation:Optional
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions"`

	// nodeResources specifies the discovered resources of the node.
	// This is a runtime.RawExtension to allow different accelerator
	// vendors to report arbitrary status objects. For NVIDIA MIG the
	// object is a DiscoveredNodeResources struct.
	// +optional
	NodeResources runtime.RawExtension `json:"nodeResources,omitempty"`
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
