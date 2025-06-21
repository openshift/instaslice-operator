package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	kubernetes "k8s.io/client-go/kubernetes"
	kcache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
	versioned "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"

	nvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

const allocationAnnotationKey = "mig.das.com/allocation"

// CDICache stores CDI specs loaded from disk. It is safe for concurrent use.
type CDICache struct {
	cdi   *cdiapi.Cache
	mu    sync.RWMutex
	specs map[string]*cdispec.Spec
}

// NewCDICache creates a new empty CDICache. If cache is nil the default CDI
// cache is used.
func NewCDICache(cache *cdiapi.Cache) *CDICache {
	if cache == nil {
		cache = cdiapi.GetDefaultCache()
	}
	return &CDICache{cdi: cache, specs: make(map[string]*cdispec.Spec)}
}

// Get returns the spec for the given path if present.
func (c *CDICache) Get(path string) (*cdispec.Spec, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	spec, ok := c.specs[path]
	return spec, ok
}

func (c *CDICache) set(path string, spec *cdispec.Spec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.specs[path] = spec
}

func (c *CDICache) delete(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.specs, path)
}

// SetupCDIDeletionWatcher watches the given directory for CDI Spec file changes
// and updates the provided cache accordingly.
func SetupCDIDeletionWatcher(ctx context.Context, dir string, cache *CDICache, client versioned.Interface) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	if err := w.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}
	klog.InfoS("Starting CDI watcher ", "dir", dir)

	// Preload existing specs.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		klog.V(4).InfoS("preloading CDI spec", "path", p)
		loadSpec(p, cache)
	}

	go func() {
		defer w.Close()
		for {
			select {
			case <-ctx.Done():
				klog.InfoS("stopping CDI watcher", "dir", dir)
				return
			case event := <-w.Events:
				if filepath.Ext(event.Name) == ".tmp" {
					continue
				}
				switch {
				case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
					handleRemoveEvent(ctx, event.Name, cache, client)
				case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
					handleWriteEvent(event.Name, cache)
				}
			case err := <-w.Errors:
				klog.ErrorS(err, "fsnotify error")
			}
		}
	}()

	return nil
}

func handleRemoveEvent(ctx context.Context, path string, cache *CDICache, client versioned.Interface) {
	klog.V(3).InfoS("CDI spec removed", "path", path)
	if spec, ok := cache.Get(path); ok && client != nil {
		for _, dev := range spec.Devices {
			processDeviceRemoval(ctx, dev, path, client)
		}
	}
	cache.delete(path)
}

func handleWriteEvent(path string, cache *CDICache) {
	klog.V(3).InfoS("CDI spec updated", "path", path)
	loadSpec(path, cache)
}

func processDeviceRemoval(ctx context.Context, dev cdispec.Device, path string, client versioned.Interface) {
	ann, ok := dev.Annotations[allocationAnnotationKey]
	if !ok || ann == "" {
		return
	}
	var alloc instav1.AllocationClaim
	if err := json.Unmarshal([]byte(ann), &alloc); err != nil {
		klog.ErrorS(err, "failed to unmarshal allocation annotation", "path", path)
		return
	}
	if alloc.Name == "" {
		return
	}

	var migUUID string
	for _, e := range dev.ContainerEdits.Env {
		switch {
		case strings.HasPrefix(e, "NVIDIA_VISIBLE_DEVICES="):
			migUUID = strings.TrimPrefix(e, "NVIDIA_VISIBLE_DEVICES=")
			break
		case strings.HasPrefix(e, "MIG_UUID="):
			migUUID = strings.TrimPrefix(e, "MIG_UUID=")
			break
		}
	}
	if migUUID != "" && os.Getenv("EMULATED_MODE") != string(instav1.EmulatedModeEnabled) {
		if err := deleteMigSlice(migUUID); err != nil {
			klog.ErrorS(err, "failed to delete MIG slice", "uuid", migUUID)
		}
	}

	if err := client.OpenShiftOperatorV1alpha1().AllocationClaims(alloc.Namespace).Delete(ctx, alloc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		klog.ErrorS(err, "failed to delete AllocationClaim", "namespace", alloc.Namespace, "name", alloc.Name)
	} else {
		klog.V(3).InfoS("Deleted AllocationClaim for CDI spec", "namespace", alloc.Namespace, "name", alloc.Name)
	}
}

