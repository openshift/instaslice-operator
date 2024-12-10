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
	instasliceQuotaResourceName := "instaslice.redhat.com/accelerator-memory-quota"
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	annotator := &PodAnnotator{
		Client:  client,
		Decoder: admission.NewDecoder(scheme),
	}

	type ExpectedMutation int

	const (
		None = iota
		Allowed
		Disallowed
	)

	tests := []struct {
		name          string
		pod           *v1.Pod
		expectMut     ExpectedMutation
		expectedLimit string
		migIndex      int
	}{
		{
			name:     "Pod without nvidia.com/mig-* resource",
			migIndex: 0,
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
			expectMut:     None,
			expectedLimit: "",
		},
		{
			name:     "Pod with nvidia.com/mig-1g.5gb resource",
			migIndex: 0,
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
			expectMut:     Allowed,
			expectedLimit: "5Gi",
		},
		{
			name:     "Pod with nvidia.com/mig-1g.5gb resource with multiple containers index 0",
			migIndex: 0,
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod-with-mig-resource",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "mig container",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
						},
						{
							Name: "extra-container",
						},
					},
				},
			},
			expectMut:     Allowed,
			expectedLimit: "5Gi",
		},
		{
			name:     "Pod with nvidia.com/mig-1g.5gb resource with multiple containers index 1",
			migIndex: 1,
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod-with-mig-resource",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "extra-container",
						},
						{
							Name: "mig container",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			expectMut:     Allowed,
			expectedLimit: "5Gi",
		},
		{
			name: "Pod with nvidia.com/mig-1g.5gb resource with multiple mig indexes (disallowed)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod-with-mig-resource",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "extra-container",
						},
						{
							Name: "mig container",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
						},
						{
							Name: "mig container 2 (this is invalid)",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"nvidia.com/mig-1g.5gb": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			expectMut: Disallowed,
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

			switch tt.expectMut {
			case None:
				g.Expect(resp.Allowed).To(BeTrue(), "Expected request to be allowed without mutation")
				g.Expect(resp.Patches).To(BeEmpty(), "Expected no patches but found some")
			case Allowed:
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

				actualMemory, found := modifiedPod.Spec.Containers[tt.migIndex].Resources.Limits[v1.ResourceName(instasliceQuotaResourceName)]
				g.Expect(found).To(BeTrue(), fmt.Sprintf("%s limit not found in the modified pod", instasliceQuotaResourceName))
				expectedMemory := resource.MustParse(tt.expectedLimit)
				g.Expect(actualMemory.Cmp(expectedMemory)).To(Equal(0), fmt.Sprintf("Expected %s to be %s", instasliceQuotaResourceName, tt.expectedLimit))
			case Disallowed:
				g.Expect(resp.Allowed).To(BeFalse(), "Expected request to be disallowed without mutation")
				g.Expect(resp.Patches).To(BeEmpty(), "Expected no patches but found some")
			default:
				t.Fatal("invalid expected case")
			}
		})
	}
}

func TestTransformResources(t *testing.T) {
	createResourceList := func(resources map[string]string) v1.ResourceList {
		resourceList := v1.ResourceList{}
		for name, value := range resources {
			resourceList[v1.ResourceName(name)] = resource.MustParse(value)
		}
		return resourceList
	}

	tests := []struct {
		name             string
		limits           map[string]string
		requests         map[string]string
		expectedLimits   map[string]string
		expectedRequests map[string]string
	}{
		{
			name: "Transform valid resources",
			limits: map[string]string{
				"nvidia.com/mig-1g": "1",
				"nvidia.com/mig-2g": "2",
			},
			requests: map[string]string{
				"nvidia.com/mig-1g": "1",
			},
			expectedLimits: map[string]string{
				"instaslice.redhat.com/mig-1g": "1",
				"instaslice.redhat.com/mig-2g": "2",
			},
			expectedRequests: map[string]string{
				"instaslice.redhat.com/mig-1g": "1",
			},
		},
		{
			name: "Do not transform unrelated resources (negative test)",
			limits: map[string]string{
				"other.com/mig-1g": "3",
			},
			requests: map[string]string{
				"unrelated.com/mig-1g": "4",
			},
			expectedLimits: map[string]string{
				"other.com/mig-1g": "3",
			},
			expectedRequests: map[string]string{
				"unrelated.com/mig-1g": "4",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := &v1.ResourceRequirements{
				Limits:   createResourceList(tt.limits),
				Requests: createResourceList(tt.requests),
			}
			transformResources(resources)
			compareResourceLists := func(actual, expected v1.ResourceList) {
				if len(actual) != len(expected) {
					t.Fatalf("expected %d resources, got %d", len(expected), len(actual))
				}
				for name, expectedQuantity := range expected {
					actualQuantity, exists := actual[name]
					if !exists {
						t.Errorf("expected resource %s not found", name)
					} else if actualQuantity.Cmp(expectedQuantity) != 0 {
						t.Errorf("expected resource %s to have quantity %s, got %s", name, expectedQuantity.String(), actualQuantity.String())
					}
				}
			}
			compareResourceLists(resources.Limits, createResourceList(tt.expectedLimits))
			compareResourceLists(resources.Requests, createResourceList(tt.expectedRequests))
		})
	}
}
