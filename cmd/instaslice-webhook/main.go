package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/instaslice-operator/pkg/cmd/webhook"
	"github.com/openshift/instaslice-operator/pkg/version"

	"github.com/spf13/cobra"

	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	cmd := NewInstasliceWebhookCommand(context.Background())
	if err := cmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func NewInstasliceWebhookCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instaslice-webhook",
		Short:   "OpenShift cluster Instaslice operator webhook",
		Version: version.Get().GitVersion,
		Run: func(cmd *cobra.Command, args []string) {
			err := cmd.Help()
			if err != nil {
				fmt.Printf("Unable to print help: %v", err)
			}
			os.Exit(1)
		},
	}

	cmd.AddCommand(webhook.NewWebhook(ctx))
	return cmd
}
