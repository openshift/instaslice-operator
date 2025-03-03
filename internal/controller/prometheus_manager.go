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
			Name: "instaslice_current_deployed_pod",
			Help: "Pods that are currently running with their slice/s usage.",
		},
			[]string{"node", "gpu_id", "namespace", "podname", "profile"}), // Labels: node, GPU ID, namespace, podname, profile
		// compatible profiles with remaining gpu slices
		compatibleProfiles: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_compatible_profiles",
			Help: "Profiles compatible with remaining GPU slices in a node and their counts.",
		},
			[]string{"profile", "node"}), // Labels: profile, node
		// total processed slices
		processedSlices: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_total_processed_gpu_slices",
			Help: "Number of total processed GPU slices since instaslice controller start time.",
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
	if processedSlices == 8 { // special handle for 7g.40gb profile
		processedSlices = 7
	} else if startPos == 4 && processedSlices == 4 { // fills gpu and it doesnt need an extra slice
		processedSlices = 3
	} else if startPos == 6 && processedSlices == 2 { // fills gpu and it doesnt need an extra slice
		processedSlices = 1
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
	if namespace == "" && podname == "" && profile == "" {
		return nil
	}
	instasliceMetrics.deployedPodTotal.WithLabelValues(
		nodeName, gpuID, namespace, podname, profile).Set(float64(size))
	ctrl.Log.Info(fmt.Sprintf("[UpdateDeployedPodTotalMetrics] Updated Deployed Pod: %d used slice/s, for node -> %v, GPUID -> %v, namespace -> %v, podname -> %v, profile -> %v", size, nodeName, gpuID, namespace, podname, profile)) // trace
	return nil
}

// UpdateCompatibleProfilesMetrics updates metrics based on remaining GPU slices and calculates compatible profiles dynamically
func (r *InstasliceReconciler) UpdateCompatibleProfilesMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName string, remainingSlices map[string]int32) error {
	// Track currently compatible profiles
	currentProfiles := make(map[string]int32)
	sortedGPUs := sortGPUs(&instasliceObj)
	// Iterate over each profile
	for profileName, migPlacement := range instasliceObj.Status.NodeResources.MigPlacement {
		if len(migPlacement.Placements) > 0 {
			size := migPlacement.Placements[0].Size
			totalFit := int32(0)
			// Iterate over all GPUs
			for _, gpuID := range sortedGPUs {
				totalFit += r.getTotalFitForProfileOnGPU(&instasliceObj, profileName, size, remainingSlices[gpuID])
			}
			currentProfiles[profileName] = totalFit

		}
	}
	// Update Prometheus metrics
	for profileName, totalFit := range currentProfiles {
		instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, nodeName).
			Set(float64(totalFit))
		ctrl.Log.Info("[UpdateCompatibleProfilesMetrics] Updated compatible profile", "profile", profileName, "count", totalFit)
	}

	return nil
}
