package deviceplugins

import (
	"fmt"
	"sync"

	nvml "github.com/NVIDIA/go-nvml/pkg/nvml"
	"k8s.io/klog/v2"
)

var (
	nvmlInitOnce     sync.Once
	nvmlShutdownOnce sync.Once
	nvmlInitErr      error
)

// EnsureNvmlInitialized initializes NVML only once. Subsequent calls return the
// initialization result from the first attempt.
func EnsureNvmlInitialized() error {
	nvmlInitOnce.Do(func() {
		if ret := nvml.Init(); ret != nvml.SUCCESS {
			nvmlInitErr = fmt.Errorf("nvml init failed: %v", ret)
		}
	})
	return nvmlInitErr
}

// ShutdownNvml shuts down NVML the first time it is called.
// It can be safely deferred from multiple places.
func ShutdownNvml() {
	nvmlShutdownOnce.Do(shutdownNvml)
}

// shutdownNvml logs an error if NVML shutdown fails. It does not attempt to
// track whether NVML was initialized.
func shutdownNvml() {
	if ret := nvml.Shutdown(); ret != nvml.SUCCESS {
		klog.ErrorS(fmt.Errorf("nvml shutdown failed: %v", ret), "nvml shutdown")
	}
}
