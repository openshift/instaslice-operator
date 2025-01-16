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

	"github.com/go-logr/logr"
	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const InstaSliceOperatorNamespace = "instaslice-system"

func UpdateInstasliceAllocations(ctx context.Context, kubeClient client.Client, name, podUUID string, allocationState inferencev1alpha1.AllocationStatus, allocation inferencev1alpha1.AllocationDetails) error {
	log, _ := logr.FromContext(ctx)
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
	newInstaslice.Spec.Allocations[podUUID] = allocation
	log.Info("the updated obj", "instaslice", newInstaslice)
	err = kubeClient.Patch(ctx, &newInstaslice, client.MergeFrom(original))
	if err != nil {
		return fmt.Errorf("error updating the instaslie object, %s, err: %v", name, err)
	}
	if newInstaslice.Status.AllocationStatus == nil {
		newInstaslice.Status.AllocationStatus = make(map[string][]inferencev1alpha1.AllocationStatus)
	}
	statuses := newInstaslice.Status.AllocationStatus[podUUID]
	exists := false
	for _, status := range statuses {
		if status == allocationState {
			exists = true
			break
		}
	}
	if !exists {
		newInstaslice.Status.AllocationStatus[podUUID] = append(newInstaslice.Status.AllocationStatus[podUUID], allocationState)
		err = kubeClient.Status().Patch(ctx, &newInstaslice, client.MergeFrom(original))
		if err != nil {
			return fmt.Errorf("error updating the instaslice status: %v", err)
		}
	}
	return nil
}
