package tlsconfig

import (
	"context"
	"crypto/tls"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/library-go/pkg/crypto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sapiflag "k8s.io/component-base/cli/flag"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	// APIServerInstanceName is the singleton name for APIServer configuration
	APIServerInstanceName = "cluster"
)

var (
	// DefaultTLSCiphers from Intermediate profile
	DefaultTLSCiphers = configv1.TLSProfiles[configv1.TLSProfileIntermediateType].Ciphers
	// DefaultMinTLSVersion from Intermediate profile
	DefaultMinTLSVersion = configv1.TLSProfiles[configv1.TLSProfileIntermediateType].MinTLSVersion
)

// TLSConfig holds the resolved TLS configuration
type TLSConfig struct {
	MinTLSVersion    string
	CipherSuites     []string // OpenSSL names
	CipherSuitesIANA []string // IANA names for Go
	Profile          *configv1.TLSSecurityProfile
}

// GetClusterTLSProfile fetches the cluster-wide TLS profile from APIServer
func GetClusterTLSProfile(ctx context.Context, cfg *rest.Config) *configv1.TLSSecurityProfile {
	client, err := configclient.NewForConfig(cfg)
	if err != nil {
		klog.V(4).InfoS("Could not create config client", "error", err)
		return nil
	}

	apiServer, err := client.ConfigV1().APIServers().Get(ctx, APIServerInstanceName, metav1.GetOptions{})
	if err != nil {
		klog.V(4).InfoS("Could not fetch APIServer TLS profile, using default", "error", err)
		return nil
	}
	return apiServer.Spec.TLSSecurityProfile
}

// ResolveTLSConfig resolves the effective TLS configuration from cluster profile.
// Falls back to Intermediate profile if clusterProfile is nil.
func ResolveTLSConfig(clusterProfile *configv1.TLSSecurityProfile) *TLSConfig {
	minVersion, ciphers := getSecurityProfileCiphers(clusterProfile)
	ciphersIANA := crypto.OpenSSLToIANACipherSuites(ciphers)

	return &TLSConfig{
		MinTLSVersion:    minVersion,
		CipherSuites:     ciphers,
		CipherSuitesIANA: ciphersIANA,
		Profile:          clusterProfile,
	}
}

// getSecurityProfileCiphers extracts TLS settings from a profile.
// Returns Intermediate profile settings if profile is nil.
func getSecurityProfileCiphers(profile *configv1.TLSSecurityProfile) (string, []string) {
	if profile == nil {
		profileSpec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		return string(profileSpec.MinTLSVersion), profileSpec.Ciphers
	}

	var profileSpec *configv1.TLSProfileSpec
	if profile.Type == configv1.TLSProfileCustomType {
		if profile.Custom != nil {
			profileSpec = &profile.Custom.TLSProfileSpec
		}
	} else {
		profileSpec = configv1.TLSProfiles[profile.Type]
	}

	// Fallback to Intermediate if nothing found
	if profileSpec == nil {
		profileSpec = configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}

	return string(profileSpec.MinTLSVersion), profileSpec.Ciphers
}

// GetGoTLSConfig converts TLSConfig to a *tls.Config with explicit settings
// per OCPSTRAT-2611 (no reliance on Go defaults)
func (c *TLSConfig) GetGoTLSConfig() (*tls.Config, error) {
	minVersionID, err := k8sapiflag.TLSVersion(c.MinTLSVersion)
	if err != nil {
		klog.Warningf("Invalid TLS min version %s, using TLS 1.2: %v", c.MinTLSVersion, err)
		minVersionID = tls.VersionTLS12
	}

	cipherSuiteIDs, err := k8sapiflag.TLSCipherSuites(c.CipherSuitesIANA)
	if err != nil || len(cipherSuiteIDs) == 0 {
		klog.Warningf("Invalid cipher suites, using defaults: %v", err)
		cipherSuiteIDs, _ = k8sapiflag.TLSCipherSuites(
			crypto.OpenSSLToIANACipherSuites(DefaultTLSCiphers),
		)
	}

	config := &tls.Config{
		MinVersion:   minVersionID,
		CipherSuites: cipherSuiteIDs,
	}

	return config, nil
}

// ProfileDescription returns a human-readable description of the active profile
func (c *TLSConfig) ProfileDescription() string {
	if c.Profile == nil {
		return "Intermediate (default)"
	}
	return string(c.Profile.Type)
}
