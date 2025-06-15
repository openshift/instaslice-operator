package watcher

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	deviceplugins "github.com/openshift/instaslice-operator/pkg/daemonset/deviceplugins"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

func mustJSON(t *testing.T, spec *cdispec.Spec) []byte {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("failed to marshal spec: %v", err)
	}
	return data
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met")
}

func TestCDIWatcherLifecycle(t *testing.T) {
	dir := t.TempDir()
	cache := NewCDICache(nil)
	alloc := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Name: "alloc1", Namespace: "default"}}
	client := fakeclient.NewSimpleClientset(alloc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cdi.Configure(cdi.WithSpecDirs(dir)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	if err := SetupCDIDeletionWatcher(ctx, dir, cache, client); err != nil {
		t.Fatalf("failed to setup watcher: %v", err)
	}

	data, _ := json.Marshal(alloc)
	annotations := map[string]string{allocationAnnotationKey: string(data)}
	path, _, err := deviceplugins.WriteCDISpecForResource("vendor/class", "test", annotations, "")
	if err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}

	cases := []struct {
		name   string
		action func()
		check  func(t *testing.T)
	}{
		{
			name:   "initial spec",
			action: func() {},
			check: func(t *testing.T) {
				waitFor(t, func() bool {
					s, ok := cache.Get(path)
					return ok && len(s.Devices) == 1 && s.Devices[0].Name == "dev0"
				})
			},
		},
		{
			name: "add device",
			action: func() {
				base, _, _, _ := deviceplugins.BuildCDIDevices("vendor/class", "class", "test", annotations, "")
				d := base.Devices[0]
				d.Name = "dev1"
				base.Devices = append(base.Devices, d)
				if err := os.WriteFile(path, mustJSON(t, base), 0644); err != nil {
					t.Fatalf("failed to update spec: %v", err)
				}
			},
			check: func(t *testing.T) {
				waitFor(t, func() bool {
					s, ok := cache.Get(path)
					return ok && len(s.Devices) == 2 && s.Devices[1].Name == "dev1"
				})
			},
		},
		{
			name: "remove device",
			action: func() {
				base, _, _, _ := deviceplugins.BuildCDIDevices("vendor/class", "class", "test", annotations, "")
				if err := os.WriteFile(path, mustJSON(t, base), 0644); err != nil {
					t.Fatalf("failed to update spec: %v", err)
				}
			},
			check: func(t *testing.T) {
				waitFor(t, func() bool {
					s, ok := cache.Get(path)
					return ok && len(s.Devices) == 1 && s.Devices[0].Name == "dev0"
				})
			},
		},
		{
			name: "delete spec",
			action: func() {
				if err := os.Remove(path); err != nil {
					t.Fatalf("failed to remove spec: %v", err)
				}
			},
			check: func(t *testing.T) {
				waitFor(t, func() bool {
					_, ok := cache.Get(path)
					return !ok
				})
				_, err := client.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Get(ctx, alloc.Name, metav1.GetOptions{})
				if err == nil || !apierrors.IsNotFound(err) {
					t.Fatalf("expected allocation claim deleted")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.action()
			tc.check(t)
		})
	}
}
func TestHandleWriteEvent(t *testing.T) {
	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}
	cache := NewCDICache(nil)
	spec, _, path, _ := deviceplugins.BuildCDIDevices("vendor/class", "class", "id", nil, "")
	if err := os.WriteFile(path, mustJSON(t, spec), 0644); err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}
	handleWriteEvent(path, cache)
	s, ok := cache.Get(path)
	if !ok || len(s.Devices) != 1 || s.Devices[0].Name != "dev0" {
		t.Fatalf("spec not loaded")
	}
}

func TestProcessDeviceRemoval(t *testing.T) {
	alloc := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Name: "alloc", Namespace: "default"}}
	client := fakeclient.NewSimpleClientset(alloc)
	data, _ := json.Marshal(alloc)
	annotations := map[string]string{allocationAnnotationKey: string(data)}
	spec, _, _, _ := deviceplugins.BuildCDIDevices("vendor/class", "class", "id", annotations, "")
	dev := spec.Devices[0]
	ctx := context.Background()
	processDeviceRemoval(ctx, dev, "dummy", client)
	_, err := client.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Get(ctx, alloc.Name, metav1.GetOptions{})
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("allocation claim not deleted")
	}
}

func TestHandleRemoveEvent(t *testing.T) {
	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}
	cache := NewCDICache(nil)
	alloc := &instav1.AllocationClaim{ObjectMeta: metav1.ObjectMeta{Name: "alloc2", Namespace: "default"}}
	client := fakeclient.NewSimpleClientset(alloc)
	data, _ := json.Marshal(alloc)
	annotations := map[string]string{allocationAnnotationKey: string(data)}
	path, _, err := deviceplugins.WriteCDISpecForResource("vendor/class", "test", annotations, "")
	if err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}
	loadSpec(path, cache)
	handleRemoveEvent(context.Background(), path, cache, client)
	if _, ok := cache.Get(path); ok {
		t.Fatalf("spec not removed from cache")
	}
	_, err = client.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Get(context.Background(), alloc.Name, metav1.GetOptions{})
	if err == nil || !apierrors.IsNotFound(err) {
		t.Fatalf("allocation claim not deleted")
	}
}
