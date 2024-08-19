package controller

import (
	"context"
	"fmt"
	"math"

	inferencev1alpha1 "codeflare.dev/instaslice/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// checks the classical resources like CPU and memory and continous GPU index available
// before making an allocation.

// find node, gpu and gpu index to place the slice
func (r *InstasliceReconciler) findNodeAndDeviceForASlice(ctx context.Context, instaslice *inferencev1alpha1.Instaslice, profileName string, policy AllocationPolicy, pod *v1.Pod) (*inferencev1alpha1.AllocationDetails, error) {
	nodeAvailableCpu, nodeAvailableMemory := r.availableClassicalResourcesOnNode(instaslice)
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
		for gpuuuid, _ := range instaslice.Spec.MigGPUUUID {
			if instaslice.Spec.Allocations == nil {
				instaslice.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
			}
			newStart := r.getStartIndexFromPreparedState(instaslice, gpuuuid, profileName)
			//size cannot be 9 atleast for A100s 40GB/80GB and H100 variants
			notValidIndex := uint32(9)
			if newStart == notValidIndex {
				//Move to next GPU
				continue
			}
			size, discoveredGiprofile, Ciprofileid, Ciengprofileid := r.extractGpuProfile(instaslice, profileName)
			allocDetails := policy.SetAllocationDetails(profileName, uint32(newStart), uint32(size),
				string(pod.UID), instaslice.Name, "creating", discoveredGiprofile,
				Ciprofileid, Ciengprofileid, pod.Namespace, pod.Name, gpuuuid, userCpuCoresCeil, userMemoryBytes)
			return allocDetails, nil
		}
	}

	return nil, fmt.Errorf("failed to find allocatable node and gpu")
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
	//TODO: remove this once we start using GPU operator with device plugin fix
	for _, item := range instaslice.Spec.Prepared {
		if item.Parent == gpuUUID {
			for i := 0; i < int(item.Size); i++ {
				gpuAllocatedIndex[int(item.Start)+i] = 1
			}

		}
	}
	// deleted allocations can be reused
	// ungated allocations are already counted in prepared
	for _, item := range instaslice.Spec.Allocations {
		if item.GPUUUID == gpuUUID && item.Allocationstatus != "deleted" && item.Allocationstatus != "ungated" {
			for i := 0; i < int(item.Size); i++ {
				gpuAllocatedIndex[int(item.Start)+i] = 1
			}
		}
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
				if value+neededContinousSlot < len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 {
						newStart = uint32(value)
						break
					}
				}

			}
			if neededContinousSlot == 4 {
				if value+neededContinousSlot < len(gpuAllocatedIndex) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 && gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 {
						newStart = uint32(value)
						break
					}
				}
			}

			if neededContinousSlot == 8 {
				//special case
				if value+neededContinousSlot < len(gpuAllocatedIndex) {
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
