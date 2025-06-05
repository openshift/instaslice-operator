package scheduler

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	instav1alpha1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"

	"github.com/openshift/instaslice-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"k8s.io/klog/v2"
)

// TODO - Replace all of this with scheduler plugins

// RunScheduler is the entrypoint for the Instaslice secondary scheduler.
func RunScheduler(ctx context.Context, cc *controllercmd.ControllerContext) error {
	kubeClient, err := kubernetes.NewForConfig(cc.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %w", err)
	}

	// Set up informer factory and Pod informer with indexer
	// resync period: 30 minutes to reprocess all Pods and refresh cache
	informerFactory := informers.NewSharedInformerFactory(kubeClient, 30*time.Minute)
	podInformer := informerFactory.Core().V1().Pods().Informer()
	podLister := informerFactory.Core().V1().Pods().Lister()
	nodeInformer := informerFactory.Core().V1().Nodes().Informer()

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
	// Trigger reschedule on Node changes
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { enqueuePodsForScheduling(podLister, queue) },
		UpdateFunc: func(old, new interface{}) { enqueuePodsForScheduling(podLister, queue) },
		DeleteFunc: func(obj interface{}) { enqueuePodsForScheduling(podLister, queue) },
	})

	// Set up Instaslice CRD informer with indexer

	cfg := rest.CopyConfig(cc.KubeConfig)
	cfg.AcceptContentTypes = "application/json"
	cfg.ContentType = "application/json"
	instaClient, err := instaclient.NewForConfig(cfg)

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
	// Trigger reschedule on Instaslice CR changes (GPU availability)
	instaInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { enqueuePodsForScheduling(podLister, queue) },
		UpdateFunc: func(old, new interface{}) { enqueuePodsForScheduling(podLister, queue) },
		DeleteFunc: func(obj interface{}) { enqueuePodsForScheduling(podLister, queue) },
	})
	// Start informers (Pods, Nodes, and Instaslice)
	informerFactory.Start(ctx.Done())
	instaInformerFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced, instaInformer.HasSynced, nodeInformer.HasSynced) {
		return fmt.Errorf("failed to sync informers")
	}
	// Set up operator config informers for dynamic log level
	opCfg := rest.CopyConfig(cc.KubeConfig)
	opCfg.AcceptContentTypes = "application/json"
	opCfg.ContentType = "application/json"
	opClientset, err := instaclient.NewForConfig(opCfg)
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
	klog.InfoS("Starting log level controller")
	go loglevel.NewClusterOperatorLoggingController(opClient, cc.EventRecorder).Run(ctx, 1)

	// Launch worker to process Pods
	nodeLister := informerFactory.Core().V1().Nodes().Lister()
	go func() {
		podIndexer := podInformer.GetIndexer()
		instIndexer := instaInformer.GetIndexer()
		for processNextWorkItem(ctx, kubeClient, nodeLister, podIndexer, instIndexer, instaClient, queue) {
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
	klog.InfoS("pod to consider ", "pod", pod.Name)
	key, err = cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.ErrorS(err, "Invalid pod key for enqueuePod", "pod", pod.Name)
		return
	}
	queue.Add(key)
}

// enqueuePodsForScheduling enqueues all unscheduled pods that use our scheduler.
func enqueuePodsForScheduling(podLister corev1listers.PodLister, queue workqueue.TypedRateLimitingInterface[string]) {
	pods, err := podLister.List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "Error listing pods for scheduling")
		return
	}
	for _, pod := range pods {
		if pod.Spec.SchedulerName != "instaslice-scheduler" || pod.Spec.NodeName != "" {
			continue
		}
		key, err := cache.MetaNamespaceKeyFunc(pod)
		if err != nil {
			klog.ErrorS(err, "Invalid pod key for enqueuePodsForScheduling", "pod", pod.Name)
			continue
		}
		queue.Add(key)
	}
}

