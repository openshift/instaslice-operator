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
	"os"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock/dgxa100"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	runtimefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCleanUp(t *testing.T) {
	// Set up the mock server
	server := dgxa100.New()

	// Mock the NVML functions
	nvml.Init = func() nvml.Return {
		return nvml.SUCCESS
	}
	nvml.Shutdown = func() nvml.Return {
		return nvml.SUCCESS
	}
	nvml.DeviceGetHandleByUUID = func(uuid string) (nvml.Device, nvml.Return) {
		for _, dev := range server.Devices {
			device := dev.(*dgxa100.Device)
			if device.UUID == uuid {
				return device, nvml.SUCCESS
			}
		}
		return nil, nvml.ERROR_NOT_FOUND
	}

	// Create a fake Kubernetes client
	s := scheme.Scheme
	_ = inferencev1alpha1.AddToScheme(s)
	fakeClient := runtimefake.NewClientBuilder().WithScheme(s).Build()

	// Create a fake kubernetes clientset

	//fakeKubeClient := fake.NewSimpleClientset()

	// Create an InstaSliceDaemonsetReconciler
	reconciler := &InstaSliceDaemonsetReconciler{
		Client: fakeClient,
		Scheme: s,
	}
	// Create a fake Instaslice resource
	instaslice := &inferencev1alpha1.Instaslice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Spec: inferencev1alpha1.InstasliceSpec{
			Prepared: map[string]inferencev1alpha1.PreparedDetails{
				"mig-uuid-1": {
					PodUUID:  "pod-uid-1",
					Parent:   "GPU-1",
					Giinfoid: 1,
					Ciinfoid: 1,
				},
			},
			Allocations: map[string]inferencev1alpha1.AllocationDetails{
				"allocation-1": {
					PodUUID:   "pod-uid-1",
					PodName:   "pod-name-1",
					Namespace: "default",
				},
			},
		},
	}
	fakeClient.Create(context.Background(), instaslice)

	// Set the NODE_NAME environment variable
	os.Setenv("NODE_NAME", "node-1")
	defer os.Unsetenv("NODE_NAME")

	// Create a fake Pod resource
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "pod-uid-1",
			Name:      "pod-name-1",
			Namespace: "default",
		},
	}

	// Call the cleanUp function
	reconciler.cleanUp(context.Background(), string(pod.UID))

	// Verify the Instaslice resource was updated
	var updatedInstaslice inferencev1alpha1.Instaslice
	err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &updatedInstaslice)
	assert.NoError(t, err)
	assert.Empty(t, updatedInstaslice.Spec.Prepared)
	assert.Empty(t, updatedInstaslice.Spec.Allocations)
}
