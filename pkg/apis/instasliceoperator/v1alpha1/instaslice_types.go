package v1alpha1

import (
	operatorv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// InstasliceOperator is the Schema for the InstasliceOperator API
// +k8s:openapi-gen=true
// +genclient
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
type InstasliceOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// spec holds user settable values for configuration
	// +required
	Spec InstasliceOperatorSpec `json:"spec"`
	// status holds observed values from the cluster. They may not be overridden.
	// +optional
	Status InstasliceOperatorStatus `json:"status"`
}

// InstasliceOperatorSpec defines the desired state of InstasliceOperator
type InstasliceOperatorSpec struct {
	operatorv1.OperatorSpec `json:",inline"`

	// EmulatedMode true configures the operator to not use the GPU backend
	// +optional
	EmulatedMode EmulatedMode `json:"emulatedMode"`
}

// InstasliceOperatorStatus defines the observed state of InstasliceOperator
type InstasliceOperatorStatus struct {
	operatorv1.OperatorStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// InstasliceOperatorList contains a list of InstasliceOperator
type InstasliceOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstasliceOperator `json:"items"`
}
