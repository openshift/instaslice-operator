package daemonset

import (
	"context"

	"github.com/spf13/cobra"
	"k8s.io/utils/clock"

	"github.com/openshift/instaslice-operator/pkg/daemonset"
	"github.com/openshift/instaslice-operator/pkg/version"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func NewDaemonset(ctx context.Context) *cobra.Command {
	cfg := controllercmd.NewControllerCommandConfig("instaslice-daemonset", version.Get(), daemonset.RunDaemonset, clock.RealClock{})
	cfg.DisableLeaderElection = true

	cmd := cfg.NewCommandWithContext(ctx)
	cmd.Use = "daemonset"
	cmd.Short = "Start the Cluster Instaslice Operator"

	return cmd
}
