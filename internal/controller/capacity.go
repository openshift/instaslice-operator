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

package controller

import (
	"context"
	"fmt"
	"sort"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// checks the classical resources like CPU and memory and continuous GPU index available
// before making an allocation.

// find node, gpu and gpu index to place the slice
func (r *InstasliceReconciler) findNodeAndDeviceForASlice(ctx context.Context, instaslice *inferencev1alpha1.Instaslice, profileName string, policy AllocationPolicy, pod *v1.Pod) (*inferencev1alpha1.AllocationRequest, *inferencev1alpha1.AllocationResult, error) {
	updatedInstaSliceObject, err := r.getInstasliceObject(ctx, instaslice.Name, instaslice.Namespace)
	if err != nil {
		return nil, nil, err
	}

	availableResources := r.availableClassicalResourcesOnNode(updatedInstaSliceObject)
	nodeAvailableCpu := availableResources[v1.ResourceCPU]
	nodeAvailableMemory := availableResources[v1.ResourceMemory]

	cpuRequest, cpuOk := pod.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]
	if cpuOk {
		log.FromContext(ctx).Info("cpu request obtained", "pod", pod.Name, "value", cpuRequest.String())
	} else {
		log.FromContext(ctx).Info("cpu request not set for", "pod", pod.Name)
	}
	memoryRequest, memOk := pod.Spec.Containers[0].Resources.Requests[v1.ResourceMemory]
	if memOk {
		log.FromContext(ctx).Info("memory request obtained", "pod", pod.Name, "value", memoryRequest.String())
	} else {
		log.FromContext(ctx).Info("memory request not set for", "pod", pod.Name)
	}

	if cpuRequest.Cmp(nodeAvailableCpu) < 0 && memoryRequest.Cmp(nodeAvailableMemory) < 0 {
		// TODO: Discover GPU UUIDs for selection. (This may work for A100 and H100 for now.)
		gpuUUIDs := sortGPUs(updatedInstaSliceObject)
		for _, gpuuuid := range gpuUUIDs {
			if updatedInstaSliceObject.Spec.PodAllocationRequests == nil {
				updatedInstaSliceObject.Spec.PodAllocationRequests = make(map[types.UID]inferencev1alpha1.AllocationRequest)
			}

			newStart := r.getStartIndexFromPreparedState(updatedInstaSliceObject, gpuuuid, profileName)
			// For example, a newStart of 9 is considered invalid.
			notValidIndex := int32(9)
			if newStart == notValidIndex {
				// Move to next GPU if the index is not valid.
				continue
			}

			size, discoveredGiprofile, Ciprofileid, Ciengprofileid := r.extractGpuProfile(updatedInstaSliceObject, profileName)
			resourceIdentifier := pod.Spec.Containers[0].EnvFrom[0].ConfigMapRef.Name

			allocRequest, allocResult := policy.SetAllocationDetails(
				profileName,
				newStart,
				size,
				pod.GetUID(),
				types.NodeName(updatedInstaSliceObject.GetName()),
				inferencev1alpha1.AllocationStatus{AllocationStatusController: inferencev1alpha1.AllocationStatusCreating},
				discoveredGiprofile,
				Ciprofileid,
				Ciengprofileid,
				pod.GetNamespace(),
				pod.GetName(),
				gpuuuid,
				types.UID(resourceIdentifier),
				v1.ResourceList{
					v1.ResourceCPU:    cpuRequest,
					v1.ResourceMemory: memoryRequest,
				},
			)
			return allocRequest, allocResult, nil
		}
	}

	return nil, nil, fmt.Errorf("failed to find allocatable node and gpu")
}

func sortGPUs(updatedInstaSliceObject *inferencev1alpha1.Instaslice) []string {
	gpuUUIDs := make([]string, 0, len(updatedInstaSliceObject.Status.NodeResources.NodeGPUs))
	for _, discoveredGpu := range updatedInstaSliceObject.Status.NodeResources.NodeGPUs {
		gpuUUIDs = append(gpuUUIDs, discoveredGpu.GPUUUID)
	}
	sort.Strings(gpuUUIDs)
	return gpuUUIDs
}

