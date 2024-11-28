/*
Copyright 2024.

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
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//+kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=ignore,sideEffects=None,groups="",resources=pods,verbs=create,versions=v1,name=instaslice.redhat.com,admissionReviewVersions=v1

type PodAnnotator struct {
	Client  client.Client
	Decoder admission.Decoder
}

func (a *PodAnnotator) Handle(ctx context.Context, req admission.Request) admission.Response {
	// Decode the incoming pod object
	pod := &v1.Pod{}
	err := a.Decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(400, fmt.Errorf("could not decode pod: %v", err))
	}

	if !hasMIGResource(pod) {
		return admission.Allowed("No nvidia.com/mig-* resource found, skipping mutation.")
	}

	performQuotaArithmetic(pod, req)

	// Add extended resource to resource limits
	if pod.Spec.Containers[0].Resources.Limits == nil {
		pod.Spec.Containers[0].Resources.Limits = make(v1.ResourceList)
	}

	limits := pod.Spec.Containers[0].Resources.Limits
	for resourceName, quantity := range limits {
		if strings.HasPrefix(string(resourceName), "nvidia.com/mig-") {
			newResourceName := strings.Replace(string(resourceName), "nvidia.com", "instaslice.redhat.com", 1)
			delete(pod.Spec.Containers[0].Resources.Limits, resourceName)
			limits[v1.ResourceName(newResourceName)] = quantity
		}
	}

	if pod.Spec.Containers[0].Resources.Requests == nil {
		pod.Spec.Containers[0].Resources.Requests = make(v1.ResourceList)
	}

	// Transform resource requests from nvidia.com/mig-* to instaslice.redhat.com/mig-*
	requests := pod.Spec.Containers[0].Resources.Requests
	for resourceName, quantity := range requests {
		if strings.HasPrefix(string(resourceName), "nvidia.com/mig-") {
			newResourceName := strings.Replace(string(resourceName), "nvidia.com", "instaslice.redhat.com", 1)
			delete(requests, resourceName)
			requests[v1.ResourceName(newResourceName)] = quantity
		}
	}

	// Add scheduling
	schedulingGateName := GateName
	found := false
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == schedulingGateName {
			found = true
			break
		}
	}
	if !found {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, v1.PodSchedulingGate{Name: schedulingGateName})
	}

	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, []v1.EnvVar{
		{
			Name: "CUDA_VISIBLE_DEVICES",
			ValueFrom: &v1.EnvVarSource{
				FieldRef: &v1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.annotations['instaslice.nvidia.device']",
				},
			},
		},
		{
			Name: "NVIDIA_VISIBLE_DEVICES",
			ValueFrom: &v1.EnvVarSource{
				FieldRef: &v1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.annotations['instaslice.nvidia.device']",
				},
			},
		},
	}...)

	// Marshal the updated pod object back to JSON
	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(500, fmt.Errorf("could not marshal pod: %v", err))
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// hasMIGResource checks if a pod has resource requests or limits with a key that matches `nvidia.com/mig-*`
func hasMIGResource(pod *v1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		// Check resource limits
		for resourceName := range container.Resources.Limits {
			if strings.HasPrefix(string(resourceName), NvidiaMIGPrefix) {
				return true
			}
		}
		// Check resource requests
		for resourceName := range container.Resources.Requests {
			if strings.HasPrefix(string(resourceName), NvidiaMIGPrefix) {
				return true
			}
		}
	}
	return false
}

func performQuotaArithmetic(pod *v1.Pod, req admission.Request) admission.Response {
	// assumption is that workloads will have 1 container where
	// MIG is requested.
	// TODO instead of only iterating over regular containers,
	// we should also consider other types of containers (such as init containers) in future
	for _, container := range pod.Spec.Containers {
		// dont bother checking requests section. Nvidia supports only limits
		// if requests is added by user, it should be equal to limits.
		for resourceName, quantity := range container.Resources.Limits {
			resourceParts := strings.Split(strings.TrimPrefix(string(resourceName), NvidiaMIGPrefix), ".")

			if len(resourceParts) == 2 {
				//gpuPart := resourceParts[0]
				memoryPart := resourceParts[1]
				memoryValue, err := strconv.Atoi(strings.TrimSuffix(memoryPart, "gb"))
				if err != nil {
					return admission.Errored(http.StatusInternalServerError, fmt.Errorf("failed to parse memory value: %v", err))
				}
				acceleratorMemory := memoryValue * int(quantity.Value())
				// assume 1 container workload
				// Convert the string to ResourceName
				resourceName := v1.ResourceName(QuotaResourceName)
				pod.Spec.Containers[0].Resources.Limits[resourceName] = resource.MustParse(fmt.Sprintf("%dGi", acceleratorMemory))
			}
		}
	}
	// Return the modified pod spec
	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}
