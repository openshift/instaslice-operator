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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type InstasliceMetrics struct {
	GpuSliceTotal        *prometheus.GaugeVec
	PendingSliceRequests prometheus.Gauge
	ConfigMapInfo        *prometheus.GaugeVec
}

var baseProfileSliceMap = map[string]int{ // slice profiles and respective size
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
			[]string{"node", "slot_status"}), // Labels: node, GPU ID, slot status.
		// Pending GPU slice requests
		PendingSliceRequests: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "instaslice_pending_gpu_slice_requests",
			Help: "Number of pending GPU slice requests.",
		}),
		// ConfigMap information
		ConfigMapInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "instaslice_configmap_info",
			Help: "Information about ConfigMaps related to InstaSlice.",
		},
			[]string{"name", "namespace", "key"}), // Labels for ConfigMap details.
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
		prometheus.MustRegister(instasliceMetrics.GpuSliceTotal, instasliceMetrics.PendingSliceRequests, instasliceMetrics.ConfigMapInfo)
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

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func (r *InstasliceReconciler) UpdateGpuSliceMetrics(nodeName string, usedSlots, freeSlots uint32) error {
	instasliceMetrics.GpuSliceTotal.WithLabelValues(nodeName, "used").Set(float64(usedSlots))
	instasliceMetrics.GpuSliceTotal.WithLabelValues(nodeName, "free").Set(float64(freeSlots))
	ctrl.Log.Info("GpuSliceTotal metric updated",
		"node", nodeName, "used", usedSlots,
		"value", instasliceMetrics.GpuSliceTotal.WithLabelValues(nodeName, "used").Desc())
	ctrl.Log.Info(fmt.Sprintf("[UpdateGpuSliceMetrics] Updating GPU Slice %d used slot, %d freeslot for nodeName -> %v.", usedSlots, freeSlots, nodeName)) // trace
	return nil
}

// UpdatePendingSliceRequests updates the metric for pending GPU slice requests
func (r *InstasliceReconciler) UpdatePendingSliceRequests(count int) error {
	instasliceMetrics.PendingSliceRequests.Set(float64(count))
	ctrl.Log.Info(fmt.Sprintf("[UpdatePendingSliceRequests] Updating Pending Slice Requests %d", count)) // trace
	return nil
}

// UpdateConfigMapMetrics updates metrics for relevant ConfigMaps
func (r *InstasliceReconciler) UpdateConfigMapMetrics(name, namespace, key string) error {
	instasliceMetrics.ConfigMapInfo.WithLabelValues(name, namespace, key).Set(1)
	return nil
}

// generateProfileSliceMap generates the full map for both instaslice.redhat.com and nvidia.com
func generateProfileSliceMap() map[string]int {
	prefixes := []string{"instaslice.redhat.com/mig-", "nvidia.com/mig-"}
	fullMap := make(map[string]int)

	for profile, slices := range baseProfileSliceMap {
		for _, prefix := range prefixes {
			fullMap[prefix+profile] = slices
		}
	}

	return fullMap
}

// getPendingGpuRequests gets pending GPU requests
func (r *InstasliceReconciler) getPendingGpuRequests(ctx context.Context, client client.Client) (int, error) {
	pods := &v1.PodList{}

	// Fetch all pods in the cluster
	err := client.List(ctx, pods)
	if err != nil {
		return 0, fmt.Errorf("failed to list pods: %w", err)
	}

	// Count total GPU slice requests from pending pods
	pendingSlices := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodPending {
			for _, container := range pod.Spec.Containers {
				for resourceName, quantity := range container.Resources.Requests {
					if sliceCount, exists := profileSliceMap[resourceName.String()]; exists {
						// Multiply slice count by the requested quantity
						pendingSlices += sliceCount * int(quantity.Value())
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

// updateConfigMaps updates ConfigMap metrics
func (r *InstasliceReconciler) updateConfigMaps(client client.Client) {
	// Create an empty list to store ConfigMaps
	configMaps := &v1.ConfigMapList{}

	// Call the List method with a context and empty ListOptions
	err := client.List(context.TODO(), configMaps)
	if err != nil {
		// Log the error or handle it if needed
		return
	}

	// Update metrics for each ConfigMap
	for _, cm := range configMaps.Items {
		for key := range cm.Data {
			r.UpdateConfigMapMetrics(cm.Name, cm.Namespace, key)
		}
	}
}
