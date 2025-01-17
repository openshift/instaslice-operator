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
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"github.com/openshift/instaslice-operator/internal/controller"
	"github.com/openshift/instaslice-operator/internal/controller/config"
)

func TestDeleteConfigMap(t *testing.T) {
	// Set up the scheme for the client
	s := scheme.Scheme
	_ = v1.AddToScheme(s)

	// Create a fake client with a ConfigMap already created in the cluster
	existingConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: "default",
		},
	}

	// Use the fake client
	client := fake.NewClientBuilder().WithScheme(s).WithObjects(existingConfigMap).Build()

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client: client,
	}

	// Create a background context
	ctx := context.TODO()

	// Test Case 1: Delete ConfigMap successfully
	err := reconciler.deleteConfigMap(ctx, "test-configmap", "default")
	assert.NoError(t, err, "expected no error when deleting configmap")

	// Test that the ConfigMap no longer exists
	cm := &v1.ConfigMap{}
	err = client.Get(ctx, types.NamespacedName{Name: "test-configmap", Namespace: "default"}, cm)
	assert.True(t, errors.IsNotFound(err), "expected configmap to be deleted")

	// Test Case 2: ConfigMap not found
	err = reconciler.deleteConfigMap(ctx, "non-existent-configmap", "default")
	assert.NoError(t, err, "expected no error when configmap is not found")
}

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

	config := config.ConfigFromEnvironment()

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client:   client,
		NodeName: nodeName,
		Config:   config,
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

	config := config.ConfigFromEnvironment()

	// Create the reconciler with the fake client
	reconciler := &InstaSliceDaemonsetReconciler{
		Client:   client,
		NodeName: nodeName,
		Config:   config,
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
				PodUUID:            podUUID,
				Allocationstatus:   status,
				Nodename:           name,
				Resourceidentifier: name,
			},
		},
	}
	instaslice.Spec = spec
	return instaslice
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

func TestInstaSliceDaemonsetReconciler_checkConfigMapExists(t *testing.T) {
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
	configMap := &v1.ConfigMap{}
	configMap.Name = nodeName
	configMap.Namespace = controller.InstaSliceOperatorNamespace
	// create a config map
	assert.NoError(t, reconciler.Create(ctx, configMap))
	exists, err := reconciler.checkConfigMapExists(ctx, nodeName, controller.InstaSliceOperatorNamespace)
	assert.NoError(t, err)
	assert.True(t, exists)
	// check for non-existent config map
	exists, err = reconciler.checkConfigMapExists(ctx, "test-configmap", controller.InstaSliceOperatorNamespace)
	assert.NoError(t, err)
	assert.True(t, !exists)
}

func TestCalculateTotalMemoryGB(t *testing.T) {
	type args struct {
		isEmulated  bool
		gpuInfoList map[string]string
	}
	tests := []struct {
		name    string
		args    args
		want    float64
		wantErr assert.ErrorAssertionFunc
	}{
		{"test-case-emulated-mode", args{true, map[string]string{"gpu-1": "1g.5GB", "gpu-2": "2g.10GB"}}, 15, assert.NoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CalculateTotalMemoryGB(tt.args.isEmulated, tt.args.gpuInfoList)
			if !tt.wantErr(t, err, fmt.Sprintf("CalculateTotalMemoryGB(%v, %v)", tt.args.isEmulated, tt.args.gpuInfoList)) {
				return
			}
			assert.Equalf(t, tt.want, got, "CalculateTotalMemoryGB(%v, %v)", tt.args.isEmulated, tt.args.gpuInfoList)
		})
	}
}
