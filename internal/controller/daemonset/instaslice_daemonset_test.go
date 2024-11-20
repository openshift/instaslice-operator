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
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
