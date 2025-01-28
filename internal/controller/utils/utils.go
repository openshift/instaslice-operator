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
	"sigs.k8s.io/controller-runtime/pkg/log"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
)

const InstaSliceOperatorNamespace = "instaslice-system"

func UpdateOrDeleteInstasliceAllocations(ctx context.Context, kubeClient client.Client, name string, allocResult *inferencev1alpha1.AllocationResult, allocRequest *inferencev1alpha1.AllocationRequest) error {
	var newInstaslice inferencev1alpha1.Instaslice
	typeNamespacedName := types.NamespacedName{
		Name:      name,
		Namespace: InstaSliceOperatorNamespace,
	}
	err := kubeClient.Get(ctx, typeNamespacedName, &newInstaslice)
	if err != nil {
		return fmt.Errorf("error fetching the instaslice object: %s", name)
	}

	originalInstaSliceObj := newInstaslice.DeepCopy()

	if newInstaslice.Spec.PodAllocationRequests == nil {
		newInstaslice.Spec.PodAllocationRequests = make(map[types.UID]inferencev1alpha1.AllocationRequest)
	}
	var keysToDelete []types.UID
	for uuid, alloc := range newInstaslice.Status.PodAllocationResults {
		if alloc.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
			keysToDelete = append(keysToDelete, uuid)
		}
	}

	for _, uuid := range keysToDelete {
		delete(newInstaslice.Spec.PodAllocationRequests, uuid)
	}
	if allocRequest != nil && allocRequest.PodRef.UID != "" {
		newInstaslice.Spec.PodAllocationRequests[allocRequest.PodRef.UID] = *allocRequest
	}
	err = kubeClient.Patch(ctx, &newInstaslice, client.MergeFrom(originalInstaSliceObj))
	if err != nil {
		return fmt.Errorf("error updating the instaslie object, %s, err: %v", name, err)
	}

	err = kubeClient.Get(ctx, typeNamespacedName, &newInstaslice)
	if err != nil {
		return fmt.Errorf("error fetching the instaslice object: %s", name)
	}

	originalInstaSliceObj = newInstaslice.DeepCopy()

	if newInstaslice.Status.PodAllocationResults == nil {
		newInstaslice.Status.PodAllocationResults = make(map[types.UID]inferencev1alpha1.AllocationResult)
	}
	if allocRequest != nil && allocRequest.PodRef.UID != "" {
		newInstaslice.Status.PodAllocationResults[allocRequest.PodRef.UID] = *allocResult
	}
	for _, uuid := range keysToDelete {
		delete(newInstaslice.Status.PodAllocationResults, uuid)
	}
	if allocRequest != nil && allocResult != nil {
		log.FromContext(ctx).Info("setting status ", "controller", allocResult.AllocationStatus.AllocationStatusController, "podid", allocRequest.PodRef.UID)
		log.FromContext(ctx).Info("setting status ", "daemonset", allocResult.AllocationStatus.AllocationStatusDaemonset, "podid", allocRequest.PodRef.UID)
	}
	err = kubeClient.Status().Patch(ctx, &newInstaslice, client.MergeFrom(originalInstaSliceObj)) // TODO - try with update
	if err != nil {
		log.FromContext(ctx).Info("error patching allocation result ", err, "pod uuid", allocRequest.PodRef.UID)
		return fmt.Errorf("error updating the instaslie object status, %s, err: %v", name, err)
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
