package webhook

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/openshift/instaslice-operator/pkg/constants"
)

// migProfileRegex is compiled once at package init for efficient reuse.
// Matches patterns like "1g.5gb", "2g.10gb", "1c.1g.5gb", etc.
var migProfileRegex = regexp.MustCompile(`(?:\d+c\.)?(\d+)g\.(\d+)gb`)

// extractGPUMemoryFromProfile extracts the GPU memory in GB from a MIG profile string.
// Uses the pre-compiled migProfileRegex for efficiency.
func extractGPUMemoryFromProfile(profile string) int64 {
	matches := migProfileRegex.FindStringSubmatch(profile)
	if len(matches) >= 3 {
		if memGB, err := strconv.ParseInt(matches[2], 10, 64); err == nil {
			return memGB
		}
	}
	return 0
}

// SerializeMIGProfiles converts a profile map to a deterministic annotation string.
// Format: "1g.5gb:1,2g.10gb:2"
func SerializeMIGProfiles(profiles map[string]int64) string {
	if len(profiles) == 0 {
		return ""
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(profiles))
	for k := range profiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(profiles))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, profiles[k]))
	}

	return strings.Join(parts, ",")
}

// TransformPodTemplateForDAS fully transforms a PodTemplateSpec for DAS scheduling.
// This is used by source resource webhooks (Job, RayJob, PyTorchJob, etc.) to transform
// resources BEFORE Kueue creates the Workload.
//
// It performs the following transformations:
// 1. Removes nvidia.com/mig-* resources from containers
// 2. Adds gpu.das.openshift.io/mem with total GPU memory
// 3. Sets schedulerName to das-scheduler
// 4. Sets runtimeClassName to nvidia-legacy
// 5. Adds das.openshift.io/mig-profiles annotation
//
// Returns (totalGPUMemoryGB, migProfiles, modified).
func TransformPodTemplateForDAS(template *corev1.PodTemplateSpec) (int64, map[string]int64, bool) {
	allProfiles := make(map[string]int64)
	totalMemGB := int64(0)
	modified := false

	// Process regular containers (run concurrently - sum their resources)
	for i := range template.Spec.Containers {
		memGB, profiles := transformContainerResourcesForDAS(&template.Spec.Containers[i])
		if memGB > 0 {
			modified = true
			totalMemGB += memGB
			for p, q := range profiles {
				allProfiles[p] += q
			}
		}
	}

	// Process init containers (run sequentially - take max)
	initMaxMemGB := int64(0)
	for i := range template.Spec.InitContainers {
		memGB, profiles := transformContainerResourcesForDAS(&template.Spec.InitContainers[i])
		if memGB > 0 {
			modified = true
			if memGB > initMaxMemGB {
				initMaxMemGB = memGB
			}
			for p, q := range profiles {
				if q > allProfiles[p] {
					allProfiles[p] = q
				}
			}
		}
	}

	// Pod's effective GPU memory = max(init_max, regular_sum)
	if initMaxMemGB > totalMemGB {
		totalMemGB = initMaxMemGB
	}

	if !modified {
		return 0, nil, false
	}

	// Set DAS scheduler
	template.Spec.SchedulerName = constants.DASSchedulerName

	// Set nvidia-legacy runtime for MIG workloads
	runtimeClass := constants.NvidiaLegacyRuntimeClass
	template.Spec.RuntimeClassName = &runtimeClass

	// Add MIG profiles annotation
	if template.Annotations == nil {
		template.Annotations = make(map[string]string)
	}
	template.Annotations[constants.MIGProfileAnnotation] = SerializeMIGProfiles(allProfiles)

	klog.InfoS("Transformed pod template for DAS",
		"totalMemoryGB", totalMemGB,
		"profiles", allProfiles,
		"scheduler", template.Spec.SchedulerName)

	return totalMemGB, allProfiles, true
}

// transformContainerResourcesForDAS transforms a single container's resources for DAS.
// Removes nvidia.com/mig-* resources and adds gpu.das.openshift.io/mem.
// Returns (gpuMemoryGB, profilesMap).
func transformContainerResourcesForDAS(container *corev1.Container) (int64, map[string]int64) {
	profiles := make(map[string]int64)
	memGB := int64(0)

	if container.Resources.Limits == nil {
		return 0, profiles
	}

	newLimits := corev1.ResourceList{}
	newRequests := corev1.ResourceList{}

	// Process limits - remove MIG resources, keep others
	for resourceName, qty := range container.Resources.Limits {
		key := string(resourceName)

		if strings.HasPrefix(key, constants.NVIDIAMIGResourcePrefix) {
			profile := strings.TrimPrefix(key, constants.NVIDIAMIGResourcePrefix)
			quantity := qty.Value()
			profiles[profile] = quantity

			profileMemGB := extractGPUMemoryFromProfile(profile)
			if profileMemGB > 0 {
				memGB += profileMemGB * quantity
			}

			klog.V(4).InfoS("Removing MIG resource, will use DAS memory resource",
				"container", container.Name, "resource", key, "profile", profile, "quantity", quantity)
			// Don't copy nvidia.com/mig-* resource
		} else {
			// Keep non-MIG resources
			newLimits[resourceName] = qty
		}
	}

	// Copy non-MIG requests
	for resourceName, qty := range container.Resources.Requests {
		key := string(resourceName)
		if !strings.HasPrefix(key, constants.NVIDIAMIGResourcePrefix) {
			newRequests[resourceName] = qty
		}
	}

	// Add GPU memory resource if we found MIG profiles
	if memGB > 0 {
		gpuMemResource := corev1.ResourceName(constants.GPUMemoryResource)
		gpuMemQuantity := resource.NewQuantity(memGB, resource.DecimalSI)
		newLimits[gpuMemResource] = *gpuMemQuantity
		newRequests[gpuMemResource] = *gpuMemQuantity

		klog.V(4).InfoS("Injected GPU memory into container",
			"container", container.Name, "memoryGB", memGB)
	}

	container.Resources.Limits = newLimits
	container.Resources.Requests = newRequests

	return memGB, profiles
}

// IsKueueManaged checks if a resource has the Kueue queue label.
func IsKueueManaged(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	_, exists := labels[constants.KueueQueueLabel]
	return exists
}