func loadSpec(path string, cache *CDICache) {
	// Skip temporary files created by the CDI library during writes.
	if filepath.Ext(path) == ".tmp" {
		return
	}

	spec, err := cdiapi.ReadSpec(path, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			klog.V(5).InfoS("CDI spec disappeared before it could be loaded", "path", path)
		} else {
			klog.ErrorS(err, "failed to load CDI spec", "path", path)
		}
		return
	}
	cache.set(path, spec.Spec)
	klog.V(4).InfoS("loaded CDI spec", "path", path)
}

// deleteMigSlice destroys the MIG slice identified by the given UUID. The MIG
// UUID is expected to refer to a valid MIG device on the node. Errors are
// returned so callers can log failures.
func deleteMigSlice(uuid string) error {
	if uuid == "" {
		return nil
	}

	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return fmt.Errorf("nvml init failed: %v", ret)
	}
	defer nvml.Shutdown()

	migDev, ret := nvml.DeviceGetHandleByUUID(uuid)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get mig device by uuid: %v", ret)
	}

	parent, ret := nvml.DeviceGetDeviceHandleFromMigDeviceHandle(migDev)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get parent device: %v", ret)
	}

	giID, ret := nvml.DeviceGetGpuInstanceId(migDev)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get gi id: %v", ret)
	}
	ciID, ret := nvml.DeviceGetComputeInstanceId(migDev)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get ci id: %v", ret)
	}

	gi, ret := parent.GetGpuInstanceById(giID)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get gpu instance: %v", ret)
	}

	ci, ret := gi.GetComputeInstanceById(ciID)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("get compute instance: %v", ret)
	}

	if ret := ci.Destroy(); ret != nvml.SUCCESS {
		return fmt.Errorf("destroy compute instance: %v", ret)
	}
	if ret := gi.Destroy(); ret != nvml.SUCCESS {
		return fmt.Errorf("destroy gpu instance: %v", ret)
	}

	klog.InfoS("Deleted MIG slice", "UUID", uuid)
	return nil
}

// getAllocationClaimSpec decodes the AllocationClaimSpec from the Raw extension.
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

// SetupPodDeletionWatcher registers a pod delete handler that removes any CDI
// specs associated with the deleted pod via allocation annotations. Spec file
// deletion will then trigger MIG and AllocationClaim cleanup via the CDI watcher.
func SetupPodDeletionWatcher(ctx context.Context, client kubernetes.Interface, nodeName string, cache *CDICache) error {
	if client == nil {
		return fmt.Errorf("kubernetes client is nil")
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		client, 0,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = fmt.Sprintf("spec.nodeName=%s", nodeName)
		}),
	)

	informer := factory.Core().V1().Pods().Informer()
	informer.AddEventHandler(kcache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			var pod *corev1.Pod
			switch t := obj.(type) {
			case *corev1.Pod:
				pod = t
			case kcache.DeletedFinalStateUnknown:
				if p, ok := t.Obj.(*corev1.Pod); ok {
					pod = p
				} else {
					return
				}
			default:
				return
			}
			handlePodDeletion(ctx, pod, cache)
		},
	})

	factory.Start(ctx.Done())
	return nil
}

// handlePodDeletion removes all CDI spec files referencing the given pod UID.
func handlePodDeletion(ctx context.Context, pod *corev1.Pod, cache *CDICache) {
	if pod == nil {
		return
	}

	var paths []string
	cache.mu.RLock()
	for path, spec := range cache.specs {
		for _, dev := range spec.Devices {
			ann, ok := dev.Annotations[allocationAnnotationKey]
			if !ok || ann == "" {
				continue
			}
			var alloc instav1.AllocationClaim
			if err := json.Unmarshal([]byte(ann), &alloc); err != nil {
				klog.ErrorS(err, "failed to unmarshal allocation annotation", "path", path)
				continue
			}
			specObj, err := getAllocationClaimSpec(&alloc)
			if err != nil {
				klog.ErrorS(err, "failed to decode allocation spec", "allocation", alloc.Name)
				continue
			}
			if specObj.PodRef.UID == pod.UID {
				paths = append(paths, path)
				break
			}
		}
	}
	cache.mu.RUnlock()

	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			klog.ErrorS(err, "failed to remove CDI spec for deleted pod", "pod", klog.KObj(pod), "path", p)
		} else {
			klog.V(3).InfoS("removed CDI spec for deleted pod", "pod", klog.KObj(pod), "path", p)
		}
	}
}