// processNextWorkItem processes items from the workqueue: fetch Pod, schedule, and bind.
func processNextWorkItem(ctx context.Context, kubeClient kubernetes.Interface, nodeLister corev1listers.NodeLister, podIndexer cache.Indexer, instIndexer cache.Indexer, instaClient instaclient.Interface, queue workqueue.TypedRateLimitingInterface[string]) bool {
	key, shutdown := queue.Get()
	if shutdown {
		return false
	}
	defer queue.Done(key)

	// klog.V(4).InfoS("Processing pod", "key", key)
	klog.V(4).InfoS("Processing pod", "key", key)

	// split namespace/name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.ErrorS(err, "Invalid pod key")
		return true
	}

	// get the Pod
	pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Error getting pod", "key", key)
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

	// list all nodes from cache instead of direct API call
	nodeList, err := nodeLister.List(labels.Everything())
	if err != nil {
		klog.ErrorS(err, "Error listing nodes from cache")
		queue.AddRateLimited(key)
		return true
	}

	// filter nodes based on selectors, tolerations, available resources, and GPU availability
	var selectedNode, selectedGPU string
	for _, node := range nodeList {
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
		var allocRequest *instav1alpha1.AllocationRequest
		var allocResult *instav1alpha1.AllocationResult
		for _, uuid := range gpuUUIDs {
			// This is very ugly, TODO - move this to a dedicated scheduler plugin
			gpuAllocatedIndex := gpuAllocatedSlices(instObj, uuid)

			// TODO - iterate over all containers
			profileName := extractProfileName(pod.Spec.Containers[0].Resources.Limits)

			newStart := getStartIndexFromAllocationResults(instObj, profileName, gpuAllocatedIndex)

			// For example, a newStart of 9 is considered invalid.
			notValidIndex := int32(9)
			if newStart == notValidIndex {
				// Move to next GPU if the index is not valid.
				continue
			}

			size, discoveredGiprofile, Ciprofileid, Ciengprofileid := extractGpuProfile(instObj, profileName)

			allocRequest, allocResult = SetAllocationDetails(
				profileName,
				newStart,
				size,
				pod.GetUID(),
				types.NodeName(instObj.GetName()),
				instav1alpha1.AllocationStatus{AllocationStatusController: string(instav1alpha1.AllocationStatusCreating)},
				discoveredGiprofile,
				Ciprofileid,
				Ciengprofileid,
				pod.GetNamespace(),
				pod.GetName(),
				uuid,
				types.UID(pod.GetUID()),
			)

			if instObj.Spec.PodAllocationRequests == nil {
				newMap := make(map[types.UID]instav1alpha1.AllocationRequest)
				instObj.Spec.PodAllocationRequests = &newMap
			}
			(*instObj.Spec.PodAllocationRequests)[pod.GetUID()] = *allocRequest

			selectedGPU = uuid
			break
		}
		if selectedGPU == "" {
			continue
		}
		selectedNode = node.Name

		updatedObj, err := instaClient.OpenShiftOperatorV1alpha1().Instaslices(instObj.Namespace).
			Update(ctx, instObj, metav1.UpdateOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to update Instaslice spec", "instaslice", instObj.Name)
			break
		}

		if updatedObj.Status.PodAllocationResults == nil {
			updatedObj.Status.PodAllocationResults = make(map[string]instav1alpha1.AllocationResult)
		}
		updatedObj.Status.PodAllocationResults[string(pod.GetUID())] = *allocResult
		if _, err := instaClient.OpenShiftOperatorV1alpha1().Instaslices(instObj.Namespace).
			UpdateStatus(ctx, updatedObj, metav1.UpdateOptions{}); err != nil {
			klog.ErrorS(err, "Failed to update Instaslice status", "instaslice", instObj.Name)
		}
		break
	}
	if selectedNode == "" {
		klog.V(3).InfoS("No fit nodes (with GPU) found for pod", "pod", key)
		queue.AddRateLimited(key)
		return true
	}
	// report selected GPU for Pod
	klog.V(3).InfoS("Selected GPU for pod", "gpu", selectedGPU, "pod", key)

	// bind Pod to the selected node
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Target:     corev1.ObjectReference{Kind: "Node", Name: selectedNode},
	}
	if err := kubeClient.CoreV1().Pods(namespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
		klog.ErrorS(err, "Failed to bind pod to node", "pod", key, "node", selectedNode)
		queue.AddRateLimited(key)
		return true
	}
	klog.InfoS("Bound pod to node with GPU", "pod", key, "node", selectedNode, "gpu", selectedGPU)
	queue.Forget(key)
	return true
}

// Extract profile name from the container limits spec
func extractProfileName(limits corev1.ResourceList) string {
	profileName := ""
	for k := range limits {
		if strings.Contains(k.String(), "mig-") {

			re := regexp.MustCompile(`(\d+g\.\d+gb)`)
			match := re.FindStringSubmatch(k.String())
			if len(match) > 1 {
				profileName = match[1]
			}
		}
	}
	return profileName
}

