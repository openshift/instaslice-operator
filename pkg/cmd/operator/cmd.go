package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/instaslice-operator/pkg/operator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/openshift/instaslice-operator/pkg/tlsconfig"
	"github.com/openshift/instaslice-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

func NewOperator(ctx context.Context) *cobra.Command {
	cfg := controllercmd.NewControllerCommandConfig("instaslice-operator", version.Get(), operator.RunOperator, clock.RealClock{})

	cmd := cfg.NewCommandWithContext(ctx)
	cmd.Use = "operator"
	cmd.Short = "Start the Cluster Instaslice Operator"

	// Wrap the Run function to inject TLS config
	originalRun := cmd.Run
	cmd.Run = func(cmd *cobra.Command, args []string) {
		// Only inject config if --config flag is not already set
		configFlag := cmd.Flag("config")
		if configFlag == nil || configFlag.Value.String() == "" {
			configFile, err := createTLSConfigFile(ctx)
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

// createTLSConfigFile fetches the cluster TLS profile and creates a temp config file
func createTLSConfigFile(ctx context.Context) (string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return "", err
	}

	clusterProfile := tlsconfig.GetClusterTLSProfile(ctx, cfg)
	tlsCfg := tlsconfig.ResolveTLSConfig(clusterProfile)

	klog.InfoS("Operator metrics server TLS profile",
		"profile", tlsCfg.ProfileDescription(),
		"minVersion", tlsCfg.MinTLSVersion,
		"cipherSuites", tlsCfg.CipherSuitesIANA)

	// Create GenericOperatorConfig with TLS settings
	config := &operatorv1alpha1.GenericOperatorConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "operator.openshift.io/v1alpha1",
			Kind:       "GenericOperatorConfig",
		},
		ServingInfo: configv1.HTTPServingInfo{
			ServingInfo: configv1.ServingInfo{
				MinTLSVersion: tlsCfg.MinTLSVersion,
				CipherSuites:  tlsCfg.CipherSuitesIANA,
			},
		},
	}

	// Write to temp file
	tmpDir := os.TempDir()
	configFile := filepath.Join(tmpDir, "operator-config.json")

	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return "", err
	}

	return configFile, nil
}
