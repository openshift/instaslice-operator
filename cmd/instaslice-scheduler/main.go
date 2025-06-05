package main

import (
	"os"

	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	_ "sigs.k8s.io/scheduler-plugins/apis/config/scheme"

	"github.com/openshift/instaslice-operator/pkg/scheduler/plugins/gpu"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(gpu.Name, gpu.New),
	)
	code := cli.Run(command)
	os.Exit(code)
}
