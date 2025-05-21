package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/openshift/instaslice-operator/pkg/cmd/daemonset"
	"github.com/openshift/instaslice-operator/pkg/version"
)

func main() {
	command := NewInstasliceDaemonsetCommand(context.Background())
	if err := command.Execute(); err != nil {
		_, err := fmt.Fprintf(os.Stderr, "%v\n", err)
		if err != nil {
			fmt.Printf("Unable to print err to stderr: %v", err)
		}
		os.Exit(1)
	}
}

func NewInstasliceDaemonsetCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instaslice-daemonset",
		Short:   "Instaslice Daemonset",
		Version: version.Get().GitVersion,
		Run: func(cmd *cobra.Command, args []string) {
			err := cmd.Help()
			if err != nil {
				fmt.Printf("Unable to print help: %v", err)
			}
			os.Exit(1)
		},
	}

	cmd.AddCommand(daemonset.NewDaemonset(ctx))
	return cmd
}
