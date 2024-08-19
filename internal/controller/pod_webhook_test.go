package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
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
		name      string
		pod       *v1.Pod
		expectMut bool
	}{
		{
			name: "Pod with nvidia.com/mig-* resource",
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
			expectMut: true,
		},
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
			expectMut: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawPod, _ := json.Marshal(tt.pod)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Object: runtime.RawExtension{
						Raw: rawPod,
					},
				},
			}

			resp := annotator.Handle(context.TODO(), req)

			// Check if mutation occurred based on the expected behavior
			if tt.expectMut {
				assert.True(t, resp.Allowed, "Expected mutation but none occurred")
				assert.NotEmpty(t, resp.Patches, "Expected patches but none were found")
			} else {
				assert.True(t, resp.Allowed, "Expected request to be allowed without mutation")
				assert.Empty(t, resp.Patches, "Expected no patches but found some")
			}
		})
	}
}
