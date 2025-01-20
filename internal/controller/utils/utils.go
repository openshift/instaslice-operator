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

package utils

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
)

const InstaSliceOperatorNamespace = "instaslice-system"

func UpdateInstasliceAllocations(ctx context.Context, kubeClient client.Client, name, podUUID string, allocation inferencev1alpha1.AllocationDetails) error {
	var newInstaslice inferencev1alpha1.Instaslice
	typeNamespacedName := types.NamespacedName{
		Name:      name,
		Namespace: InstaSliceOperatorNamespace,
	}
	err := kubeClient.Get(ctx, typeNamespacedName, &newInstaslice)
	if err != nil {
		return fmt.Errorf("error fetching the instaslice object: %s", name)
	}

	original := newInstaslice.DeepCopy()

	if newInstaslice.Spec.Allocations == nil {
		newInstaslice.Spec.Allocations = make(map[string]inferencev1alpha1.AllocationDetails)
	}

	for uuid, alloc := range newInstaslice.Spec.Allocations {
		if alloc.Allocationstatus == inferencev1alpha1.AllocationStatusDeleted {
			delete(newInstaslice.Spec.Allocations, uuid)
		}
	}
	newInstaslice.Spec.Allocations[podUUID] = allocation
	err = kubeClient.Patch(ctx, &newInstaslice, client.MergeFrom(original))
	if err != nil {
		return fmt.Errorf("error updating the instaslie object, %s, err: %v", name, err)
	}
	return nil
}

func RunningOnOpenshift(ctx context.Context, cl client.Client) bool {
	gvk := schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "route"}
	return isGvkPresent(ctx, cl, gvk)
}

// isGvkPresent returns whether the given gvk is present or not
func isGvkPresent(ctx context.Context, cl client.Client, gvk schema.GroupVersionKind) bool {
	log := logr.FromContext(ctx)
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := cl.List(ctx, list, &client.ListOptions{}); err != nil {
		if !meta.IsNoMatchError(err) {
			log.Error(err, "Unable to query", "gvk", gvk.String())
		}
		return false
	}
	return true
}
