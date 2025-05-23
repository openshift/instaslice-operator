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

package cache

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	utilcache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
)

var _ ctrlmgr.Runnable = (*ResourceTracker)(nil)

type ResourceTracker struct {
	cache   *ResourceCache
	factory informers.SharedInformerFactory
}

func NewResourceTracker(cfg *rest.Config) (*ResourceTracker, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	f := informers.NewSharedInformerFactory(cs, 0)
	t := &ResourceTracker{
		cache:   NewResourceCache(),
		factory: f,
	}

	if _, err := f.Core().V1().Nodes().Informer().
		AddEventHandler(t.cache.ResourceEventHandlerForNode()); err != nil {
		return nil, fmt.Errorf("failed to add node handler: %w", err)
	}

	if _, err := f.Core().V1().Pods().Informer().
		AddEventHandler(t.cache.ResourceEventHandlerForPod()); err != nil {
		return nil, fmt.Errorf("failed to add pod handler: %w", err)
	}

	return t, nil
}

func (t *ResourceTracker) Start(ctx context.Context) error {
	t.factory.Start(ctx.Done())
	if ok := utilcache.WaitForCacheSync(ctx.Done(),
		t.factory.Core().V1().Pods().Informer().HasSynced,
		t.factory.Core().V1().Nodes().Informer().HasSynced); !ok {
		return fmt.Errorf("ResourceTracker: informers did not sync")
	}
	if err := t.cache.Rebuild(
		t.factory.Core().V1().Nodes().Lister(),
		t.factory.Core().V1().Pods().Lister()); err != nil {
		return err
	}
	// periodic reconcile
	go wait.Until(func() {
		err := t.cache.Rebuild(
			t.factory.Core().V1().Nodes().Lister(),
			t.factory.Core().V1().Pods().Lister())
		t.Cache().DebugDump()
		if err != nil {
			klog.Info("failed to rebuild the cache during periodic reconcile: %w", err)
		}
	}, 10*time.Minute, ctx.Done())

	<-ctx.Done()
	return nil
}

func (t *ResourceTracker) Cache() *ResourceCache { return t.cache }
