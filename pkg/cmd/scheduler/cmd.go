package scheduler

import (
   "context"

   "github.com/spf13/cobra"
   "k8s.io/utils/clock"

   "github.com/openshift/instaslice-operator/pkg/scheduler"
   "github.com/openshift/instaslice-operator/pkg/version"
   "github.com/openshift/library-go/pkg/controller/controllercmd"
)

// NewScheduler returns the cobra command to run the secondary scheduler.
func NewScheduler(ctx context.Context) *cobra.Command {
   cfg := controllercmd.NewControllerCommandConfig("instaslice-scheduler", version.Get(), scheduler.RunScheduler, clock.RealClock{})
   cfg.DisableLeaderElection = true

   cmd := cfg.NewCommandWithContext(ctx)
   cmd.Use = "scheduler"
   cmd.Short = "Start the Instaslice secondary scheduler"
   return cmd
}