// accounting logic that finds the correct GPU and index where a slice could be placed.
func (*InstasliceReconciler) getStartIndexFromPreparedState(instaslice *inferencev1alpha1.Instaslice, gpuUUID string, profileName string) int32 {
	//TODO: generalize, A100 and H100 have 8 indexes for 3g and 7g and 7 for rest, so go with 8 and we are bounded by
	//only valid placement indexes for a profile.
	var gpuAllocatedIndex [8]int32
	// clean slate init
	for i := range gpuAllocatedIndex {
		gpuAllocatedIndex[i] = 0
	}
	// deleted allocations can be reused
	// ungated allocations are already counted in prepared
	for _, allocResult := range instaslice.Status.PodAllocationResults {
		if allocResult.GPUUUID == gpuUUID && allocResult.AllocationStatus.AllocationStatusDaemonset != inferencev1alpha1.AllocationStatusDeleted {
			for i := 0; i < int(allocResult.MigPlacement.Size); i++ {
				gpuAllocatedIndex[int(allocResult.MigPlacement.Start)+i] = 1
			}
		}
	}
	// Check if all indices are allocated
	allAllocated := true
	for _, allocated := range gpuAllocatedIndex {
		if allocated != 1 {
			allAllocated = false
			break
		}
	}
	if allAllocated {
		// invalid index
		return int32(9)
	}
	var neededContinousSlot int32
	var possiblePlacements []int32
	for profile, placement := range instaslice.Status.NodeResources.MigPlacement {
		if profile == profileName {
			neededContinousSlot = placement.Placements[0].Size
			for _, placement := range placement.Placements {
				possiblePlacements = append(possiblePlacements, placement.Start)
			}
			break
		}
	}
	//TODO: generalize for other hardware models like A30, no slices can be placed on 9th index
	//if we return 9 then assume no valid index is found.
	var newStart = int32(9)
	for _, value := range possiblePlacements {
		if gpuAllocatedIndex[value] == 0 {
			if neededContinousSlot == 1 {
				newStart = value
				break
			}
			if neededContinousSlot == 2 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 {
						newStart = value
						break
					}
				}

			}
			if neededContinousSlot == 4 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 && gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 {
						newStart = value
						break
					}
				}
			}

			if neededContinousSlot == 8 {
				//special case
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 &&
						gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 &&
						gpuAllocatedIndex[value+4] == 0 && gpuAllocatedIndex[value+5] == 0 &&
						gpuAllocatedIndex[value+6] == 0 && gpuAllocatedIndex[value+7] == 0 {
						newStart = value
					}
				}
			}
		}

	}

	return newStart
}

func (r *InstasliceReconciler) availableClassicalResourcesOnNode(instaslice *inferencev1alpha1.Instaslice) v1.ResourceList {
	allocatedCpu := resource.MustParse("0")
	allocatedMemory := resource.MustParse("0")

	for _, allocRequest := range instaslice.Spec.PodAllocationRequests {
		if cpuReq := allocRequest.Resources.Requests.Cpu(); cpuReq != nil {
			allocatedCpu.Add(*cpuReq)
		}
		if memReq := allocRequest.Resources.Requests.Memory(); memReq != nil {
			allocatedMemory.Add(*memReq)
		}
	}

	totalNodeCpu := instaslice.Status.NodeResources.NodeResources.Cpu()
	totalNodeMem := instaslice.Status.NodeResources.NodeResources.Memory()

	availableCpu := totalNodeCpu.DeepCopy()
	availableCpu.Sub(allocatedCpu)

	availableMemory := totalNodeMem.DeepCopy()
	availableMemory.Sub(allocatedMemory)

	return v1.ResourceList{
		v1.ResourceCPU:    availableCpu,
		v1.ResourceMemory: availableMemory,
	}
}
