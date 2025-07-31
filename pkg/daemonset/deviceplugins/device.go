package deviceplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	versioned "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	instainformers "github.com/openshift/instaslice-operator/pkg/generated/informers/externalversions"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamic "k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	allocationIndexer cache.Indexer
	allocationMutex   sync.Mutex
)

// sanitizeProfileName replaces characters not allowed in Kubernetes resource
// names with a safe representation. Currently this only replaces '+' with
// "-plus-".
func sanitizeProfileName(profile string) string {
	return strings.ReplaceAll(profile, "+", "-plus-")
}

// unsanitizeProfileName reverses sanitizeProfileName, converting "-plus-" back
// to '+'.
func unsanitizeProfileName(profile string) string {
	return strings.ReplaceAll(profile, "-plus-", "+")
}

func getAllocationClaimSpec(a *instav1.AllocationClaim) (instav1.AllocationClaimSpec, error) {
	if a == nil {
		return instav1.AllocationClaimSpec{}, fmt.Errorf("allocation claim is nil")
	}
	var spec instav1.AllocationClaimSpec
	if len(a.Spec.Raw) == 0 {
		return spec, fmt.Errorf("allocation claim spec is empty")
	}
	if err := json.Unmarshal(a.Spec.Raw, &spec); err != nil {
		return instav1.AllocationClaimSpec{}, fmt.Errorf("failed to decode allocation spec: %w", err)
	}
	return spec, nil
}

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
	dynClient, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		klog.ErrorS(err, "Failed to create dynamic client", "node", nodeName)
		return err
	}
	if err := waitForClusterPolicy(ctx, dynClient); err != nil {
		return err
	}
	emulatedModeStr := os.Getenv("EMULATED_MODE")
	var emulatedMode instav1.EmulatedMode
	if emulatedModeStr == "" {
		emulatedMode = instav1.EmulatedModeDisabled
	} else {
		emulatedMode = instav1.EmulatedMode(emulatedModeStr)
	}
	var discoverer MigGpuDiscoverer
	if emulatedMode == instav1.EmulatedModeEnabled {
		discoverer = &EmulatedMigGpuDiscoverer{
			ctx:         ctx,
			nodeName:    nodeName,
			instaClient: csOp.OpenShiftOperatorV1alpha1().NodeAccelerators(instasliceNamespace),
		}
	} else {
		discoverer = &RealMigGpuDiscoverer{
			ctx:           ctx,
			nodeName:      nodeName,
			instaClient:   csOp.OpenShiftOperatorV1alpha1().NodeAccelerators(instasliceNamespace),
			dynamicClient: dynClient,
		}
	}
	instObj, err := discoverer.Discover()
	if err != nil {
		klog.ErrorS(err, "Failed to discover MIG-enabled GPUs")
		return fmt.Errorf("failed to discover MIG GPUs: %w", err)
	}
	klog.InfoS("MIG GPU discovery completed")

	// Setup informer to watch AllocationClaim resources. We index allocations by
	// the target node name to easily query allocations for this node.
	allocInformerFactory := instainformers.NewSharedInformerFactoryWithOptions(
		csOp, 10*time.Minute, instainformers.WithNamespace(instasliceNamespace))
	allocInformer := allocInformerFactory.OpenShiftOperator().V1alpha1().AllocationClaims().Informer()

	// Index allocations by nodename, by the composite "node-gpu" key and by
	// "node-MigProfile" for quick lookup.
	err = allocInformer.AddIndexers(cache.Indexers{
		"nodename": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1.AllocationClaim)
			if !ok {
				return []string{}, nil
			}
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			return []string{string(spec.Nodename)}, nil
		},
		"node-gpu": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1.AllocationClaim)
			if !ok {
				return nil, nil
			}
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s/%s", spec.Nodename, spec.GPUUUID)
			return []string{key}, nil
		},
		"node-MigProfile": func(obj interface{}) ([]string, error) {
			a, ok := obj.(*instav1.AllocationClaim)
			if !ok {
				return nil, nil
			}
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s/%s", spec.Nodename, spec.Profile)
			return []string{key}, nil
		},
	})
	if err != nil {
		klog.ErrorS(err, "Failed to add indexer to Allocation informer")
		return err
	}

	if _, err = allocInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			a, ok := obj.(*instav1.AllocationClaim)
			if !ok {
				return
			}
			spec, err := getAllocationClaimSpec(a)
			if err != nil {
				klog.ErrorS(err, "failed to decode allocation spec")
				return
			}
			// Perform a simple action on AllocationClaim creation. For now we just log it.
			klog.InfoS("AllocationClaim created", "name", a.Name, "node", spec.Nodename, "Spec", spec)
		},
	}); err != nil {
		klog.ErrorS(err, "Failed to add AllocationClaim handler")
		return err
	}

	// Start the informer in a separate goroutine.
	allocInformerFactory.Start(ctx.Done())
	allocationIndexer = allocInformer.GetIndexer()

	// Use the NodeAccelerator returned by the discovery step
	var discovered instav1.DiscoveredNodeResources
	if len(instObj.Status.NodeResources.Raw) > 0 {
		if err := json.Unmarshal(instObj.Status.NodeResources.Raw, &discovered); err != nil {
			klog.ErrorS(err, "Failed to unmarshal NodeResources")
			return err
		}
	}

	// define the extended resources to serve based on the discovered
	// NodeAccelerator resources
	resourceNames := make([]string, 0, len(discovered.MigPlacement))
	for profile := range discovered.MigPlacement {
		sanitizedProfile := sanitizeProfileName(profile)
		resourceNames = append(resourceNames, fmt.Sprintf("mig.das.com/%s", sanitizedProfile))
	}
	// ensure deterministic ordering for stable socket names
	sort.Strings(resourceNames)
	const socketDir = "/var/lib/kubelet/device-plugins"

	for _, res := range resourceNames {
		mgr := NewManager(res, discovered)

		sanitized := strings.ReplaceAll(res, "/", "_")
		sanitized = sanitizeProfileName(sanitized)
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

	// Create GPU memory resource manager for Kueue integration
	if err := createGPUMemoryResourceManager(ctx, discovered, kubeConfig, emulatedMode); err != nil {
		return fmt.Errorf("failed to create GPU memory resource manager: %w", err)
	}

	return nil
}

