package daemonset

import (
	"context"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func RunDaemonset(ctx context.Context, cc *controllercmd.ControllerContext) error {
	<-ctx.Done()
	return nil
}
