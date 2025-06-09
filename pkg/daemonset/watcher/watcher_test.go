package watcher

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	deviceplugins "github.com/openshift/instaslice-operator/pkg/daemonset/deviceplugins"
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
	cache := NewCDICache()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cdi.Configure(cdi.WithSpecDirs(dir)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	if err := SetupCDIDeletionWatcher(ctx, dir, cache); err != nil {
		t.Fatalf("failed to setup watcher: %v", err)
	}

	path, _, err := deviceplugins.WriteCDISpecForResource("vendor/class", "test", nil)
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
				base, _, _, _ := deviceplugins.BuildCDIDevices("vendor/class", "class", "test", nil)
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
				base, _, _, _ := deviceplugins.BuildCDIDevices("vendor/class", "class", "test", nil)
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
