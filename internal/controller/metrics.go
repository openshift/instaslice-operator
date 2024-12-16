/*
Copyright 2025.
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

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
)

// updateMetrics - updates UpdateDeployedPodTotalMetrics, UpdateGpuSliceMetrics and UpdateCompatibleProfilesMetrics
func (r *InstasliceReconciler) updateMetrics(ctx context.Context, instasliceList inferencev1alpha1.InstasliceList) error {
	log := logr.FromContext(ctx)
	for _, instaslice := range instasliceList.Items {
		// Fetch latest Instaslice state before updating metrics
		updatedInstaslice, err := r.getInstasliceObject(ctx, instaslice.Name, instaslice.Namespace)
		if err != nil {
			log.Error(err, "Failed to get latest Instaslice object", "instaslice", instaslice.Name)
			return err
		}
		// update CompatibleProfilesMetrics
		if err := r.UpdateCompatibleProfilesMetrics(*updatedInstaslice, updatedInstaslice.Name); err != nil {
			log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", updatedInstaslice.Name)
		}
	}
	return nil
}

// calculateProfileFitOnGPU handles both profile simulation fit and actual allocation size
// simulate - `true` → simulate fits | `false` → check actual allocation
func (r *InstasliceReconciler) calculateProfileFitOnGPU(instaslice *inferencev1alpha1.Instaslice, profileName, gpuUUID string, simulate bool) (int32, error) {
	// Get the GPU allocation state (already allocated slices)
	originalAllocatedIndex := r.gpuAllocatedSlices(instaslice, gpuUUID)
	gpuAllocatedIndex := make([]int32, len(originalAllocatedIndex))
	copy(gpuAllocatedIndex, originalAllocatedIndex) // Ensure we don’t modify real allocations
	// Determine the required slice size for this profile
	var neededContinuousSlot int32
	placement, exists := instaslice.Status.NodeResources.MigPlacement[profileName]
	if !exists || len(placement.Placements) == 0 {
		return 0, fmt.Errorf("profile %s not found in MigPlacement", profileName)
	}
	neededContinuousSlot = placement.Placements[0].Size
	// If we're checking actual allocation, count and return immediately
	if !simulate {
		actualSliceSize := int32(0)
		startIdx := r.getStartIndexFromPreparedState(instaslice, profileName, gpuAllocatedIndex)
		for i := int32(0); i < neededContinuousSlot; i++ {
			if startIdx+i < int32(len(originalAllocatedIndex)-1) {
				actualSliceSize++
			}
		}
		return actualSliceSize, nil // Return the **actual** allocated slice count
	}
	// If we are simulating, count how many times the profile **could fit**
	fitCount := int32(0)
	for i := 0; i < len(originalAllocatedIndex); i++ {
		startIdx := r.getStartIndexFromPreparedState(instaslice, profileName, gpuAllocatedIndex)
		// If no valid placement found, break the loop
		if startIdx == 9 {
			break
		}
		// Simulate allocation by marking the slots
		for i := int32(0); i < neededContinuousSlot; i++ {
			if startIdx+i < int32(len(gpuAllocatedIndex)) {
				gpuAllocatedIndex[startIdx+i] = 1
			}
		}
		fitCount++ // one successful fit
	}
	return fitCount, nil // total hypothetical fits
}
