package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"k8s.io/klog/v2"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

// CDICache stores CDI specs loaded from disk. It is safe for concurrent use.
type CDICache struct {
	mu    sync.RWMutex
	specs map[string]*cdispec.Spec
}

// NewCDICache creates a new empty CDICache.
func NewCDICache() *CDICache {
	return &CDICache{specs: make(map[string]*cdispec.Spec)}
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
func SetupCDIDeletionWatcher(ctx context.Context, dir string, cache *CDICache) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	if err := w.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}
	klog.InfoS("Starting CDI watcher", "dir", dir)

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
					klog.V(3).InfoS("CDI spec removed", "path", event.Name)
					cache.delete(event.Name)
				case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
					klog.V(3).InfoS("CDI spec updated", "path", event.Name)
					loadSpec(event.Name, cache)
				}
			case err := <-w.Errors:
				klog.ErrorS(err, "fsnotify error")
			}
		}
	}()

	return nil
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
