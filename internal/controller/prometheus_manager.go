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
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
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
func (r *InstasliceReconciler) IncrementTotalProcessedGpuSliceMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName, gpuID string, profile string) error {
	processedSlices := r.actualProfileSliceSize(&instasliceObj, profile, gpuID)
	instasliceMetrics.processedSlices.WithLabelValues(
		nodeName, gpuID).Add(float64(processedSlices))
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateGpuSliceMetrics(nodeName, gpuID string, usedSlots, freeSlots int32) error {
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName, gpuID, "used").Set(float64(usedSlots))
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName, gpuID, "free").Set(float64(freeSlots))
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateDeployedPodTotalMetrics(nodeName, gpuID, namespace, podname, profile string, size int32) error {
	if namespace == "" && podname == "" && profile == "" {
		return nil
	}
	instasliceMetrics.deployedPodTotal.WithLabelValues(
		nodeName, gpuID, namespace, podname, profile).Set(float64(size))
	return nil
}

// UpdateCompatibleProfilesMetrics updates metrics based on remaining GPU slices and calculates compatible profiles dynamically
func (r *InstasliceReconciler) UpdateCompatibleProfilesMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName string) error {
	sortedGPUs := sortGPUs(&instasliceObj)
	// Iterate over each profile
	for profileName, migPlacement := range instasliceObj.Status.NodeResources.MigPlacement {
		if len(migPlacement.Placements) > 0 {
			totalFit := int32(0)
			// Iterate over all available GPUs
			for _, gpuID := range sortedGPUs {
				totalFit += r.getTotalFitForProfileOnGPU(&instasliceObj, profileName, gpuID)
			}
			// Update Prometheus metrics
			instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, nodeName).
				Set(float64(totalFit))
		}
	}
	return nil
}
