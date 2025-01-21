/*
Copyright 2024.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Mig struct {
	// placements provide vendor profile indexes and sizes
	// +required
	Placements []Placement `json:"placements"`
	// profile provides name of the vendor profile
	// +required
	Profile string `json:"profile"`
	// giprofileid provides gpu instance id of a profile
	// +required
	Giprofileid int `json:"giprofileid"`
	// ciProfileid provides compute instance id of a profile
	// +required
	CIProfileID int `json:"ciProfileid"`
	// ciengprofileid provides compute instance engineering id of a profile
	// +optional
	CIEngProfileID int `json:"ciengprofileid"`
}

type Placement struct {
	// size represents solts consumed by a profile on gpu
	// +required
	Size int `json:"size"`
	// start represents the begin index driven by size for a profile
	// +required
	Start int `json:"start"`
}
type AllocationStatus string

const (
	AllocationStatusDeleted  AllocationStatus = "deleted"
	AllocationStatusDeleting AllocationStatus = "deleting"
	AllocationStatusUngated  AllocationStatus = "ungated"
	AllocationStatusCreating AllocationStatus = "creating"
	AllocationStatusCreated  AllocationStatus = "created"
)

// Define the struct for allocation details
type AllocationDetails struct {
	// profile requested by user workload
	// +required
	Profile string `json:"profile"`
	// start position of a profile on a gpu
	// +required
	Start uint32 `json:"start"`
	// size of profile that begins from start position
	// +required
	Size uint32 `json:"size"`
	// podUUID represents uuid of user workload
	// +required
	PodUUID string `json:"podUUID"`
	// gpuUUID represents gpu uuid of selected gpu
	// +required
	GPUUUID string `json:"gpuUUID"`
	// nodename represents name of the selected node
	// +required
	Nodename string `json:"nodename"`
	// allocationStatus represents status of allocation
	// +kubebuilder:validation:Enum:=deleted;deleting;ungated;creating;created
	// +required
	Allocationstatus AllocationStatus `json:"allocationStatus"`
	// resourceIdentifier represents uuid used for creating configmap resource
	// +required
	Resourceidentifier string `json:"resourceIdentifier"`
	// namespace represents namespace of user workload
	// +required
	Namespace string `json:"namespace"`
	// podName represents name of the user workload pod
	// +required
	PodName string `json:"podName"`
	// cpu represents amount of cpu requested by user workload
	// +required
	Cpu int64 `json:"cpu"`
	// memory represents amount of memory requested by user workload
	// +required
	Memory int64 `json:"memory"`
}

// InstasliceSpec defines the desired state of Instaslice
type InstasliceSpec struct {
	// migGpuUuid represents uuid of the mig device created on the gpu
	// +required
	MigGPUUUID map[string]string `json:"migGpuUuid"`
	// allocations represents allocation details of user workloads
	// +required
	Allocations map[string]AllocationDetails `json:"allocations"`
	// migplacement represents gpu instance, compute instance with placement for a profile
	// +required
	Migplacement []Mig `json:"migplacement"`
	// cpuonnodeatboot represents total amount of cpu present on the node
	// +required
	CpuOnNodeAtBoot int64 `json:"cpuonnodeatboot"`
	// memoryonnodeatboot represents total amount of memory present on the node
	// +required
	MemoryOnNodeAtBoot int64 `json:"memoryonnodeatboot"`
}

// InstasliceStatus defines the observed state of Instaslice
type InstasliceStatus struct {
	// processed represents state of the instaslice object after daemonset creation
	// +required
	Processed bool `json:"processed"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Instaslice is the Schema for the instaslices API
type Instaslice struct {
	// TypeMeta contains metadata about the API resource type.
	// It includes information such as API version and kind.
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// spec defines the current state of different allocations in the Instaslice resource.
	// It contains configuration details, such as GPU allocation settings,
	// node-specific parameters, and placement preferences.
	// +required
	Spec InstasliceSpec `json:"spec"`
	// status represents the observed state of the Instaslice resource.
	// It provides runtime information about the resource, such as whether
	// allocations have been processed.
	// +required
	Status InstasliceStatus `json:"status"`
}

//+kubebuilder:object:root=true

// InstasliceList contains a list of Instaslice
type InstasliceList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	// items represents an entry of instaslice resource per node
	// +required
	Items []Instaslice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instaslice{}, &InstasliceList{})
}
