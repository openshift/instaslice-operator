package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// RunScheduler is the entrypoint for the Instaslice secondary scheduler.
func RunScheduler(ctx context.Context, cc *controllercmd.ControllerContext) error {
	// 1. Create Kubernetes client
	kubeClient, err := kubernetes.NewForConfig(cc.ProtoKubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %w", err)
	}

	// 2. Set up informer factory and Pod informer with indexer
	// resync period: 30 minutes to reprocess all Pods and refresh cache
	informerFactory := informers.NewSharedInformerFactory(kubeClient, 30*time.Minute)
	podInformer := informerFactory.Core().V1().Pods().Informer()

	// index pods by .Spec.NodeName for usage lookup
	if err := podInformer.AddIndexers(cache.Indexers{
		"podsByNode": func(obj interface{}) ([]string, error) {
			pod, ok := obj.(*corev1.Pod)
			if !ok || pod.Spec.NodeName == "" {
				return nil, nil
			}
			return []string{pod.Spec.NodeName}, nil
		},
	}); err != nil {
		return fmt.Errorf("failed to add podsByNode index: %w", err)
	}

	// 3. Create a typed workqueue with a named config (type=string keys)
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "instaslice-scheduler"},
	)

	// 4. Register event handlers for Pods
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { enqueuePod(obj, queue) },
		UpdateFunc: func(old, new interface{}) { enqueuePod(new, queue) },
		DeleteFunc: func(obj interface{}) { enqueuePod(obj, queue) },
	})

	// 5. Start informers
	informerFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
		return fmt.Errorf("failed to sync pod informer")
	}

	// 6. Launch worker to process Pods
	go func() {
		podIndexer := podInformer.GetIndexer()
		for processNextWorkItem(ctx, kubeClient, podIndexer, queue) {
		}
	}()

	<-ctx.Done()
	return nil
}

// enqueuePod checks for our schedulerName and enqueues the pod key.
func enqueuePod(obj interface{}, queue workqueue.TypedRateLimitingInterface[string]) {
	var key string
	var err error
	// handle DeleteFinalStateUnknown
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if pod.Spec.SchedulerName != "instaslice-scheduler" {
		return
	}
	key, err = cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		return
	}
	queue.Add(key)
}

// processNextWorkItem processes items from the workqueue: fetch Pod, schedule, and bind.
func processNextWorkItem(ctx context.Context, kubeClient kubernetes.Interface, podIndexer cache.Indexer, queue workqueue.TypedRateLimitingInterface[string]) bool {
	key, shutdown := queue.Get()
	if shutdown {
		return false
	}
	defer queue.Done(key)
	defer queue.Forget(key)

	fmt.Printf("[instaslice-scheduler] processing pod %s\n", key)

	// split namespace/name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		fmt.Printf("invalid pod key: %v\n", err)
		return true
	}

	// get the Pod
	pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("error getting pod %s: %v\n", key, err)
		return true
	}
	// skip already scheduled Pods
	if pod.Spec.NodeName != "" {
		return true
	}

	// compute total resource requests for the Pod
	var reqCPU int64
	var reqMem int64
	var reqStorage int64
	for _, c := range pod.Spec.Containers {
		if cpuQty, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			reqCPU += cpuQty.MilliValue()
		}
		if memQty, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			reqMem += memQty.Value()
		}
		if stoQty, ok := c.Resources.Requests[corev1.ResourceEphemeralStorage]; ok {
			reqStorage += stoQty.Value()
		}
	}

	// list all nodes
	nodeList, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("error listing nodes: %v\n", err)
		return true
	}

	// filter nodes based on selectors, tolerations, available resources, and GPU availability
	var selectedNode, selectedGPU string
	for _, node := range nodeList.Items {
		// 1) NodeSelector
		if len(pod.Spec.NodeSelector) > 0 {
			sel := labels.SelectorFromSet(pod.Spec.NodeSelector)
			if !sel.Matches(labels.Set(node.Labels)) {
				continue
			}
		}
		// 2) Taints/Tolerations
		if !podToleratesTaints(pod, node.Spec.Taints) {
			continue
		}
		// 3) Compute current usage on node from local cache
		objs, err := podIndexer.ByIndex("podsByNode", node.Name)
		if err != nil {
			continue
		}
		var usedCPU, usedMem, usedStorage int64
		for _, obj := range objs {
			npod, ok := obj.(*corev1.Pod)
			if !ok {
				continue
			}
			if npod.Status.Phase != corev1.PodRunning && npod.Status.Phase != corev1.PodPending {
				continue
			}
			for _, c := range npod.Spec.Containers {
				if cpuQty, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
					usedCPU += (&cpuQty).MilliValue()
				}
				if memQty, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
					usedMem += (&memQty).Value()
				}
				if stoQty, ok := c.Resources.Requests[corev1.ResourceEphemeralStorage]; ok {
					usedStorage += (&stoQty).Value()
				}
			}
		}
		// 4) Available resources
		alloc := node.Status.Allocatable
		cpuQty := alloc[corev1.ResourceCPU]
		availCPU := (&cpuQty).MilliValue() - usedCPU
		if availCPU < reqCPU {
			continue
		}
		memQty := alloc[corev1.ResourceMemory]
		availMem := (&memQty).Value() - usedMem
		if availMem < reqMem {
			continue
		}
		stoQty := alloc[corev1.ResourceEphemeralStorage]
		availSto := (&stoQty).Value() - usedStorage
		if availSto < reqStorage {
			continue
		}
		// GPU selection stub: hard-coded list of UUIDs
		gpuUUIDs := []string{
			"GPU-1785aa6b-6edf-f58e-2e29-f6ccd30f306f",
			"GPU-11111111-2222-3333-4444-555555555555",
		}
		selectedGPU = ""
		for _, uuid := range gpuUUIDs {
			// TODO: apply real GPU capacity filter here
			selectedGPU = uuid
			break
		}
		if selectedGPU == "" {
			// no GPU available on this node, try next
			continue
		}
		// node passes all filters including GPU availability
		selectedNode = node.Name
		break
	}
	if selectedNode == "" {
		fmt.Printf("no fit nodes (with GPU) found for pod %s\n", key)
		return true
	}
	// report selected GPU for Pod
	fmt.Printf("selected GPU %s for pod %s\n", selectedGPU, key)

	// bind Pod to the selected node
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Target:     corev1.ObjectReference{Kind: "Node", Name: selectedNode},
	}
	if err := kubeClient.CoreV1().Pods(namespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
		fmt.Printf("failed to bind pod %s to node %s: %v\n", key, selectedNode, err)
		return true
	}
	fmt.Printf("bound pod %s to node %s with GPU %s\n", key, selectedNode, selectedGPU)
	return true
}

// podToleratesTaints checks if a Pod tolerates all NoSchedule taints of a node.
func podToleratesTaints(pod *corev1.Pod, taints []corev1.Taint) bool {
	for _, taint := range taints {
		if taint.Effect != corev1.TaintEffectNoSchedule {
			continue
		}
		tolerated := false
		for _, tol := range pod.Spec.Tolerations {
			if tol.Effect != "" && tol.Effect != taint.Effect {
				continue
			}
			op := tol.Operator
			if op == "" {
				op = corev1.TolerationOpEqual
			}
			switch op {
			case corev1.TolerationOpExists:
				if tol.Key == "" || tol.Key == taint.Key {
					tolerated = true
				}
			case corev1.TolerationOpEqual:
				if tol.Key == taint.Key && tol.Value == taint.Value {
					tolerated = true
				}
			}
			if tolerated {
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	return true
}
