package watcher

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// SetupPodDeletionWatcher sets up an informer to watch for Pod deletions and triggers a callback.
func SetupPodDeletionWatcher(ctx context.Context, kubeConfig *rest.Config) error {
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %w", err)
	}

	factory := informers.NewSharedInformerFactory(kubeClient, 0)
	podInformer := factory.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			// Decode the deleted object into a Pod (handles tombstones too).
			var pod *corev1.Pod
			switch t := obj.(type) {
			case *corev1.Pod:
				pod = t
			case cache.DeletedFinalStateUnknown:
				if p, ok := t.Obj.(*corev1.Pod); ok {
					pod = p
				} else {
					klog.V(4).InfoS("could not decode tombstone to Pod", "obj", t.Obj)
					return
				}
			default:
				klog.V(4).InfoS("unexpected object type in delete handler", "obj", obj)
				return
			}

			// TODO: Update the GPU capacity so the scheduler can use it.
			klog.InfoS("Pod deleted", "name", pod.Name, "namespace", pod.Namespace)
		},
	})
	go factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
		return fmt.Errorf("failed to sync pod informer cache")
	}
	return nil
}
