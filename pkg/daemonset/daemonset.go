package daemonset

import (
	"context"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
	// TODO remove me
	<-ctx.Done()
	return nil
}
