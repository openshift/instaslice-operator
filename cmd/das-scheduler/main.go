package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/instaslice-operator/pkg/cmd/scheduler"
)

func main() {
	command := scheduler.NewScheduler(context.Background())
	command.Use = "das-scheduler"
	command.Short = "Instaslice Scheduler"
	if err := command.Execute(); err != nil {
		_, err := fmt.Fprintf(os.Stderr, "%v\n", err)
		if err != nil {
			fmt.Printf("Unable to print err to stderr: %v", err)
		}
		os.Exit(1)
	}
}
