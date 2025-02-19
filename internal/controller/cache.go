/*
Copyright 2025.

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
package controller

import (
	"context"

	inferencev1alpha1 "github.com/openshift/instaslice-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
)

// Cache avoids non determinism in the system which occurs when we play
// catch game by being upto date with .status.podallocationresults. status
// takes time to propogate and often causes controller to assign same slice
// to multiple pods.

func (r *InstasliceReconciler) rebuildAllocationCache(ctx context.Context) error {
	// Only rebuild if the cache is empty (indicating a controller restart)
	if r.isCacheInitialized {
		return nil
	}

	// Fetch all Instaslice objects in the cluster
	// TODO: cache is rebuilt on node failure we should
	// avoid instaslice objects that are related to failed
	// nodes in the cluster.
	var instasliceList inferencev1alpha1.InstasliceList
	if err := r.List(ctx, &instasliceList); err != nil {
		logr.FromContext(ctx).Error(err, "Error listing Instaslice objects")
		return err
	}

	r.allocationCache = make(map[types.UID]inferencev1alpha1.AllocationResult)

	for _, instaslice := range instasliceList.Items {
		for podUid, allocResult := range instaslice.Status.PodAllocationResults {
			if allocResult.AllocationStatus.AllocationStatusDaemonset == inferencev1alpha1.AllocationStatusDeleted {
				continue
			}
			r.allocationCache[podUid] = allocResult
		}
	}

	r.isCacheInitialized = true
	return nil
}

func (r *InstasliceReconciler) updateCacheWithNewAllocation(ctx context.Context, podUid types.UID, allocResult inferencev1alpha1.AllocationResult) {

	r.allocationCache[podUid] = allocResult
}
