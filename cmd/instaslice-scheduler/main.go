package main

import (
   "context"
   "fmt"
   "os"

   "github.com/spf13/cobra"
   "github.com/openshift/instaslice-operator/pkg/cmd/scheduler"
   "github.com/openshift/instaslice-operator/pkg/version"
)

func main() {
   command := NewInstasliceSchedulerCommand(context.Background())
   if err := command.Execute(); err != nil {
       fmt.Fprintf(os.Stderr, "%v\n", err)
       os.Exit(1)
   }
}

// NewInstasliceSchedulerCommand returns a cobra command for the secondary scheduler.
func NewInstasliceSchedulerCommand(ctx context.Context) *cobra.Command {
   cmd := &cobra.Command{
       Use:     "instaslice-scheduler",
       Short:   "Instaslice secondary scheduler",
       Version: version.Get().GitVersion,
       Run: func(cmd *cobra.Command, args []string) {
           if err := cmd.Help(); err != nil {
               fmt.Printf("Unable to print help: %v", err)
           }
           os.Exit(1)
       },
   }
   cmd.AddCommand(scheduler.NewScheduler(ctx))
   return cmd
}