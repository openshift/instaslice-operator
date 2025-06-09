package deviceplugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/instasliceoperator/v1alpha1"
	versioned "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

var (
	allocationIndexer cache.Indexer
	allocationMutex   sync.Mutex
)

// StartDevicePlugins starts device managers, gRPC servers, and registrars for each resource.
func StartDevicePlugins(ctx context.Context, kubeConfig *rest.Config) error {

	nodeName := os.Getenv("NODE_NAME")

	klog.InfoS("Starting discovery of MIG-enabled GPUs", "node", nodeName)
	if nodeName == "" {
		err := fmt.Errorf("NODE_NAME environment variable is required")
		klog.ErrorS(err, "NODE_NAME environment variable is required")
		return err
	}
	csOp, err := versioned.NewForConfig(kubeConfig)
	if err != nil {
		klog.ErrorS(err, "Failed to create operator client", "node", nodeName)
		return err
	}
	opClient := csOp.OpenShiftOperatorV1alpha1().InstasliceOperators(instasliceNamespace)
	instOp, err := opClient.Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to get InstasliceOperator", "node", nodeName)
		return err
	}

	emulatedMode := instOp.Spec.EmulatedMode
	var discoverer MigGpuDiscoverer
	if emulatedMode == instav1.EmulatedModeEnabled {
		discoverer = &EmulatedMigGpuDiscoverer{
			ctx:         ctx,
			nodeName:    nodeName,
			instaClient: csOp.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace),
		}
	} else {
		discoverer = &RealMigGpuDiscoverer{
			ctx:         ctx,
			nodeName:    nodeName,
			instaClient: csOp.OpenShiftOperatorV1alpha1().Instaslices(instasliceNamespace),
		}
	}
	if err := discoverer.Discover(); err != nil {
		klog.ErrorS(err, "Failed to discover MIG-enabled GPUs")
		return fmt.Errorf("failed to discover MIG GPUs: %w", err)
	}
	klog.InfoS("MIG GPU discovery completed")

	// Setup informer to watch Allocation resources. We index allocations by
	// the target node name to easily query allocations for this node.
	allocInformerFactory := instainformers.NewSharedInformerFactoryWithOptions(
		csOp, 10*time.Minute, instainformers.WithNamespace(instasliceNamespace))
	allocInformer := allocInformerFactory.OpenShiftOperator().V1alpha1().Allocations().Informer()

	// Index allocations by nodename and by the composite "node-gpu" key for quick lookup.
	err = allocInformer.AddIndexers(cache.Indexers{
		"nodename": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1.Allocation)
			if !ok {
				return []string{}, nil
			}
			return []string{string(a.Spec.Nodename)}, nil
		},
		"node-gpu": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1.Allocation)
			if !ok {
				return nil, nil
			}
			key := fmt.Sprintf("%s/%s", a.Spec.Nodename, a.Spec.GPUUUID)
			return []string{key}, nil
		},
	})
	if err != nil {
		klog.ErrorS(err, "Failed to add indexer to Allocation informer")
		return err
	}

	allocInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			a, ok := obj.(*instav1.Allocation)
			if !ok {
				return
			}
			// Perform a simple action on Allocation creation. For now we just log it.
			klog.InfoS("Allocation created", "name", a.Name, "node", a.Spec.Nodename, "Spec", a.Spec)
		},
	})

	// Start the informer in a separate goroutine.
	allocInformerFactory.Start(ctx.Done())
	allocationIndexer = allocInformer.GetIndexer()

	// define the extended resources to serve
	// TODO : load these from the instaslice CR
	resourceNames := []string{
		// TODO - rename these to nvidia.instaslice.com/mig-1g.5gb, etc.
		"instaslice.com/mig-1g.5gb",
		"instaslice.com/mig-2g.10gb",
		"instaslice.com/mig-7g.40gb",
	}
	const socketDir = "/var/lib/kubelet/device-plugins"

	for _, res := range resourceNames {
		mgr := NewManager(res)

		sanitized := strings.ReplaceAll(res, "/", "_")
		endpoint := sanitized + ".sock"
		socketPath := filepath.Join(socketDir, endpoint)

		srv, err := NewServer(mgr, socketPath, kubeConfig, emulatedMode)
		if err != nil {
			return fmt.Errorf("failed to create device plugin server for resource %q: %w", res, err)
		}
		if err := srv.Start(ctx); err != nil {
			return fmt.Errorf("device plugin server for resource %q failed: %w", res, err)
		}

		reg := NewRegistrar(socketPath, res)
		go reg.Start(ctx)
	}
	return nil
}

// GetAllocationsByNodeGPU returns up to the requested number of allocations
// indexed by nodename and GPU UUID. If fewer than the requested number of
// allocations are found, the lookup will be retried using an exponential
// backoff before giving up and returning an error. The lookup and returned slice
// creation are performed while holding a mutex to avoid races between multiple
// callers processing the same Allocation objects.
func GetAllocationsByNodeGPU(nodeName, gpuUUID string, count int) ([]*instav1.Allocation, error) {
	if count <= 0 {
		return nil, fmt.Errorf("requested allocation count must be greater than zero")
	}

	if allocationIndexer == nil {
		return nil, fmt.Errorf("allocation indexer not initialized")
	}

	key := fmt.Sprintf("%s/%s", nodeName, gpuUUID)
	var result []*instav1.Allocation
	err := wait.ExponentialBackoff(wait.Backoff{Duration: 100 * time.Millisecond, Factor: 2, Steps: 5}, func() (bool, error) {
		allocationMutex.Lock()
		defer allocationMutex.Unlock()
		objs, err := allocationIndexer.ByIndex("node-gpu", key)
		if err != nil {
			return false, err
		}

		out := make([]*instav1.Allocation, 0, len(objs))
		for _, obj := range objs {
			if a, ok := obj.(*instav1.Allocation); ok {
				out = append(out, a)
				if len(out) == count {
					break
				}
			}
		}
		result = out
		return len(result) >= count, nil
	})
	if err != nil {
		if wait.Interrupted(err) {
			return nil, fmt.Errorf("requested %d allocations but only found %d", count, len(result))
		}
		return nil, err
	}

	return result[:count], nil
}

// UpdateAllocationStatus safely updates the status of the given Allocation using
// the provided client while holding the allocation mutex. This prevents multiple
// goroutines from updating the same object concurrently.
func UpdateAllocationStatus(ctx context.Context, client versioned.Interface, alloc *instav1.Allocation, status instav1.AllocationStatus) (*instav1.Allocation, error) {
	allocationMutex.Lock()
	defer allocationMutex.Unlock()

	copy := alloc.DeepCopy()
	copy.Status = status
	return client.OpenShiftOperatorV1alpha1().Allocations(copy.Namespace).UpdateStatus(ctx, copy, metav1.UpdateOptions{})
}
