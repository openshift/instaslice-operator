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
		sortedGPUs := sortGPUs(&instaslice)
		for _, gpuID := range sortedGPUs {
			for podUuid, allocation := range instaslice.Status.PodAllocationResults {
				if allocation.GPUUUID == gpuID {
					allocRequest := instaslice.Spec.PodAllocationRequests[podUuid]
					nodeName := allocation.Nodename
					namespace := allocRequest.PodRef.Namespace
					podname := allocRequest.PodRef.Name
					profile := allocRequest.Profile
					freeSlots, usedSlots := getFreeAndUsedSlicesCount(&instaslice, gpuID)
					// update GpuSliceMetrics
					if err := r.UpdateGpuSliceMetrics(string(nodeName), gpuID, usedSlots, freeSlots); err != nil {
						return fmt.Errorf("failed to update GPU slice metrics (nodeName: %s, gpuID: %s): %w", nodeName, gpuID, err)
					}
					// update DeployedPodTotalMetrics
					if err := r.UpdateDeployedPodTotalMetrics(string(nodeName), gpuID, namespace, podname, profile, allocation.MigPlacement.Size); err != nil {
						return fmt.Errorf("failed to update deployed pod metrics (nodeName: %s): %w", nodeName, err)
					}
				}
			}
		}
		// update CompatibleProfilesMetrics
		if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name); err != nil {
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
		sortedGPUs := sortGPUs(&instaslice)
		for _, gpuID := range sortedGPUs {
			nodeName := instaslice.Name
			if len(instaslice.Spec.PodAllocationRequests) == 0 {
				err := r.updateGpuSliceMetricsForUnallocatedGPU(instaslice, nodeName, gpuID)
				if err != nil {
					log.Error(err, "Failed to update GPU slice metrics for unallocated GPU", "nodeName", nodeName, "gpuID", gpuID)
					return err
				}
				continue
			}
		}
		// update CompatibleProfilesMetrics
		if err := r.UpdateCompatibleProfilesMetrics(instaslice, instaslice.Name); err != nil {
			log.Error(err, "Failed to update Compatible Profiles Metrics", "nodeName", instaslice.Name)
			return err
		}
	}
	return nil
}

// updateGpuSliceMetricsForUnallocatedGPU - Updates GPU metrics when no allocations exist
func (r *InstasliceReconciler) updateGpuSliceMetricsForUnallocatedGPU(instaslice inferencev1alpha1.Instaslice, nodeName, gpuID string) error {
	freeSlots, usedSlots := getFreeAndUsedSlicesCount(&instaslice, gpuID)
	if err := r.UpdateGpuSliceMetrics(nodeName, gpuID, usedSlots, freeSlots); err != nil {
		return fmt.Errorf("failed to update GPU slice metrics (nodeName: %s, gpuID: %s): %w", nodeName, gpuID, err)
	}
	return nil
}
