package deviceplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"tags.cncf.io/container-device-interface/pkg/cdi"
	"tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	"os"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	fakeclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned/fake"
	utils "github.com/openshift/instaslice-operator/test/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

	specPath, _, err := WriteCDISpecForResource("vendor/class", "id1", nil, "")
	if err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}

	mgr := NewManager("vendor/class", instav1.DiscoveredNodeResources{})
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

	mgr := NewManager("vendor/class", instav1.DiscoveredNodeResources{})
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

			mgr := NewManager("mig.das.com/1g.5gb", instav1.DiscoveredNodeResources{})
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
				if !strings.HasPrefix(cr.CDIDevices[0].Name, "mig.das.com/c1g.5gb=") {
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

func TestAllocateMultipleDevices(t *testing.T) {
	nodeName := "test-node"
	os.Setenv("NODE_NAME", nodeName)
	defer os.Unsetenv("NODE_NAME")

	inst := utils.GenerateFakeCapacity(nodeName)
	client := fakeclient.NewSimpleClientset(inst)

	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	mgr := NewManager("mig.das.com/1g.5gb", instav1.DiscoveredNodeResources{})
	srv := &Server{
		Manager:          mgr,
		SocketPath:       filepath.Join(dir, "dp.sock"),
		InstasliceClient: client,
		NodeName:         nodeName,
		EmulatedMode:     instav1.EmulatedModeEnabled,
	}

	req := &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{
		{DevicesIDs: []string{"id1", "id2"}},
		{DevicesIDs: []string{"id3", "id4"}},
	}}

	resp, err := srv.Allocate(context.Background(), req)
	if err != nil {
		t.Fatalf("Allocate failed: %v", err)
	}

	if len(resp.ContainerResponses) != len(req.ContainerRequests) {
		t.Fatalf("expected %d responses, got %d", len(req.ContainerRequests), len(resp.ContainerResponses))
	}
	for i, cr := range resp.ContainerResponses {
		wantIDs := req.ContainerRequests[i].GetDevicesIDs()
		if len(cr.CDIDevices) != len(wantIDs) {
			t.Fatalf("response %d expected %d CDI devices, got %d", i, len(wantIDs), len(cr.CDIDevices))
		}
		for j, id := range wantIDs {
			got := cr.CDIDevices[j].Name
			want := fmt.Sprintf("mig.das.com/c1g.5gb=%s", id)
			if got != want {
				t.Fatalf("response %d device %d expected %q, got %q", i, j, want, got)
			}
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
	if count != 4 {
		t.Fatalf("expected %d spec files, got %d", 4, count)
	}

	sanitized := "1g.5gb"
	if err := parser.ValidateClassName(sanitized); err != nil {
		sanitized = "c" + sanitized
	}

	for _, ids := range [][]string{{"id1", "id2"}, {"id3", "id4"}} {
		for _, id := range ids {
			path := filepath.Join(dir, fmt.Sprintf("%s_%s.cdi.json", sanitized, id))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("expected spec file %s: %v", path, err)
			}
			var spec cdispec.Spec
			if err := json.Unmarshal(data, &spec); err != nil {
				t.Fatalf("failed to parse spec %s: %v", path, err)
			}
			if len(spec.Devices) != 1 || spec.Devices[0].Name != id {
				t.Fatalf("spec %s does not contain expected device %s", path, id)
			}
		}
	}
}

func TestGetAllocationsByNodeGPU(t *testing.T) {
	nodeName := "node1"
	resource := "mig.das.com/1g.5gb"

	allocationIndexer = cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"node-MigProfile": func(obj interface{}) ([]string, error) {
			a := obj.(*instav1.AllocationClaim)
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s/%s", spec.Nodename, spec.Profile)
			return []string{key}, nil
		},
	})

	specObj := instav1.AllocationClaimSpec{Profile: "1g.5gb", Nodename: types.NodeName(nodeName)}
	raw, _ := json.Marshal(&specObj)
	alloc := &instav1.AllocationClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a1"},
		Spec:       runtime.RawExtension{Raw: raw},
		Status:     instav1.AllocationClaimStatus{State: instav1.AllocationClaimStatusCreated},
	}
	_ = allocationIndexer.Add(alloc)

	srv := &Server{}
	res, err := srv.getAllocationsByNodeGPU(context.Background(), nodeName, resource, 1)
	if err != nil {
		t.Fatalf("getAllocationsByNodeGPU returned error: %v", err)
	}
	if len(res) != 1 || res[0] != alloc {
		t.Fatalf("expected returned allocation")
	}
	if alloc.Status.State != instav1.AllocationClaimStatusProcessing {
		t.Fatalf("expected allocation status updated, got %s", alloc.Status.State)
	}
}

func TestWriteCDISpecForResourceEnv(t *testing.T) {
	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir), cdi.WithAutoRefresh(false)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	path, _, err := WriteCDISpecForResource("vendor/class", "id-env", nil, "NVIDIA_VISIBLE_DEVICES=foo")
	if err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read spec: %v", err)
	}
	var spec cdispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("failed to unmarshal spec: %v", err)
	}
	env := spec.Devices[0].ContainerEdits.Env
	if len(env) != 2 {
		t.Fatalf("unexpected env %v", env)
	}
	if env[0] != "NVIDIA_VISIBLE_DEVICES=foo" || env[1] != "CUDA_VISIBLE_DEVICES=foo" {
		t.Fatalf("unexpected env %v", env)
	}
}

func TestWriteCDISpecForResourceWait(t *testing.T) {
	dir := t.TempDir()
	if err := cdi.Configure(cdi.WithSpecDirs(dir), cdi.WithAutoRefresh(false)); err != nil {
		t.Fatalf("failed to configure cdi: %v", err)
	}

	path, _, err := WriteCDISpecForResource("vendor/class", "id", nil, "")
	if err != nil {
		t.Fatalf("failed to write spec: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_, _, _ = WriteCDISpecForResource("vendor/class", "id", nil, "")
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("WriteCDISpecForResource returned before spec removed")
	case <-time.After(200 * time.Millisecond):
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove spec: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("WriteCDISpecForResource did not finish after spec removal")
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected spec recreated: %v", err)
	}
}