// createGPUMemoryResourceManager creates a device plugin manager for GPU memory resources
func createGPUMemoryResourceManager(ctx context.Context, discovered instav1.DiscoveredNodeResources, kubeConfig *rest.Config, emulatedMode instav1.EmulatedMode) error {
	const socketDir = "/var/lib/kubelet/device-plugins"
	const gpuMemoryResource = "gpu.das.com/mem"

	// Calculate total available GPU memory in GB
	var totalMemoryGB int64
	for _, gpu := range discovered.NodeGPUs {
		totalMemoryGB += gpu.GPUMemory.Value() / (1024 * 1024 * 1024) // Convert bytes to GB
	}

	if totalMemoryGB == 0 {
		klog.InfoS("no GPU memory available, skipping GPU memory resource manager")
		return nil
	}

	mgr := NewGPUMemoryManager(gpuMemoryResource, discovered)
	endpoint := "gpu_das_com_mem.sock"
	socketPath := filepath.Join(socketDir, endpoint)

	srv, err := NewServer(mgr, socketPath, kubeConfig, emulatedMode)
	if err != nil {
		return fmt.Errorf("failed to create GPU memory device plugin server: %w", err)
	}
	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("GPU memory device plugin server failed: %w", err)
	}

	reg := NewRegistrar(socketPath, gpuMemoryResource)
	go reg.Start(ctx)

	klog.InfoS("GPU memory resource manager started", "total_memory_gb", totalMemoryGB)
	return nil
}

// UpdateAllocationStatus safely updates the status of the given AllocationClaim using
// the provided client while holding the allocation mutex. This prevents multiple
// goroutines from updating the same object concurrently.
func UpdateAllocationStatus(ctx context.Context, client versioned.Interface, alloc *instav1.AllocationClaim, status instav1.AllocationClaimState) (*instav1.AllocationClaim, error) {
	allocationMutex.Lock()
	defer allocationMutex.Unlock()

	var result *instav1.AllocationClaim
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current, err := client.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Get(ctx, alloc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		copy := current.DeepCopy()
		copy.Status.State = status
		cond := metav1.Condition{
			Type:               "State",
			Status:             metav1.ConditionTrue,
			Reason:             string(status),
			Message:            fmt.Sprintf("Allocation is %s", status),
			ObservedGeneration: copy.Generation,
		}
		meta.SetStatusCondition(&copy.Status.Conditions, cond)

		result, err = client.OpenShiftOperatorV1alpha1().AllocationClaims(copy.Namespace).UpdateStatus(ctx, copy, metav1.UpdateOptions{})
		return err
	})

	return result, err
}
