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

package daemonset

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller"
)

func TestInstaSliceDaemonsetReconciler_Reconcile_Deleting_Alloc_Status(t *testing.T) {
	// Set up the scheme for the client
	s := scheme.Scheme
	_ = v1.AddToScheme(s)
	_ = inferencev1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)

	// Use the fake client
	client := fake.NewClientBuilder().WithScheme(s).Build()
	const (
		nodeName = "test-node"
		podUUID  = "test-pod-uuid"
	)
	// Set NODE_NAME and EMULATOR_MODE env variables
	assert.NoError(t, os.Setenv("NODE_NAME", nodeName))
	assert.NoError(t, os.Setenv("EMULATOR_MODE", controller.EmulatorModeTrue))

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client:   client,
		NodeName: nodeName,
	}

	// Create a background context
	ctx := context.Background()

	// Create an instaslice object
	instaslice := newInstaslice(nodeName, podUUID, inferencev1alpha1.AllocationStatusDeleting)
	typeNamespacedName := types.NamespacedName{
		Name:      nodeName,
		Namespace: controller.InstaSliceOperatorNamespace,
	}
	req := ctrl.Request{
		NamespacedName: typeNamespacedName,
	}

	// Testcase 1: reconcile on an unknown instaslice object's name
	req.Name = "unknown-node"
	result, err := reconciler.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, result, ctrl.Result{})

	// Testcase 2: reconcile when an Instaslice object is not present
	req.Name = nodeName
	result, err = reconciler.Reconcile(ctx, req)
	assert.Error(t, err)
	assert.Equal(t, result, ctrl.Result{})

	// Testcase 3: reconcile for an AllocationStatusDeleting
	assert.NoError(t, reconciler.Client.Create(ctx, instaslice))
	result, err = reconciler.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, result, ctrl.Result{})
}

func TestInstaSliceDaemonsetReconciler_Reconcile_Creating_Alloc_Status(t *testing.T) {
	// Set up the scheme for the client
	s := scheme.Scheme
	_ = v1.AddToScheme(s)
	_ = inferencev1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	const (
		nodeName = "test-node"
		podUUID  = "test-pod-uuid"
	)
	// Use the fake client
	client := fake.NewClientBuilder().WithScheme(s).Build()

	// Set NODE_NAME and EMULATOR_MODE env variables
	assert.NoError(t, os.Setenv("NODE_NAME", nodeName))
	assert.NoError(t, os.Setenv("EMULATOR_MODE", controller.EmulatorModeTrue))

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client:   client,
		NodeName: nodeName,
	}

	// Create a background context
	ctx := context.Background()

	// Create an instaslice object
	instaslice := newInstaslice(nodeName, podUUID, inferencev1alpha1.AllocationStatusCreating)
	typeNamespacedName := types.NamespacedName{
		Name:      nodeName,
		Namespace: controller.InstaSliceOperatorNamespace,
	}
	req := ctrl.Request{
		NamespacedName: typeNamespacedName,
	}

	// Testcase 4: reconcile for an AllocationStatusDeleting
	assert.NoError(t, reconciler.Client.Create(ctx, instaslice))
	result, err := reconciler.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, result, ctrl.Result{})
}

func newInstaslice(name, podUUID string, status inferencev1alpha1.AllocationStatus) *inferencev1alpha1.Instaslice {
	// Create an instaslice object
	instaslice := new(inferencev1alpha1.Instaslice)
	instaslice.Name = name
	instaslice.Namespace = controller.InstaSliceOperatorNamespace
	spec := inferencev1alpha1.InstasliceSpec{
		Allocations: map[string]inferencev1alpha1.AllocationDetails{
			podUUID: {
				PodUUID:          podUUID,
				Allocationstatus: status,
				Nodename:         name,
			},
		},
	}
	instaslice.Spec = spec
	return instaslice
}

func Test_calculateTotalMemoryGB(t *testing.T) {
	type args struct {
		gpuInfoList map[string]string
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{"test-1", args{map[string]string{"gpu-1": "1g.5GB", "gpu-2": "2g.10GB"}}, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, calculateTotalMemoryGB(tt.args.gpuInfoList), "calculateTotalMemoryGB(%v)", tt.args.gpuInfoList)
		})
	}
}

func TestInstaSliceDaemonsetReconciler_addMigCapacityToNode(t *testing.T) {
	// Set up the scheme for the client
	s := scheme.Scheme
	_ = v1.AddToScheme(s)
	_ = inferencev1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)

	const (
		nodeName = "test-node"
		podUUID  = "test-pod-uuid"
	)

	// Use the fake client
	client := fake.NewClientBuilder().WithScheme(s).Build()

	// Set NODE_NAME and EMULATOR_MODE env variables
	assert.NoError(t, os.Setenv("NODE_NAME", nodeName))
	assert.NoError(t, os.Setenv("EMULATOR_MODE", controller.EmulatorModeTrue))

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client:   client,
		NodeName: nodeName,
	}

	// Create a background context
	ctx := context.Background()

	// create a fake node object
	node := &v1.Node{}
	node.Name = nodeName
	assert.NoError(t, client.Create(ctx, node))
	// Create an instaslice object
	instaslice := newInstaslice(nodeName, podUUID, inferencev1alpha1.AllocationStatusCreating)
	assert.NoError(t, reconciler.addMigCapacityToNode(ctx, instaslice))
}

