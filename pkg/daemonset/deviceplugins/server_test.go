package deviceplugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	utils "github.com/openshift/instaslice-operator/test/utils"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"tags.cncf.io/container-device-interface/pkg/cdi"
)

func TestAllocateEmulated(t *testing.T) {
	nodeName := "test-node"
	os.Setenv("NODE_NAME", nodeName)
	defer os.Unsetenv("NODE_NAME")

	inst := utils.GenerateFakeCapacity(nodeName)
	client := fakeclient.NewSimpleClientset(inst)

	cases := []struct {
		name       string
		containers int
	}{
		{"single", 1},
		{"multiple", 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := cdi.Configure(cdi.WithSpecDirs(dir)); err != nil {
				t.Fatalf("failed to configure cdi: %v", err)
			}

			mgr := NewManager("instaslice.com/mig-1g.5gb")
			srv := &Server{
				Manager:          mgr,
				SocketPath:       filepath.Join(dir, "dp.sock"),
				InstasliceClient: client,
				NodeName:         nodeName,
				EmulatedMode:     instav1.EmulatedModeEnabled,
			}

			req := &pluginapi.AllocateRequest{ContainerRequests: make([]*pluginapi.ContainerAllocateRequest, tc.containers)}
			for i := 0; i < tc.containers; i++ {
				req.ContainerRequests[i] = &pluginapi.ContainerAllocateRequest{}
			}

			resp, err := srv.Allocate(context.Background(), req)
			if err != nil {
				t.Fatalf("Allocate failed: %v", err)
			}
			if len(resp.ContainerResponses) != tc.containers {
				t.Fatalf("expected %d responses, got %d", tc.containers, len(resp.ContainerResponses))
			}
			for i, cr := range resp.ContainerResponses {
				if len(cr.CDIDevices) != 1 {
					t.Fatalf("response %d expected 1 CDI device, got %d", i, len(cr.CDIDevices))
				}
				want := "instaslice.com/mig-1g.5gb=dev0"
				if cr.CDIDevices[0].Name != want {
					t.Fatalf("response %d unexpected CDI device %q", i, cr.CDIDevices[0].Name)
				}
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("failed to read dir: %v", err)
			}
			count := 0
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".json" {
					count++
				}
			}
			if count != tc.containers {
				t.Fatalf("expected %d spec files, got %d", tc.containers, count)
			}
		})
	}
}
