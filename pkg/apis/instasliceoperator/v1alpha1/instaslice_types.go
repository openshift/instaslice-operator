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

	// EmulatedMode true configures the operator to not use the GPU backend
	// +optional
	EmulatedMode EmulatedMode `json:"emulatedMode,omitempty"`

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
