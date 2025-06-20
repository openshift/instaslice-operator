package scheduler

import (
	"context"

	"github.com/spf13/cobra"
	"k8s.io/utils/clock"

	sched "github.com/openshift/instaslice-operator/pkg/scheduler"
	"github.com/openshift/instaslice-operator/pkg/version"

	scheduleroptions "k8s.io/kubernetes/cmd/kube-scheduler/app/options"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func NewScheduler(ctx context.Context) *cobra.Command {
	opts := scheduleroptions.NewOptions()
	// Disable scheduler leader election by default. The deployment runs a
	// single replica and doesn't require coordination.
	opts.LeaderElection.LeaderElect = false

	startFn := func(ctx context.Context, cc *controllercmd.ControllerContext) error {
		return sched.RunScheduler(ctx, cc, opts)
	}

	cfg := controllercmd.NewControllerCommandConfig("das-scheduler", version.Get(), startFn, clock.RealClock{})
	cfg.DisableLeaderElection = true

	cmd := cfg.NewCommandWithContext(ctx)
	cmd.Use = "scheduler"
	cmd.Short = "Start the Instaslice scheduler"

	// Propagate the config flag managed by the controller framework to the
	// kube-scheduler options. The framework already defines a --config flag
	// so the scheduler option is skipped; copy its value here.
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if v, err := cmd.Flags().GetString("config"); err == nil {
			opts.ConfigFile = v
		}
		return nil
	}

	for _, f := range opts.Flags.FlagSets {
		cmd.Flags().AddFlagSet(f)
	}

	return cmd
}
