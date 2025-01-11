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
	"github.com/prometheus/client_golang/prometheus/promhttp"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

type InstasliceMetrics struct {
	GpuSliceTotal        *prometheus.GaugeVec
	PendingSliceRequests prometheus.Gauge
	compatibleProfiles   *prometheus.GaugeVec
}

var baseProfileSliceMap = map[string]uint32{ // slice profiles and respective size
	"1g.5gb":  1,
	"1g.10gb": 2,
	"2g.10gb": 2,
	"3g.20gb": 4,
	"4g.20gb": 4,
	"7g.40gb": 8,
}

var profileSliceMap = generateProfileSliceMap() // including instaslice.redhat.com/mig-" & "nvidia.com/mig-

var (
	instasliceMetrics = &InstasliceMetrics{
		// Total number of GPU slices
		GpuSliceTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_gpu_slices_total",
			Help: "Total number of GPU slices allocated per node.",
		},
			[]string{"node", "gpu_id", "namespace", "podname", "profile", "slot_status"}), // Labels: node, GPU ID, namespace, "odname, profile, slot status.
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
			[]string{"profile", "remaining_slices"}),
	}

	prometheusRegistry *prometheus.Registry
	prometheusHandler  http.Handler
)

// InitializeMetricsExporter registers all Prometheus metrics
func (r *InstasliceReconciler) InitializeMetricsExporter() {
	// Initiate the exporting of prometheus metrics for instaslice
	ctrl.Log.Info("Entering InitializeMetricsExporter().")
	if prometheusRegistry == nil {
		ctrl.Log.Info("Prometheus registry is nil, initializing new registry and metrics.")
		prometheusRegistry = prometheus.NewRegistry()

		// Register the metrics
		prometheus.MustRegister(instasliceMetrics.GpuSliceTotal, instasliceMetrics.PendingSliceRequests, instasliceMetrics.compatibleProfiles)
		ctrl.Log.Info("Instaslice Metrics registered successfully.")

		// Create and register the Prometheus handler
		prometheusHandler = promhttp.HandlerFor(prometheusRegistry, promhttp.HandlerOpts{Registry: prometheusRegistry})
		http.Handle("/metrics", prometheusHandler)
		ctrl.Log.Info("Prometheus handler for /metrics endpoint registered.")
		// go func() {
		// 	err := http.ListenAndServe(":8080", nil)

		// 	if err != nil {
		// 		ctrl.Log.Error(err, "PANIC [InitializeMetricsExporter] ListenAndServe")
		// 		panic("PANIC [InitializeMetricsExporter]: ListenAndServe: " + err.Error())
		// 	}
		// }()
	} else {
		ctrl.Log.Info("Prometheus registry already initialized, skipping initialization.")
	}
}
func RegisterMetrics() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(instasliceMetrics.GpuSliceTotal, instasliceMetrics.PendingSliceRequests, instasliceMetrics.compatibleProfiles)
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateGpuSliceMetrics(nodeName, gpuID, namespace, podname, profile string, usedSlots, freeSlots uint32) error {
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName,
		gpuID,
		namespace,
		podname,
		profile,
		"used").Set(float64(usedSlots))
	instasliceMetrics.GpuSliceTotal.WithLabelValues(
		nodeName,
		gpuID,
		namespace,
		podname,
		profile,
		"free").Set(float64(freeSlots))
	// log check
	// ctrl.Log.Info("GpuSliceTotal metric updated",
	// 	"node", nodeName, "gpuID", gpuID, "used", usedSlots,
	// 	"value", instasliceMetrics.GpuSliceTotal.WithLabelValues(nodeName, gpuID, namespace, podname, profile, "used").Desc())
	ctrl.Log.Info(fmt.Sprintf("[UpdateGpuSliceMetrics] Updated GPU Slices: %d used slot/s, %d freeslot for node -> %v, slice allocated GPUID -> %v, deployed pod -> %v, namespace -> %v, allocated profile -> %v.", usedSlots, freeSlots, nodeName, gpuID, podname, namespace, profile)) // trace
	return nil
}

// UpdatePendingSliceRequests updates the metric for pending GPU slice requests
func (r *InstasliceReconciler) UpdatePendingSliceRequests(count uint32) error {
	instasliceMetrics.PendingSliceRequests.Set(float64(count))
	ctrl.Log.Info(fmt.Sprintf("[UpdatePendingSliceRequests] Updating Pending Slice Requests %d", count)) // trace
	return nil
}

// UpdateCompatibleProfilesMetrics updates metrics based on remaining GPU slices and calculates compatible profiles dynamically
func (r *InstasliceReconciler) UpdateCompatibleProfilesMetrics(instasliceObj inferencev1alpha1.Instaslice, totalSlices, usedSlices uint32) error {
	remaining := totalSlices - usedSlices
	ctrl.Log.Info("Calculated remaining GPU slices", "totalSlices", totalSlices, "usedSlices", usedSlices, "remainingSlices", remaining)

	// Reset compatible profiles
	instasliceMetrics.compatibleProfiles.Reset()
	ctrl.Log.Info("Reset compatible profiles metric")
	// Initialize counter for incremental values
	counter := 1

	// Parse the MIG placements for profiles and their sizes
	for _, migPlacement := range instasliceObj.Spec.Migplacement {
		profileName := migPlacement.Profile

		// Check the first size value from the placements for this profile
		if len(migPlacement.Placements) > 0 {
			size := uint32(migPlacement.Placements[0].Size)

			// Check if the profile is compatible with the remaining slices
			if size <= remaining {
				instasliceMetrics.compatibleProfiles.WithLabelValues(profileName, fmt.Sprintf("%d", remaining)).Set(float64(counter)) // Indicate compatibility
				ctrl.Log.Info("UpdateCompatiableProfilesMetrics] Added compatible profile", "profile", profileName, "size", size, "remainingSlices", remaining)
				// Increment the counter for the next profile
				counter++
			}
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
