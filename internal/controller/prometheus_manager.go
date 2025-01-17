/*
Copyright 2023, 2024.

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
	"net/http"
	"strings"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var baseProfileSliceMap = map[string]uint32{ // slice profiles and respective size
	"1g.5gb":    1,
	"1g.5gb+me": 1,
	"1g.10gb":   2,
	"2g.10gb":   2,
	"3g.20gb":   4,
	"4g.20gb":   4,
	"7g.40gb":   8,
}

var profileSliceMap = generateProfileSliceMap() // including instaslice.redhat.com/mig-" & "nvidia.com/mig-

type InstasliceMetrics struct {
	GpuSliceTotal        *prometheus.GaugeVec
	PendingSliceRequests prometheus.Gauge
	compatibleProfiles   *prometheus.GaugeVec
	processedSlices      *prometheus.GaugeVec
	deployedPodTotal     *prometheus.GaugeVec
}

var (
	instasliceMetrics = &InstasliceMetrics{
		// Total number of GPU slices
		GpuSliceTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_gpu_slices_total",
			Help: "Total number of GPU slices utilized/ free per gpu in a node.",
		},
			[]string{"node", "gpu_id", "slot_status"}), // Labels: node, GPU ID, slot status.
		// Total number of GPU slices
		deployedPodTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_deployed_pod_total",
			Help: "Pods that are deployed on slices.",
		},
			[]string{"node", "gpu_id", "namespace", "podname", "profile"}), // Labels: node, GPU ID, namespace, podname, profile
		// Pending GPU slice requests
		PendingSliceRequests: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "instaslice_pending_gpu_slice_requests",
			Help: "Number of pending GPU slice requests.",
		}),
		// compatible profiles with remaining gpu slices
		compatibleProfiles: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_gpu_compatible_profiles",
			Help: "Profiles compatible with remaining GPU slices.",
		},
			[]string{"profile", "node", "remaining_slices"}),
		// processed slices
		processedSlices: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_total_processed_gpu_slices",
			Help: "Number of total processed GPU slices.",
		},
			[]string{"node", "gpu_id"}),
	}

	prometheusRegistry *prometheus.Registry
	prometheusHandler  http.Handler
)

// RegisterMetrics registers all Prometheus metrics
func RegisterMetrics() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(instasliceMetrics.GpuSliceTotal, instasliceMetrics.PendingSliceRequests, instasliceMetrics.compatibleProfiles, instasliceMetrics.processedSlices, instasliceMetrics.deployedPodTotal)
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) IncrementTotalProcessedGpuSliceMetrics(nodeName, gpuID string, processedSlices uint32) error {
	instasliceMetrics.processedSlices.WithLabelValues(
		nodeName, gpuID).Add(float64(processedSlices))
	ctrl.Log.Info(fmt.Sprintf("[IncrementTotalProcessedGpuSliceMetrics] Incremented Total Processed GPU Slices: %d total processed slices, for node -> %v, GPUID -> %v.", processedSlices, nodeName, gpuID)) // trace
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateGpuSliceMetrics(nodeName, gpuID string, usedSlots, freeSlots uint32) error {
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName, gpuID, "used").Set(float64(usedSlots))
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName, gpuID, "free").Set(float64(freeSlots))
	// log check
	// ctrl.Log.Info("GpuSliceTotal metric updated",
	// 	"node", nodeName, "gpuID", gpuID, "used", usedSlots,
	// 	"value", instasliceMetrics.GpuSliceTotal.WithLabelValues(nodeName, gpuID, namespace, podname, profile, "used").Desc())
	ctrl.Log.Info(fmt.Sprintf("[UpdateGpuSliceMetrics] Updated GPU Slices: %d used slot/s, %d freeslot for node -> %v, GPUID -> %v", usedSlots, freeSlots, nodeName, gpuID)) // trace
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateDeployedPodTotalMetrics(nodeName, gpuID, namespace, podname, profile string, size uint32) error {
	instasliceMetrics.deployedPodTotal.WithLabelValues(
		nodeName, gpuID, namespace, podname, profile).Set(float64(size))
	ctrl.Log.Info(fmt.Sprintf("[UpdateDeployedPodTotalMetrics] Updated Deployed Pod: %d used slice/s, for node -> %v, GPUID -> %v, namespace -> %v, podname -> %v, profile -> %v", size, nodeName, gpuID, namespace, podname, profile)) // trace
	return nil
}

// UpdatePendingSliceRequests updates the metric for pending GPU slice requests
func (r *InstasliceReconciler) UpdatePendingSliceRequests(count uint32) error {
	instasliceMetrics.PendingSliceRequests.Set(float64(count))
	ctrl.Log.Info(fmt.Sprintf("[UpdatePendingSliceRequests] Updating Pending Slice Requests %d", count)) // trace
	return nil
}

// UpdateCompatibleProfilesMetrics updates metrics based on remaining GPU slices and calculates compatible profiles dynamically
func (r *InstasliceReconciler) UpdateCompatibleProfilesMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName string, remainingSlices map[string]uint32) error {
	totalRemaining := uint32(0)
	for _, remaining := range remainingSlices {
		totalRemaining += remaining
	}
	// Reset compatible profiles
	instasliceMetrics.compatibleProfiles.Reset()
	ctrl.Log.Info("Reset compatible profiles metric")

	recommendedProfileMap := map[string]int{
		"1g.5gb":    1,
		"1g.10gb":   2,
		"1g.5gb+me": 3,
		"2g.10gb":   4,
		"3g.20gb":   5,
		"4g.20gb":   6,
		"7g.40gb":   7,
	}

	// Maintain a map to track currently compatible profiles
	currentProfiles := make(map[string]struct{})

	// Parse the MIG placements for profiles and their sizes
	for _, migPlacement := range instasliceObj.Spec.Migplacement {
		profileName := migPlacement.Profile

		// Skip if profile is already recommended
		if _, exists := currentProfiles[profileName]; exists {
			continue
		}

		// Check the first size value from the placements for this profile
		if len(migPlacement.Placements) > 0 {
			size := uint32(migPlacement.Placements[0].Size)

			// Check if the profile is compatible with any remaining slices
			for gpuID, remaining := range remainingSlices {
				if size <= remaining {
					currentProfiles[profileName] = struct{}{}
					instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, nodeName, fmt.Sprintf("%d", totalRemaining)).Set(float64(recommendedProfileMap[profileName])) // Indicate compatibility
					ctrl.Log.Info("Added compatible profile", "profile", profileName, "size", size, "gpuID", gpuID, "remainingSlices", totalRemaining)
					break
				}
			}
		}
	}

	// Clean up metrics for profiles that are no longer compatible
	for profileName := range baseProfileSliceMap { // baseProfileSliceMap contains all possible profiles
		if _, exists := currentProfiles[profileName]; !exists {
			// Profile is no longer compatible; set its value to 0
			instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, nodeName, fmt.Sprintf("%d", totalRemaining)).Set(0)
			ctrl.Log.Info("Removed incompatible profile", "profile", profileName, "nodeName", nodeName)
		}
	}

	return nil
}

// generateProfileSliceMap generates the full map for both instaslice.redhat.com and nvidia.com
func generateProfileSliceMap() map[string]uint32 {
	prefixes := []string{"instaslice.redhat.com/mig-", "nvidia.com/mig-"}
	fullMap := make(map[string]uint32)

	for profile, slices := range baseProfileSliceMap {
		for _, prefix := range prefixes {
			fullMap[prefix+profile] = slices
		}
	}

	return fullMap
}

// getPendingGpuRequests gets pending GPU requests
func (r *InstasliceReconciler) getPendingGpuRequests(ctx context.Context, client client.Client) (uint32, error) {
	pods := &v1.PodList{}

	// Fetch all pods in the cluster
	err := client.List(ctx, pods)
	if err != nil {
		return 0, fmt.Errorf("failed to list pods: %w", err)
	}

	// Count total GPU slice requests from pending pods
	pendingSlices := uint32(0)
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodPending {
			for _, container := range pod.Spec.Containers {
				for resourceName, quantity := range container.Resources.Requests {
					if sliceCount, exists := profileSliceMap[resourceName.String()]; exists {
						// Multiply slice count by the requested quantity
						pendingSlices += sliceCount * uint32(quantity.Value())
					}
				}
			}
		}
	}

	return pendingSlices, nil
}

// isGpuRequested checks if a Pod requests GPU slices
func isGpuRequested(pod v1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		for resourceName := range container.Resources.Requests {
			if strings.HasPrefix(resourceName.String(), "instaslice.redhat.com/mig-") {
				return true
			}
		}
	}
	return false
}