func SetAllocationDetails(profileName string, newStart, size int32, podUUID types.UID, nodename types.NodeName,
	allocationStatus instav1alpha1.AllocationStatus, discoveredGiprofile int32, Ciprofileid int32, Ciengprofileid int32,
	namespace string, podName string, gpuUuid string, resourceIdentifier types.UID) (*instav1alpha1.AllocationRequest, *instav1alpha1.AllocationResult) {
	return &instav1alpha1.AllocationRequest{
			Profile: profileName,
			PodRef: corev1.ObjectReference{
				Kind:      "Pod",
				Namespace: namespace,
				Name:      podName,
				UID:       podUUID,
			},
		}, &instav1alpha1.AllocationResult{
			MigPlacement: instav1alpha1.Placement{
				Size:  size,
				Start: newStart,
			},
			GPUUUID:                     gpuUuid,
			Nodename:                    nodename,
			AllocationStatus:            allocationStatus,
			ConfigMapResourceIdentifier: resourceIdentifier,
			Conditions:                  []metav1.Condition{},
		}
}

// Extract NVML specific attributes for GPUs, this will change for different generations of the GPU.
func extractGpuProfile(instaslice *instav1alpha1.Instaslice, profileName string) (int32, int32, int32, int32) {
	var size int32
	var discoveredGiprofile int32
	var Ciprofileid int32
	var Ciengprofileid int32
	for profName, placement := range instaslice.Status.NodeResources.MigPlacement {
		if profName == profileName {
			for _, aPlacement := range placement.Placements {
				size = aPlacement.Size
				discoveredGiprofile = placement.GIProfileID
				Ciprofileid = placement.CIProfileID
				Ciengprofileid = placement.CIEngProfileID
				break
			}
		}
	}
	return size, discoveredGiprofile, Ciprofileid, Ciengprofileid
}

// getStartIndexFromPreparedState finds the correct GPU and index where a slice could be placed.
func getStartIndexFromAllocationResults(instaslice *instav1alpha1.Instaslice, profileName string, gpuAllocatedIndex [8]int32) int32 {
	// Check if all indices are allocated
	allAllocated := true
	for _, allocated := range gpuAllocatedIndex {
		if allocated != 1 {
			allAllocated = false
			break
		}
	}
	if allAllocated {
		// invalid index
		return int32(9)
	}
	var neededContinousSlot int32
	var possiblePlacements []int32
	for profile, placement := range instaslice.Status.NodeResources.MigPlacement {
		if profile == profileName {
			neededContinousSlot = placement.Placements[0].Size
			for _, placement := range placement.Placements {
				possiblePlacements = append(possiblePlacements, placement.Start)
			}
			break
		}
	}
	//TODO: generalize for other hardware models like A30, no slices can be placed on 9th index
	//if we return 9 then assume no valid index is found.
	var newStart = int32(9)
	for _, value := range possiblePlacements {
		if gpuAllocatedIndex[value] == 0 {
			if neededContinousSlot == 1 {
				newStart = value
				break
			}
			if neededContinousSlot == 2 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 {
						newStart = value
						break
					}
				}

			}
			if neededContinousSlot == 4 {
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 && gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 {
						newStart = value
						break
					}
				}
			}

			if neededContinousSlot == 8 {
				//special case
				if value+neededContinousSlot <= int32(len(gpuAllocatedIndex)) {
					if gpuAllocatedIndex[value] == 0 && gpuAllocatedIndex[value+1] == 0 &&
						gpuAllocatedIndex[value+2] == 0 && gpuAllocatedIndex[value+3] == 0 &&
						gpuAllocatedIndex[value+4] == 0 && gpuAllocatedIndex[value+5] == 0 &&
						gpuAllocatedIndex[value+6] == 0 && gpuAllocatedIndex[value+7] == 0 {
						newStart = value
					}
				}
			}
		}

	}
	return newStart
}

// gpuAllocatedSlices stores already allocated slices in indexes
func gpuAllocatedSlices(instObj *instav1alpha1.Instaslice, gpuUUID string) [8]int32 {
	//TODO: generalize, A100 and H100 have 8 indexes for 3g and 7g and 7 for rest, so go with 8 and we are bounded by
	var gpuAllocatedIndex [8]int32
	// clean slate init
	for i := range gpuAllocatedIndex {
		gpuAllocatedIndex[i] = 0
	}

	for _, allocResult := range instObj.Status.PodAllocationResults {
		for i := 0; i < int(allocResult.MigPlacement.Size); i++ {
			gpuAllocatedIndex[int(allocResult.MigPlacement.Start)+i] = 1
		}
	}
	return gpuAllocatedIndex
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