func TestInstaSliceDaemonsetReconciler_classicalResourcesAndGPUMemOnNode(t *testing.T) {
	// Set up the scheme for the client
	s := scheme.Scheme
	_ = v1.AddToScheme(s)
	_ = inferencev1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)

	nodeName := "test-node"

	// Use the fake client
	client := fake.NewClientBuilder().WithScheme(s).Build()

	// Set NODE_NAME and EMULATOR_MODE env variables
	assert.NoError(t, os.Setenv("NODE_NAME", nodeName))
	assert.NoError(t, os.Setenv("EMULATOR_MODE", controller.EmulatorModeTrue))

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client:   client,
		NodeName: nodeName,
	}
	// Create a background context
	ctx := context.Background()
	// create a fake node object
	node := &v1.Node{}
	node.Name = nodeName
	node.Status.Allocatable = v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse("10m"),
		v1.ResourceMemory: resource.MustParse("500Mi"),
	}

	assert.NoError(t, client.Create(ctx, node))
	cpu, mem, err := reconciler.classicalResourcesAndGPUMemOnNode(ctx, nodeName, "30")
	assert.Equal(t, int64(1), cpu)
	assert.Equal(t, int64(524288000), mem)
	assert.NoError(t, err)
}

func TestNewMigProfile(t *testing.T) {
	type args struct {
		giProfileID            int
		ciProfileID            int
		ciEngProfileID         int
		giSliceCount           uint32
		ciSliceCount           uint32
		migMemorySizeMB        uint64
		totalDeviceMemoryBytes uint64
	}
	tests := []struct {
		name string
		args args
		want *MigProfile
	}{
		{"test-case-1", args{
			giProfileID:            1,
			ciProfileID:            2,
			ciEngProfileID:         3,
			giSliceCount:           4,
			ciSliceCount:           5,
			migMemorySizeMB:        32,
			totalDeviceMemoryBytes: 64,
		}, &MigProfile{
			C:              5,
			G:              4,
			GB:             int(getMigMemorySizeInGB(64, 32)),
			GIProfileID:    1,
			CIProfileID:    2,
			CIEngProfileID: 3,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, NewMigProfile(tt.args.giProfileID, tt.args.ciProfileID, tt.args.ciEngProfileID, tt.args.giSliceCount, tt.args.ciSliceCount, tt.args.migMemorySizeMB, tt.args.totalDeviceMemoryBytes), "NewMigProfile(%v, %v, %v, %v, %v, %v, %v)", tt.args.giProfileID, tt.args.ciProfileID, tt.args.ciEngProfileID, tt.args.giSliceCount, tt.args.ciSliceCount, tt.args.migMemorySizeMB, tt.args.totalDeviceMemoryBytes)
		})
	}
}

func Test_getMigMemorySizeInGB(t *testing.T) {
	type args struct {
		totalDeviceMemory uint64
		migMemorySizeMB   uint64
	}
	tests := []struct {
		name string
		args args
		want uint64
	}{
		{"test-case", args{1, 1}, 0x100000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, getMigMemorySizeInGB(tt.args.totalDeviceMemory, tt.args.migMemorySizeMB), "getMigMemorySizeInGB(%v, %v)", tt.args.totalDeviceMemory, tt.args.migMemorySizeMB)
		})
	}
}

func TestMigProfile_String(t *testing.T) {
	type fields struct {
		C              int
		G              int
		GB             int
		GIProfileID    int
		CIProfileID    int
		CIEngProfileID int
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{"test-case-1", fields{1, 2, 3, 4, 5, 6}, "1c.2g.3gb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := MigProfile{
				C:              tt.fields.C,
				G:              tt.fields.G,
				GB:             tt.fields.GB,
				GIProfileID:    tt.fields.GIProfileID,
				CIProfileID:    tt.fields.CIProfileID,
				CIEngProfileID: tt.fields.CIEngProfileID,
			}
			assert.Equalf(t, tt.want, m.String(), "String()")
		})
	}
}

func TestMigProfile_Attributes(t *testing.T) {
	type fields struct {
		C              int
		G              int
		GB             int
		GIProfileID    int
		CIProfileID    int
		CIEngProfileID int
	}
	tests := []struct {
		name   string
		fields fields
		want   []string
	}{
		{"test-case-1", fields{1, 2, 3, 7, 5, 6}, []string{"me"}},
		{"test-case-2", fields{1, 2, 3, 4, 5, 6}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := MigProfile{
				C:              tt.fields.C,
				G:              tt.fields.G,
				GB:             tt.fields.GB,
				GIProfileID:    tt.fields.GIProfileID,
				CIProfileID:    tt.fields.CIProfileID,
				CIEngProfileID: tt.fields.CIEngProfileID,
			}
			assert.Equalf(t, tt.want, m.Attributes(), "Attributes()")
		})
	}
}
