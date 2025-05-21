package operator

import (
	"context"

	"github.com/spf13/cobra"

	"k8s.io/utils/clock"

	"github.com/openshift/instaslice-operator/pkg/operator"
	"github.com/openshift/instaslice-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func NewOperator(ctx context.Context) *cobra.Command {
	cmd := controllercmd.
		NewControllerCommandConfig("instaslice-operator", version.Get(), operator.RunOperator, clock.RealClock{}).
		NewCommandWithContext(ctx)
	cmd.Use = "operator"
	cmd.Short = "Start the Cluster Instaslice Operator"
	return cmd
}
