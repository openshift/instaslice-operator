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
	"math"
	"sort"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// checks the classical resources like CPU and memory and continuous GPU index available
// before making an allocation.

// find node, gpu and gpu index to place the slice
func (r *InstasliceReconciler) findNodeAndDeviceForASlice(ctx context.Context, instaslice *inferencev1alpha1.Instaslice, profileName string, policy AllocationPolicy, pod *v1.Pod) (*inferencev1alpha1.AllocationDetails, error) {
	updatedInstaSliceObject, err := r.getInstasliceObject(ctx, instaslice.Name, instaslice.Namespace)
	if err != nil {
		return nil, err
	}
	nodeAvailableCpu, nodeAvailableMemory := r.availableClassicalResourcesOnNode(updatedInstaSliceObject)
	cpuRequest, ok := pod.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]
	// a user pod can never provide cpu and memory requests in that case
	// continue with just GPU allocation
	// consider only pod requests for allocation and not limits
	var userCpuCoresCeil int64 = 0
	if ok {
		cpuMillicores := cpuRequest.MilliValue()
		cpuCores := float64(cpuMillicores) / 1000.0
		userCpuCoresCeil = int64(math.Ceil(cpuCores))
		log.FromContext(ctx).Info("cpu request obtained ", "pod", pod.Name, "value", userCpuCoresCeil)
	} else {
		log.FromContext(ctx).Info("cpu request not set for ", "pod", pod.Name)
	}
	memoryRequest, ok := pod.Spec.Containers[0].Resources.Requests[v1.ResourceMemory]
	var userMemoryBytes int64 = 0
	if ok {
		userMemoryBytes = memoryRequest.Value()
		log.FromContext(ctx).Info("memory request obtained ", "pod", pod.Name, "value", userMemoryBytes)
	} else {
		log.FromContext(ctx).Info(" memory requests not set for ", "pod", pod.Name)
	}
	if userCpuCoresCeil < nodeAvailableCpu && userMemoryBytes < nodeAvailableMemory {
		//TODO: discover this value, this may work for A100 and H100 for now.
		gpuUUIDs := sortGPUs(updatedInstaSliceObject)
		for _, gpuuuid := range gpuUUIDs {
			if updatedInstaSliceObject.Spec.Allocations == nil {
				updatedInstaSliceObject.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
			}
			newStart := r.getStartIndexFromPreparedState(updatedInstaSliceObject, gpuuuid, profileName)
			//size cannot be 9 atleast for A100s 40GB/80GB and H100 variants
			notValidIndex := uint32(9)
			if newStart == notValidIndex {
				//Move to next GPU
				continue
			}
			size, discoveredGiprofile, Ciprofileid, Ciengprofileid := r.extractGpuProfile(updatedInstaSliceObject, profileName)
			resourceIdentifier := pod.Spec.Containers[0].EnvFrom[0].ConfigMapRef.Name
			allocDetails := policy.SetAllocationDetails(profileName, newStart, uint32(size),
				string(pod.UID), updatedInstaSliceObject.Name, discoveredGiprofile,
				Ciprofileid, Ciengprofileid, pod.Namespace, pod.Name, gpuuuid, resourceIdentifier, userCpuCoresCeil, userMemoryBytes)
			return allocDetails, nil
		}
	}

	return nil, fmt.Errorf("failed to find allocatable node and gpu")
}

func sortGPUs(updatedInstaSliceObject *inferencev1alpha1.Instaslice) []string {
	gpuUUIDs := make([]string, 0, len(updatedInstaSliceObject.Spec.MigGPUUUID))
	for gpuuuid := range updatedInstaSliceObject.Spec.MigGPUUUID {
		gpuUUIDs = append(gpuUUIDs, gpuuuid)
	}
	sort.Strings(gpuUUIDs)
	return gpuUUIDs
}

// accounting logic that finds the correct GPU and index where a slice could be placed.
func (*InstasliceReconciler) getStartIndexFromPreparedState(instaslice *inferencev1alpha1.Instaslice, gpuUUID string, profileName string) uint32 {
	//TODO: generalize, A100 and H100 have 8 indexes for 3g and 7g and 7 for rest, so go with 8 and we are bounded by
	//only valid placement indexes for a profile.
	var gpuAllocatedIndex [8]uint32
	// clean slate init
	for i := range gpuAllocatedIndex {
		gpuAllocatedIndex[i] = 0
	}
	// deleted allocations can be reused
	// ungated allocations are already counted in prepared
	for uuid, item := range instaslice.Spec.Allocations {
		statuses := instaslice.Status.AllocationStatus[uuid]
		mostRecentStatus := statuses[len(statuses)-1]
		if item.GPUUUID == gpuUUID && mostRecentStatus != inferencev1alpha1.AllocationStatusDeleted {
			for i := 0; i < int(item.Size); i++ {
				gpuAllocatedIndex[int(item.Start)+i] = 1
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
		return uint32(9)
	}
	var neededContinousSlot int
	var possiblePlacements []int
	for _, placement := range instaslice.Spec.Migplacement {
		if placement.Profile == profileName {
			neededContinousSlot = placement.Placements[0].Size
			for _, placement := range placement.Placements {
				possiblePlacements = append(possiblePlacements, placement.Start)
			}
			break
		}
	}
	//TODO: generalize for other hardware models like A30, no slices can be placed on 9th index
	//if we return 9 then assume no valid index is found.
	var newStart = uint32(9)
	for _, value := range possiblePlacements {
		if gpuAllocatedIndex[value] == 0 {
			if neededContinousSlot == 1 {
				newStart = uint32(value)
				break
			}
			if neededContinousSlot == 2 {
				if value+neededContinousSlot <= len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 {
						newStart = uint32(value)
						break
					}
				}

			}
			if neededContinousSlot == 4 {
				if value+neededContinousSlot <= len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 && gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 {
						newStart = uint32(value)
						break
					}
				}
			}

			if neededContinousSlot == 8 {
				//special case
				if value+neededContinousSlot <= len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 &&
						gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 &&
						gpuAllocatedIndex[value+4] == 0 && gpuAllocatedIndex[value+5] == 0 &&
						gpuAllocatedIndex[value+6] == 0 && gpuAllocatedIndex[value+7] == 0 {
						newStart = uint32(value)
					}
				}
			}
		}

	}

	return newStart
}

func (*InstasliceReconciler) availableClassicalResourcesOnNode(instaslice *inferencev1alpha1.Instaslice) (int64, int64) {
	var allocatedCpu int64 = 0
	var allocatedMemory int64 = 0
	for _, allocations := range instaslice.Spec.Allocations {
		allocatedCpu = allocatedCpu + allocations.Cpu
		allocatedMemory = allocatedMemory + allocations.Memory
	}
	availableCpu := instaslice.Spec.CpuOnNodeAtBoot - allocatedCpu
	availableMemory := instaslice.Spec.MemoryOnNodeAtBoot - allocatedMemory
	return availableCpu, availableMemory
}
