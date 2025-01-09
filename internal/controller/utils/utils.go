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

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
