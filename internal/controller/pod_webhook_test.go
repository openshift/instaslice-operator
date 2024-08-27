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
	"testing"

	jsonpatch "github.com/evanphx/json-patch"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestHandle(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	annotator := &PodAnnotator{
		Client:  client,
		Decoder: admission.NewDecoder(scheme),
	}

	tests := []struct {
		name          string
		pod           *v1.Pod
		expectMut     bool
		expectedLimit string
	}{
		{
			name: "Pod without nvidia.com/mig-* resource",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod-without-mig-resource",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"cpu": resource.MustParse("100m"),
								},
							},
						},
					},
				},
			},
			expectMut:     false,
			expectedLimit: "",
		},
		{
			name: "Pod with nvidia.com/mig-1g.5gb resource",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod-with-mig-resource",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			expectMut:     true,
			expectedLimit: "5Gi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			rawPod, _ := json.Marshal(tt.pod)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Object: runtime.RawExtension{
						Raw: rawPod,
					},
				},
			}

			resp := annotator.Handle(context.TODO(), req)

			if tt.expectMut {
				g.Expect(resp.Allowed).To(BeTrue(), "Expected mutation but none occurred")
				g.Expect(resp.Patches).NotTo(BeEmpty(), "Expected patches but none were found")

				patchBytes, err := json.Marshal(resp.Patches)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to marshal patches")

				originalPodBytes, err := json.Marshal(tt.pod)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to marshal original pod")

				patch, err := jsonpatch.DecodePatch(patchBytes)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to decode patch")

				patchedPodBytes, err := patch.Apply(originalPodBytes)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to apply patches")

				modifiedPod := &v1.Pod{}
				g.Expect(json.Unmarshal(patchedPodBytes, modifiedPod)).To(Succeed(), "Failed to unmarshal patched pod")

				actualMemory, found := modifiedPod.Spec.Containers[0].Resources.Limits["nvidia.com/accelerator-memory"]
				g.Expect(found).To(BeTrue(), "nvidia.com/accelerator-memory limit not found in the modified pod")
				expectedMemory := resource.MustParse(tt.expectedLimit)
				g.Expect(actualMemory.Cmp(expectedMemory)).To(Equal(0), "Expected nvidia.com/accelerator-memory to be %s", tt.expectedLimit)
			} else {
				g.Expect(resp.Allowed).To(BeTrue(), "Expected request to be allowed without mutation")
				g.Expect(resp.Patches).To(BeEmpty(), "Expected no patches but found some")
			}
		})
	}
}
