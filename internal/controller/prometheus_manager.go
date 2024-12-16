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
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

type InstasliceMetrics struct {
	compatibleProfiles *prometheus.GaugeVec
	processedSlices    *prometheus.GaugeVec
	deployedPodTotal   *prometheus.GaugeVec
}

var (
	instasliceMetrics = &InstasliceMetrics{
		// deployed pod total
		deployedPodTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_pod_processed_slices",
			Help: "Pods that are processed with their slice/s allocation.",
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
	metrics.Registry.MustRegister(instasliceMetrics.compatibleProfiles, instasliceMetrics.processedSlices, instasliceMetrics.deployedPodTotal)
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) IncrementTotalProcessedGpuSliceMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName, gpuID string, profile string, pod *v1.Pod) error {
	processedSlices, err := r.calculateProfileFitOnGPU(&instasliceObj, profile, gpuID, false, pod)
	if err != nil {
		return fmt.Errorf("failed to calculate processed GPU slices for profile %s: %w", profile, err)
	}
	instasliceMetrics.processedSlices.WithLabelValues(nodeName, gpuID).Add(float64(processedSlices))
	return nil
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateDeployedPodTotalMetrics(nodeName, gpuID, namespace, podname, profile string, size int32) error {
	if namespace == "" && podname == "" && profile == "" {
		return fmt.Errorf("[UpdateDeployedPodTotalMetrics] : missing required parameters, no prometheus update needed")
	}
	instasliceMetrics.deployedPodTotal.WithLabelValues(
		nodeName, gpuID, namespace, podname, profile).Set(float64(size))
	return nil
}

// UpdateCompatibleProfilesMetrics updates metrics based on remaining GPU slices and calculates compatible profiles dynamically
// TODO: store metrics per gpu and when there is an update, calculate a fit for only one GPU instead of all GPUs on the host
func (r *InstasliceReconciler) UpdateCompatibleProfilesMetrics(instasliceObj inferencev1alpha1.Instaslice, nodeName string) error {
	sortedGPUs := sortGPUs(&instasliceObj)
	// Iterate over each profile
	for profileName, migPlacement := range instasliceObj.Status.NodeResources.MigPlacement {
		if len(migPlacement.Placements) > 0 {
			totalFit := int32(0)
			// Iterate over all available GPUs
			for _, gpuID := range sortedGPUs {
				fit, err := r.calculateProfileFitOnGPU(&instasliceObj, profileName, gpuID, true, nil)
				if err != nil {
					return fmt.Errorf("failed to calculate compatible profiles for profile %s: %w", profileName, err)
				}
				totalFit += fit
			}
			// Update Prometheus metrics
			instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, nodeName).Set(float64(totalFit))
		}
	}
	return nil
}
