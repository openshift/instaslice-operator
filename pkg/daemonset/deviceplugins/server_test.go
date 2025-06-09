package deviceplugins

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"tags.cncf.io/container-device-interface/pkg/cdi"

	"os"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	utils "github.com/openshift/instaslice-operator/test/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
)

// fakeListWatchServer implements pluginapi.DevicePlugin_ListAndWatchServer.
// It captures sent responses for inspection.
type fakeListWatchServer struct {
	ctx context.Context
	ch  chan *pluginapi.ListAndWatchResponse
}

func newFakeListWatchServer(ctx context.Context) *fakeListWatchServer {
	return &fakeListWatchServer{ctx: ctx, ch: make(chan *pluginapi.ListAndWatchResponse, 10)}
}

func (f *fakeListWatchServer) Send(resp *pluginapi.ListAndWatchResponse) error {
	f.ch <- resp
	return nil
}

func (f *fakeListWatchServer) Context() context.Context { return f.ctx }

func (f *fakeListWatchServer) SendMsg(m interface{}) error {
	if r, ok := m.(*pluginapi.ListAndWatchResponse); ok {
		f.ch <- r
	}
	return nil
}
func (f *fakeListWatchServer) RecvMsg(interface{}) error    { return nil }
func (f *fakeListWatchServer) SetHeader(metadata.MD) error  { return nil }
func (f *fakeListWatchServer) SendHeader(metadata.MD) error { return nil }
func (f *fakeListWatchServer) SetTrailer(metadata.MD)       {}

func TestListAndWatchInitialSpecs(t *testing.T) {
	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir), cdi.WithAutoRefresh(false)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	specPath, _, err := WriteCDISpecForResource("vendor/class", "id1", nil)
	if err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}

	mgr := NewManager("vendor/class")
	srv := &Server{Manager: mgr}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newFakeListWatchServer(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.ListAndWatch(&pluginapi.Empty{}, stream) }()

	var resp *pluginapi.ListAndWatchResponse
	select {
	case resp = <-stream.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial response")
	}

	expectedID := filepath.Base(specPath)
	if len(resp.Devices) != 1 || resp.Devices[0].ID != expectedID || resp.Devices[0].Health != pluginapi.Healthy {
		t.Fatalf("unexpected initial devices: %+v", resp.Devices)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ListAndWatch returned error: %v", err)
	}
}

func TestListAndWatchForwardsUpdates(t *testing.T) {
	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir), cdi.WithAutoRefresh(false)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	mgr := NewManager("vendor/class")
	srv := &Server{Manager: mgr}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeListWatchServer(ctx)

	done := make(chan error, 1)
	go func() { done <- srv.ListAndWatch(&pluginapi.Empty{}, stream) }()

	// Read initial (empty) response
	select {
	case <-stream.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial response")
	}

	update := []*pluginapi.Device{{ID: "foo", Health: pluginapi.Healthy}}
	mgr.updates <- update

	var resp *pluginapi.ListAndWatchResponse
	select {
	case resp = <-stream.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for update")
	}

	if !reflect.DeepEqual(resp.Devices, update) {
		t.Fatalf("unexpected update: %+v", resp.Devices)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ListAndWatch returned error: %v", err)
	}
}

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

func TestGetAllocationsByNodeGPU(t *testing.T) {
	nodeName := "node1"
	resource := "instaslice.com/mig-1g.5gb"

	allocationIndexer = cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"node-MigProfile": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.Profile)
			return []string{key}, nil
		},
	})

	alloc := &instav1.AllocationClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a1"},
		Spec:       instav1.AllocationClaimSpec{Profile: "1g.5gb", Nodename: types.NodeName(nodeName)},
	}
	_ = allocationIndexer.Add(alloc)

	srv := &Server{}
	res, err := srv.getAllocationsByNodeGPU(nodeName, resource, 1)
	if err != nil {
		t.Fatalf("getAllocationsByNodeGPU returned error: %v", err)
	}
	if len(res) != 1 || res[0] != alloc {
		t.Fatalf("expected returned allocation")
	}
}
