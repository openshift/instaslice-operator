package daemonset

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/instaslice-operator/pkg/daemonset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/openshift/instaslice-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

var (
	tlsMinVersion   string
	tlsCipherSuites []string
)

func NewDaemonset(ctx context.Context) *cobra.Command {
	cfg := controllercmd.NewControllerCommandConfig("das-daemonset", version.Get(), daemonset.RunDaemonset, clock.RealClock{})
	cfg.DisableLeaderElection = true

	cmd := cfg.NewCommandWithContext(ctx)
	cmd.Use = "daemonset"
	cmd.Short = "Start the Cluster Instaslice Operator"

	// TLS security profile flags - values injected by operator per OCPSTRAT-2611
	cmd.Flags().StringVar(&tlsMinVersion, "tls-min-version", "VersionTLS12",
		"Minimum TLS version (e.g., VersionTLS12, VersionTLS13)")
	cmd.Flags().StringSliceVar(&tlsCipherSuites, "tls-cipher-suites", nil,
		"Comma-separated list of TLS cipher suites (IANA names)")

	// Wrap the Run function to inject TLS config
	originalRun := cmd.Run
	cmd.Run = func(cmd *cobra.Command, args []string) {
		// Only inject config if --config flag is not already set
		configFlag := cmd.Flag("config")
		if configFlag == nil || configFlag.Value.String() == "" {
			configFile, err := createTLSConfigFileFromFlags()
			if err != nil {
				klog.V(4).InfoS("Could not create TLS config file, using defaults", "error", err)
			} else if configFile != "" {
				if err := cmd.Flags().Set("config", configFile); err != nil {
					klog.V(4).InfoS("Could not set config flag", "error", err)
				} else {
					klog.InfoS("Using cluster TLS profile for metrics server", "configFile", configFile)
				}
			}
		}
		originalRun(cmd, args)
	}

	return cmd
}

// createTLSConfigFileFromFlags creates a config file from CLI flags injected by operator.
// The operator serializes TLS config into --tls-min-version and --tls-cipher-suites
// per OCPSTRAT-2611 (components don't read from the API directly).
func createTLSConfigFileFromFlags() (string, error) {
	klog.InfoS("Daemonset metrics server TLS configuration from operator",
		"minVersion", tlsMinVersion,
		"cipherSuites", tlsCipherSuites)

	// Create GenericOperatorConfig with TLS settings from CLI flags
	config := &operatorv1alpha1.GenericOperatorConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "operator.openshift.io/v1alpha1",
			Kind:       "GenericOperatorConfig",
		},
		ServingInfo: configv1.HTTPServingInfo{
			ServingInfo: configv1.ServingInfo{
				MinTLSVersion: tlsMinVersion,
				CipherSuites:  tlsCipherSuites,
			},
		},
	}

	// Write to temp file
	tmpDir := os.TempDir()
	configFile := filepath.Join(tmpDir, "daemonset-config.json")

	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return "", err
	}

	return configFile, nil
}
