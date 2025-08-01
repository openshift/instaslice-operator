package webhook

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	envNvidia = "NVIDIA_VISIBLE_DEVICES"
	envCUDA   = "CUDA_VISIBLE_DEVICES"
	testName  = "test"
)

func TestMutatePodNvidiaResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: testName},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  testName,
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

	if len(mutated.Spec.Containers[0].Env) != 0 {
		t.Fatalf("env vars should not be added")
	}
}

func TestMutatePodEphemeralNvidiaResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ephem"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main"}},
			EphemeralContainers: []corev1.EphemeralContainer{
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{
						Name:  "debug",
						Image: "busybox",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("1"),
							},
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
	limits := mutated.Spec.EphemeralContainers[0].Resources.Limits
	if _, ok := limits[corev1.ResourceName("mig.das.com/1g.5gb")]; !ok {
		t.Fatalf("instaslice resource missing")
	}

	if len(mutated.Spec.EphemeralContainers[0].Env) != 0 {
		t.Fatalf("env vars should not be added")
	}
}

func TestMutatePodOverrideValues(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "override"},
		Spec: corev1.PodSpec{
			SchedulerName: "foo",
			Containers: []corev1.Container{
				{
					Name:  "c",
					Image: "busybox",
					Env: []corev1.EnvVar{
						{Name: envNvidia, Value: "0"},
						{Name: envCUDA, Value: "0"},
					},
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
		t.Fatalf("unmarshal mutated pod: %v", err)
	}

	if mutated.Spec.SchedulerName != secondaryScheduler {
		t.Fatalf("scheduler not overridden")
	}

	envs := mutated.Spec.Containers[0].Env
	if len(envs) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(envs))
	}
	for _, e := range envs {
		switch e.Name {
		case envNvidia:
			if e.Value != "0" {
				t.Fatalf("NVIDIA env not preserved")
			}
		case envCUDA:
			if e.Value != "0" {
				t.Fatalf("CUDA env not preserved")
			}
		default:
			t.Fatalf("unexpected env var %s", e.Name)
		}
	}
}

func TestMutatePodInstaResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: testName},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  testName,
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

	if len(mutated.Spec.Containers[0].Env) != 0 {
		t.Fatalf("env vars should not be added")
	}
}

func TestMutatePodNoResource(t *testing.T) {
	hook := &InstasliceWebhook{}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "none"},
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
	if len(mutated.Spec.Containers[0].Env) != 0 {
		t.Fatalf("env vars should not be added")
	}
}
