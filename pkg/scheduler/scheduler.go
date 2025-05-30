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

   instav1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
   instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
   instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"

   "github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
   "github.com/openshift/library-go/pkg/operator/loglevel"
   "k8s.io/klog/v2"
)

// RunScheduler is the entrypoint for the Instaslice secondary scheduler.
func RunScheduler(ctx context.Context, cc *controllercmd.ControllerContext) error {
	kubeClient, err := kubernetes.NewForConfig(cc.ProtoKubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %w", err)
	}

	// Set up informer factory and Pod informer with indexer
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

	// Create a typed workqueue with a named config (type=string keys)
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "instaslice-scheduler"},
	)

	// Register event handlers for Pods
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { enqueuePod(obj, queue) },
		UpdateFunc: func(old, new interface{}) { enqueuePod(new, queue) },
		DeleteFunc: func(obj interface{}) { enqueuePod(obj, queue) },
	})

	// Set up Instaslice CRD informer with indexer
	instaClient, err := instaclient.NewForConfig(cc.ProtoKubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create instaslice client: %w", err)
	}
	instaInformerFactory := instainformers.NewSharedInformerFactory(instaClient, 30*time.Minute)
	instaInformer := instaInformerFactory.OpenShiftOperator().V1alpha1().Instaslices().Informer()
	if err := instaInformer.AddIndexers(cache.Indexers{
		"instasliceByNode": func(obj interface{}) ([]string, error) {
			is, ok := obj.(*instav1alpha1.Instaslice)
			if !ok {
				return nil, nil
			}
			return []string{is.Name}, nil
		},
	}); err != nil {
		return fmt.Errorf("failed to add instasliceByNode index: %w", err)
	}
	// Start informers (Pods and Instaslice)
	informerFactory.Start(ctx.Done())
	instaInformerFactory.Start(ctx.Done())
   if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced, instaInformer.HasSynced) {
       return fmt.Errorf("failed to sync informers")
   }
   // Set up operator config informers for dynamic log level
   opClientset, err := instaclient.NewForConfig(cc.KubeConfig)
   if err != nil {
       return fmt.Errorf("failed to create instaslice operator client: %w", err)
   }
   operatorNamespace := cc.OperatorNamespace
   if operatorNamespace == "openshift-config-managed" {
       operatorNamespace = "instaslice-system"
   }
   opInformerFactory := instainformers.NewSharedInformerFactory(opClientset, 10*time.Minute)
   opClient := &operatorclient.InstasliceOperatorSetClient{
       Ctx:               ctx,
       SharedInformer:    opInformerFactory.OpenShiftOperator().V1alpha1().InstasliceOperators().Informer(),
       Lister:            opInformerFactory.OpenShiftOperator().V1alpha1().InstasliceOperators().Lister(),
       OperatorClient:    opClientset.OpenShiftOperatorV1alpha1(),
       OperatorNamespace: operatorNamespace,
   }
   opInformerFactory.Start(ctx.Done())
   klog.Info("Starting log level controller")
   go loglevel.NewClusterOperatorLoggingController(opClient, cc.EventRecorder).Run(ctx, 1)

	// Launch worker to process Pods
	go func() {
		podIndexer := podInformer.GetIndexer()
		instIndexer := instaInformer.GetIndexer()
		for processNextWorkItem(ctx, kubeClient, podIndexer, instIndexer, queue) {
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
func processNextWorkItem(ctx context.Context, kubeClient kubernetes.Interface, podIndexer cache.Indexer, instIndexer cache.Indexer, queue workqueue.TypedRateLimitingInterface[string]) bool {
	key, shutdown := queue.Get()
	if shutdown {
		return false
	}
	defer queue.Done(key)
	defer queue.Forget(key)

	klog.V(4).Infof("[instaslice-scheduler] processing pod %s", key)

	// split namespace/name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
   if err != nil {
       klog.Errorf("invalid pod key: %v", err)
		return true
	}

	// get the Pod
	pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        klog.Errorf("error getting pod %s: %v", key, err)
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
        klog.Errorf("error listing nodes: %v", err)
		return true
	}

	// filter nodes based on selectors, tolerations, available resources, and GPU availability
	var selectedNode, selectedGPU string
	for _, node := range nodeList.Items {
		// NodeSelector
		if len(pod.Spec.NodeSelector) > 0 {
			sel := labels.SelectorFromSet(pod.Spec.NodeSelector)
			if !sel.Matches(labels.Set(node.Labels)) {
				continue
			}
		}
		// Taints/Tolerations
		if !podToleratesTaints(pod, node.Spec.Taints) {
			continue
		}
		// Compute current usage on node from local cache
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
		// retrieve Instaslice CR for this node to get GPU information
		instObjs, err := instIndexer.ByIndex("instasliceByNode", node.Name)
		if err != nil {
			continue
		}
		if len(instObjs) == 0 {
			continue
		}
		instObj, ok := instObjs[0].(*instav1alpha1.Instaslice)
		if !ok {
			continue
		}
		// extract GPU UUIDs from Instaslice status
		var gpuUUIDs []string
		for _, gpu := range instObj.Status.NodeResources.NodeGPUs {
			gpuUUIDs = append(gpuUUIDs, gpu.GPUUUID)
		}
		selectedGPU = ""
		for _, uuid := range gpuUUIDs {
			selectedGPU = uuid
			break
		}
		if selectedGPU == "" {
			continue
		}
		selectedNode = node.Name
		break
	}
    if selectedNode == "" {
        klog.V(3).Infof("no fit nodes (with GPU) found for pod %s", key)
        return true
    }
	// report selected GPU for Pod
    klog.V(3).Infof("selected GPU %s for pod %s", selectedGPU, key)

	// bind Pod to the selected node
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Target:     corev1.ObjectReference{Kind: "Node", Name: selectedNode},
	}
    if err := kubeClient.CoreV1().Pods(namespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
        klog.Errorf("failed to bind pod %s to node %s: %v", key, selectedNode, err)
        return true
    }
    klog.Infof("bound pod %s to node %s with GPU %s", key, selectedNode, selectedGPU)
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
