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
		remainingSlotsPerGPU := map[string]int32{}

		// Iterate over GPUs from Status.NodeResources.NodeGPUs
		for _, gpu := range instaslice.Status.NodeResources.NodeGPUs {
			nodeName := instaslice.Name
			gpuID := gpu.GPUUUID // Extract GPU UUID

			// If no allocations exist, update metrics with all slots free
			if len(instaslice.Spec.PodAllocationRequests) == 0 {
				totalSlots, err := r.getTotalGpuSlotsForGPU(instaslice, gpuID)
				if err != nil {
					log.Error(err, "Failed to determine total GPU slots for GPU without allocations", "gpuID", gpuID)
					continue
				}
				if err := r.UpdateGpuSliceMetrics(nodeName, gpuID, 0, totalSlots); err != nil {
					log.Error(err, "Failed to update GPU slice metrics for unallocated GPU", "nodeName", nodeName, "gpuID", gpuID)
				}
				remainingSlotsPerGPU[gpuID] = totalSlots
				continue
			}

			// Check if GPU is present in allocations
			allocated := false
			for podUuid, allocation := range instaslice.Status.PodAllocationResults {
				if allocation.GPUUUID == gpuID {
					allocRequest := instaslice.Spec.PodAllocationRequests[podUuid]
					allocated = true
					nodeName := allocation.Nodename
					namespace := allocRequest.PodRef.Namespace
					podname := allocRequest.PodRef.Name
					profile := allocRequest.Profile
					usedSlots := calculateUsedSlotsForGPU(instaslice, string(nodeName), gpuID)
					totalSlots, err := r.getTotalGpuSlotsForGPU(instaslice, gpuID)
					if err != nil {
						log.Error(err, "Failed to determine total GPU slots", "nodeName", allocation.Nodename, "gpuID", gpuID)
						continue
					}
					freeSlots := totalSlots - usedSlots
					// update GpuSliceMetrics
					if err := r.UpdateGpuSliceMetrics(string(nodeName), gpuID, usedSlots, freeSlots); err != nil {
						return fmt.Errorf("failed to update GPU slice metrics (nodeName: %s, gpuID: %s): %w", nodeName, gpuID, err)
					}
					// update DeployedPodTotalMetrics
					if err = r.UpdateDeployedPodTotalMetrics(string(nodeName), gpuID, namespace, podname, profile, allocation.MigPlacement.Size); err != nil {
						return fmt.Errorf("failed to update deployed pod metrics (nodeName: %s): %w", nodeName, err)
					}
					remainingSlotsPerGPU[gpuID] = totalSlots - usedSlots
				}
			}

			// If GPU is not allocated, set usedSlots to 0 and freeSlots to totalSlots
			if !allocated {
				totalSlots, err := r.updateGpuSliceMetricsForUnallocatedGPU(instaslice, nodeName, gpuID)
				if err != nil {
					log.Error(err, "Failed to update GPU slice metrics for unallocated GPU", "nodeName", nodeName, "gpuID", gpuID)
					return err
				}
				remainingSlotsPerGPU[gpuID] = totalSlots
			}
		}
		// update CompatibleProfilesMetrics
		if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name, remainingSlotsPerGPU); err != nil {
			log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", instaslice.Name)
			return err
		}
	}
	return nil
}

// updateMetricsAllSlotsFree - If no allocations exist, update metrics with all slots free
func (r *InstasliceReconciler) updateMetricsAllSlotsFree(ctx context.Context, instasliceList inferencev1alpha1.InstasliceList) error {
	log := logr.FromContext(ctx)
	for _, instaslice := range instasliceList.Items {
		remainingSlotsPerGPU := map[string]int32{}
		for _, gpu := range instaslice.Status.NodeResources.NodeGPUs {
			nodeName := instaslice.Name
			gpuID := gpu.GPUUUID // Extract GPU UUID

			if len(instaslice.Spec.PodAllocationRequests) == 0 {
				totalSlots, err := r.updateGpuSliceMetricsForUnallocatedGPU(instaslice, nodeName, gpuID)
				if err != nil {
					log.Error(err, "Failed to update GPU slice metrics for unallocated GPU", "nodeName", nodeName, "gpuID", gpuID)
					return err
				}
				remainingSlotsPerGPU[gpuID] = totalSlots
				continue
			}
		}
		// update CompatibleProfilesMetrics
		if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name, remainingSlotsPerGPU); err != nil {
			log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", instaslice.Name)
			return err
		}
	}
	return nil
}

// updateGpuSliceMetricsForUnallocatedGPU - Updates GPU metrics when no allocations exist
func (r *InstasliceReconciler) updateGpuSliceMetricsForUnallocatedGPU(instaslice inferencev1alpha1.Instaslice, nodeName, gpuID string) (int32, error) {
	totalSlots, err := r.getTotalGpuSlotsForGPU(instaslice, gpuID)
	if err != nil {
		return 0, fmt.Errorf("failed to determine total GPU slots for unallocated GPU (gpuID: %s): %w", gpuID, err)
	}
	if err := r.UpdateGpuSliceMetrics(nodeName, gpuID, 0, totalSlots); err != nil {
		return 0, fmt.Errorf("failed to update GPU slice metrics (nodeName: %s, gpuID: %s): %w", nodeName, gpuID, err)
	}
	return totalSlots, nil
}

// Calculates used slots for a specific GPU
func calculateUsedSlotsForGPU(instaslice inferencev1alpha1.Instaslice, nodeName, gpuID string) int32 {
	usedSlots := int32(0)
	for _, allocation := range instaslice.Status.PodAllocationResults {
		if string(allocation.Nodename) == nodeName && allocation.GPUUUID == gpuID {
			if string(allocation.Nodename) == nodeName && allocation.GPUUUID == gpuID {
				if allocation.MigPlacement.Size == 8 { // handle for 7g.40gb profile
					usedSlots += 7
				} else if allocation.MigPlacement.Start == 4 && allocation.MigPlacement.Size == 4 { // fills gpu and it doesnt need an extra slice
					usedSlots += 3
				} else if allocation.MigPlacement.Start == 6 && allocation.MigPlacement.Size == 2 { // fills gpu and it doesnt need an extra slice
					usedSlots += 1
				} else {
					usedSlots += allocation.MigPlacement.Size
				}
			}
		}
	}
	return usedSlots
}

// Retrieves the total GPU slots available for a specific GPU
func (r *InstasliceReconciler) getTotalGpuSlotsForGPU(instaslice inferencev1alpha1.Instaslice, gpuID string) (int32, error) {
	const slotsPerGPU = 7
	for _, gpu := range instaslice.Status.NodeResources.NodeGPUs {
		if gpu.GPUUUID == gpuID {
			return slotsPerGPU, nil
		}
	}

	// If GPU ID is not found, return an error
	return 0, fmt.Errorf("GPU ID %s not found in Instaslice object", gpuID)
}
