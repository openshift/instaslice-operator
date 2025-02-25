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
	"fmt"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	ctrl "sigs.k8s.io/controller-runtime"
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
	GpuSliceTotal      *prometheus.GaugeVec
	compatibleProfiles *prometheus.GaugeVec
	processedSlices    *prometheus.GaugeVec
	deployedPodTotal   *prometheus.GaugeVec
}

var (
	instasliceMetrics = &InstasliceMetrics{
		// Total number of GPU slices
		GpuSliceTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_gpu_slices_total",
			Help: "Total number of GPU slices utilized and free per gpu in a node.",
		},
			[]string{"node", "gpu_id", "slot_status"}), // Labels: node, GPU ID, slot status.
		// Current deployed pod total
		deployedPodTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_current_deployed_pod_total",
			Help: "Pods that are deployed currently on slices.",
		},
			[]string{"node", "gpu_id", "namespace", "podname", "profile"}), // Labels: node, GPU ID, namespace, podname, profile
		// compatible profiles with remaining gpu slices
		compatibleProfiles: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_current_gpu_compatible_profiles",
			Help: "Profiles compatible with remaining GPU slices and their counts.",
		},
			[]string{"profile", "node"}), // Labels: profile, node
		// total processed slices
		processedSlices: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_total_processed_gpu_slices",
			Help: "Number of total processed GPU slices.",
		},
			[]string{"node", "gpu_id"}), // Labels: node, GPU ID
	}
)

// RegisterMetrics registers all Prometheus metrics
func RegisterMetrics() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(instasliceMetrics.GpuSliceTotal, instasliceMetrics.compatibleProfiles, instasliceMetrics.processedSlices, instasliceMetrics.deployedPodTotal)
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) IncrementTotalProcessedGpuSliceMetrics(nodeName, gpuID string, processedSlices int32, startPos int32, profile string) error {
	if processedSlices == 8 { // handle for 7g.40gb profile
		processedSlices = maxSlices7g40gb
	} else if profile == profile3g20gb && startPos == EndStartPos3g20gb { // fills gpu and it doesnt need an extra slice
		processedSlices = EndPosSlices3g20gb
	} else if profile == profile1g10gb && startPos == EndStartPos1g10gb { // fills gpu and it doesnt need an extra slice
		processedSlices = EndPosSlices1g10gb
	}
	instasliceMetrics.processedSlices.WithLabelValues(
		nodeName, gpuID).Add(float64(processedSlices))
	ctrl.Log.Info(fmt.Sprintf("[IncrementTotalProcessedGpuSliceMetrics] Incremented Total Processed GPU Slices: %d total processed slices, for node -> %v, GPUID -> %v.", processedSlices, nodeName, gpuID)) // trace
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateGpuSliceMetrics(nodeName, gpuID string, usedSlots, freeSlots int32) error {
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName, gpuID, "used").Set(float64(usedSlots))
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName, gpuID, "free").Set(float64(freeSlots))
	ctrl.Log.Info(fmt.Sprintf("[UpdateGpuSliceMetrics] Updated GPU Slices: %d used slot/s, %d freeslot for node -> %v, GPUID -> %v", usedSlots, freeSlots, nodeName, gpuID)) // trace
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateDeployedPodTotalMetrics(nodeName, gpuID, namespace, podname, profile string, size int32) error {
	instasliceMetrics.deployedPodTotal.WithLabelValues(
		nodeName, gpuID, namespace, podname, profile).Set(float64(size))
	ctrl.Log.Info(fmt.Sprintf("[UpdateDeployedPodTotalMetrics] Updated Deployed Pod: %d used slice/s, for node -> %v, GPUID -> %v, namespace -> %v, podname -> %v, profile -> %v", size, nodeName, gpuID, namespace, podname, profile)) // trace
	return nil
}

// UpdateCompatibleProfilesMetrics updates metrics based on remaining GPU slices and calculates compatible profiles dynamically
func (r *InstasliceReconciler) UpdateCompatibleProfilesMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName string, remainingSlices map[string]int32) error {
	// profile map with fixed indexes for prometheus
	// example for A100
	// {
	// 	"1g.5gb":    1,
	// 	"2g.10gb":   2,
	// 	"3g.20gb":   3,
	// 	"4g.20gb":   4,
	// 	"7g.40gb":   5,
	// 	"1g.10gb":   6,
	// 	"1g.5gb+me": 7,
	// }

	// Maintain a map to track currently compatible profiles
	currentProfiles := make(map[string]int32)

	// Parse the MIG placements for profiles and their sizes
	for profileName, migPlacement := range instasliceObj.Status.NodeResources.MigPlacement {

		// Skip if profile is already recommended
		if _, exists := currentProfiles[profileName]; exists {
			continue
		}

		// Check the first size value from the placements for this profile
		if len(migPlacement.Placements) > 0 {
			size := migPlacement.Placements[0].Size

			// Check if the profile is compatible with any remaining slices
			// **Track total fit across all GPUs correctly**
			totalFit := int32(0)

			for _, remaining := range remainingSlices {
				gpuFit := int32(0)
				usedSlices := int32(0)

				// **Ensure correct profile placement per GPU**
				for usedSlices+size <= remaining || (usedSlices+size-1 == remaining && (profileName == "3g.20gb" || profileName == "1g.10gb")) {
					gpuFit++
					usedSlices += size
				}

				// **Fix `7g.40gb` handling:** Ensure it fits when exactly 7 slices are available.
				if profileName == "7g.40gb" && remaining == 7 {
					gpuFit = 1
				} else if profileName == "7g.40gb" && remaining > 7 {
					gpuFit = 0
				}

				// **Accumulate per-GPU fit counts**
				totalFit += gpuFit
			}

			// Only record the profile **once** with final totalFit
			if totalFit > 0 {
				currentProfiles[profileName] = totalFit
			}
		}
	}

	// **Update metrics only once per profile**
	for profileName, totalFit := range currentProfiles {
		instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, nodeName).
			Set(float64(totalFit))

		ctrl.Log.Info("[UpdateCompatibleProfilesMetrics] Added compatible profile", "profile", profileName, "count", totalFit)
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
