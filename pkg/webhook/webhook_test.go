package webhook

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestMutatePodNvidiaResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test",
					Image: "ubuntu:20.04",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	data, err := hook.mutatePod(pod)
	if err != nil {
		t.Fatalf("mutatePod returned error: %v", err)
	}

	mutated := &corev1.Pod{}
	if err := json.Unmarshal(data, mutated); err != nil {
		t.Fatalf("failed to unmarshal mutated pod: %v", err)
	}

	if mutated.Spec.SchedulerName != secondaryScheduler {
		t.Fatalf("expected scheduler %s, got %s", secondaryScheduler, mutated.Spec.SchedulerName)
	}

	limits := mutated.Spec.Containers[0].Resources.Limits
	if _, ok := limits[corev1.ResourceName("nvidia.com/mig-1g.5gb")]; ok {
		t.Fatalf("nvidia resource still present")
	}
	q, ok := limits[corev1.ResourceName("mig.das.com/1g.5gb")]
	if !ok || q.Value() != 1 {
		t.Fatalf("expected instaslice resource quantity 1")
	}
}

func TestMutatePodInstaResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test",
					Image: "ubuntu:20.04",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName("mig.das.com/1g.5gb"): resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	data, err := hook.mutatePod(pod)
	if err != nil {
		t.Fatalf("mutatePod returned error: %v", err)
	}
	mutated := &corev1.Pod{}
	if err := json.Unmarshal(data, mutated); err != nil {
		t.Fatalf("unmarshal mutated pod: %v", err)
	}
	if mutated.Spec.SchedulerName != secondaryScheduler {
		t.Fatalf("expected scheduler set")
	}
	if _, ok := mutated.Spec.Containers[0].Resources.Limits[corev1.ResourceName("mig.das.com/1g.5gb")]; !ok {
		t.Fatalf("instaslice resource missing")
	}
}

func TestMutatePodNoResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "t"}},
		},
	}

	data, err := hook.mutatePod(pod)
	if err != nil {
		t.Fatalf("mutatePod returned error: %v", err)
	}
	mutated := &corev1.Pod{}
	if err := json.Unmarshal(data, mutated); err != nil {
		t.Fatalf("unmarshal mutated pod: %v", err)
	}
	if mutated.Spec.SchedulerName != "" {
		t.Fatalf("expected scheduler not set")
	}
}
