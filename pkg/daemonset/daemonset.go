package daemonset

import (
	"context"
	"fmt"

	"github.com/openshift/instaslice-operator/pkg/daemonset/device"
	"github.com/openshift/instaslice-operator/pkg/daemonset/watcher"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
	// Discover MIG-enabled GPUs and populate Instaslice CR before starting plugins
	if err := discoverMigEnabledGpuWithSlices(ctx, cc.KubeConfig); err != nil {
		return fmt.Errorf("failed to discover MIG GPUs: %w", err)
	}
	// Patch node with max MIG placements capacity
	if err := addMigCapacityToNode(ctx, cc.KubeConfig); err != nil {
		return fmt.Errorf("failed to patch MIG capacity on node: %w", err)
	}
	if err := device.StartDevicePlugins(ctx); err != nil {
		return err
	}

	if err := watcher.SetupPodDeletionWatcher(ctx, cc.KubeConfig); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}
