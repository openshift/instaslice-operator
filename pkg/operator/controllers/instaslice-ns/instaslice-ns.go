package instaslicens

import (
	"context"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"
)

type InstasliceControllerNS struct {
	namespaceFilteredInformer informers.SharedInformerFactory
}

func NewInstasliceController(namespaceFilteredInformer informers.SharedInformerFactory, eventRecorder events.Recorder) factory.Controller {
	c := &InstasliceControllerNS{
		namespaceFilteredInformer: namespaceFilteredInformer,
	}

	return factory.New().
		WithSync(c.sync).
		WithInformersQueueKeysFunc(c.nameToKey, namespaceFilteredInformer.Core().V1().Namespaces().Informer()).
		ToController("InstasliceNamespaceController", eventRecorder)
}

func (c *InstasliceControllerNS) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.V(2).InfoS("Instaslice NS Sync", "queue_key", syncCtx.QueueKey())
	return nil
}

// queueKeysRuntimeForObj is an adapter on top of queueKeysForObj to be used in
// factory.Controller queueing functions
func (c *InstasliceControllerNS) nameToKey(obj runtime.Object) []string {
	metaObj, ok := obj.(metav1.ObjectMetaAccessor)
	if !ok {
		klog.Errorf("the object is not a metav1.ObjectMetaAccessor: %T", obj)
		return []string{}
	}
	return []string{metaObj.GetObjectMeta().GetName()}
}
