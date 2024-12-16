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

	"github.com/prometheus/client_golang/prometheus"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// Total number of GPU slices
	GpuSliceTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "instaslice_gpu_slices_total",
			Help: "Total number of GPU slices allocated per GPU slot.",
		},
		[]string{"node", "gpu_id", "slot_status"}, // Labels: node, GPU ID, slot status.
	)

	// Pending GPU slice requests
	PendingSliceRequests = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "instaslice_pending_gpu_slice_requests",
			Help: "Number of pending GPU slice requests.",
		},
	)

	// ConfigMap information
	ConfigMapInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "instaslice_configmap_info",
			Help: "Information about ConfigMaps related to InstaSlice.",
		},
		[]string{"name", "namespace", "key"}, // Labels for ConfigMap details.
	)
)

// RegisterMetrics registers all Prometheus metrics
func RegisterMetrics() {
	prometheus.MustRegister(GpuSliceTotal, PendingSliceRequests, ConfigMapInfo)
}

// UpdateGpuSliceMetrics updates GPU slice allocation metrics
func UpdateGpuSliceMetrics(nodeName, gpuID string, usedSlots, freeSlots int) {
	GpuSliceTotal.WithLabelValues(nodeName, gpuID, "used").Set(float64(usedSlots))
	GpuSliceTotal.WithLabelValues(nodeName, gpuID, "free").Set(float64(freeSlots))
}

// UpdatePendingSliceRequests updates the metric for pending GPU slice requests
func UpdatePendingSliceRequests(count int) {
	PendingSliceRequests.Set(float64(count))
}

// UpdateConfigMapMetrics updates metrics for relevant ConfigMaps
func UpdateConfigMapMetrics(name, namespace, key string) {
	ConfigMapInfo.WithLabelValues(name, namespace, key).Set(1)
}

// Helper function to get pending GPU requests
func getPendingGpuRequests(client client.Client) int {
	// Create an empty list to store Pods
	pods := &v1.PodList{}

	// Call the List method with a context and empty ListOptions
	err := client.List(context.TODO(), pods)
	if err != nil {
		// Log the error or handle it if needed
		return 0
	}

	// Count pending Pods that request GPU slices
	count := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodPending && isGpuRequested(pod) {
			count++
		}
	}
	return count
}

// Helper function to check if a Pod requests GPU slices
func isGpuRequested(pod v1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		if _, ok := container.Resources.Requests["nvidia.com/gpu"]; ok {
			return true
		}
	}
	return false
}

// Helper function to update ConfigMap metrics
func updateConfigMaps(client client.Client) {
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
			UpdateConfigMapMetrics(cm.Name, cm.Namespace, key)
		}
	}
}
