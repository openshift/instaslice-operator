package v1alpha1

import (
	dasv1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Type aliases

type (
	AllocationClaim         = dasv1alpha1.AllocationClaim
	AllocationClaimList     = dasv1alpha1.AllocationClaimList
	AllocationClaimState    = dasv1alpha1.AllocationClaimState
	AllocationClaimStatus   = dasv1alpha1.AllocationClaimStatus
	DASOperator             = dasv1alpha1.DASOperator
	DASOperatorSpec         = dasv1alpha1.DASOperatorSpec
	DASOperatorStatus       = dasv1alpha1.DASOperatorStatus
	DASOperatorList         = dasv1alpha1.DASOperatorList
	NodeAcceleratorSpec     = dasv1alpha1.NodeAcceleratorSpec
	NodeAcceleratorStatus   = dasv1alpha1.NodeAcceleratorStatus
	NodeAccelerator         = dasv1alpha1.NodeAccelerator
	NodeAcceleratorList     = dasv1alpha1.NodeAcceleratorList
	DiscoveredGPU           = dasv1alpha1.DiscoveredGPU
	DiscoveredNodeResources = dasv1alpha1.DiscoveredNodeResources
	Mig                     = dasv1alpha1.Mig
	Placement               = dasv1alpha1.Placement
	AllocationClaimSpec     = dasv1alpha1.AllocationClaimSpec
	EmulatedMode            = dasv1alpha1.EmulatedMode
)

// Constant aliases
const (
	AllocationClaimStatusCreated    = dasv1alpha1.AllocationClaimStatusCreated
	AllocationClaimStatusProcessing = dasv1alpha1.AllocationClaimStatusProcessing
	AllocationClaimStatusInUse      = dasv1alpha1.AllocationClaimStatusInUse
	AllocationClaimStatusOrphaned   = dasv1alpha1.AllocationClaimStatusOrphaned

	EmulatedModeEmpty    = dasv1alpha1.EmulatedModeEmpty
	EmulatedModeEnabled  = dasv1alpha1.EmulatedModeEnabled
	EmulatedModeDisabled = dasv1alpha1.EmulatedModeDisabled
	EmulatedModeDefault  = dasv1alpha1.EmulatedModeDefault
)

var (
	SchemeGroupVersion = dasv1alpha1.SchemeGroupVersion
	SchemeBuilder      = dasv1alpha1.SchemeBuilder
	AddToScheme        = dasv1alpha1.AddToScheme
)

func Resource(resource string) schema.GroupResource {
	return dasv1alpha1.Resource(resource)
}
