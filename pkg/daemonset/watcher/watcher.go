package watcher

import (
   "context"
   "fmt"

   "k8s.io/client-go/informers"
   "k8s.io/client-go/kubernetes"
   "k8s.io/client-go/rest"
   "k8s.io/client-go/tools/cache"
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
           // TODO: take some action when a pod is deleted
       },
   })
   go factory.Start(ctx.Done())
   if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
       return fmt.Errorf("failed to sync pod informer cache")
   }
   return nil
}