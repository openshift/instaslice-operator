package tlsobserver

import (
	"crypto/tls"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestResolveTLSConfig_NilProfile(t *testing.T) {
	config := resolveTLSConfig(nil)

	if config.MinTLSVersion != "VersionTLS12" {
		t.Errorf("Expected MinTLSVersion VersionTLS12, got %s", config.MinTLSVersion)
	}

	if len(config.CipherSuites) == 0 {
		t.Error("Expected cipher suites to be set")
	}

	if len(config.CipherSuitesIANA) == 0 {
		t.Error("Expected IANA cipher suites to be set")
	}

	if config.Profile != nil {
		t.Error("Expected Profile to be nil for default config")
	}
}

func TestResolveTLSConfig_IntermediateProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileIntermediateType,
	}

	config := resolveTLSConfig(profile)

	if config.MinTLSVersion != "VersionTLS12" {
		t.Errorf("Expected MinTLSVersion VersionTLS12, got %s", config.MinTLSVersion)
	}

	if config.Profile.Type != configv1.TLSProfileIntermediateType {
		t.Errorf("Expected profile type Intermediate, got %s", config.Profile.Type)
	}
}

func TestResolveTLSConfig_ModernProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileModernType,
	}

	config := resolveTLSConfig(profile)

	if config.MinTLSVersion != "VersionTLS13" {
		t.Errorf("Expected MinTLSVersion VersionTLS13, got %s", config.MinTLSVersion)
	}

	// Modern profile should have fewer ciphers (TLS 1.3 only)
	if len(config.CipherSuites) == 0 {
		t.Error("Expected cipher suites to be set")
	}

	if config.Profile.Type != configv1.TLSProfileModernType {
		t.Errorf("Expected profile type Modern, got %s", config.Profile.Type)
	}
}

func TestResolveTLSConfig_OldProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileOldType,
	}

	config := resolveTLSConfig(profile)

	if config.MinTLSVersion != "VersionTLS10" {
		t.Errorf("Expected MinTLSVersion VersionTLS10, got %s", config.MinTLSVersion)
	}

	// Old profile should have more ciphers for legacy compatibility
	if len(config.CipherSuites) == 0 {
		t.Error("Expected cipher suites to be set")
	}

	if config.Profile.Type != configv1.TLSProfileOldType {
		t.Errorf("Expected profile type Old, got %s", config.Profile.Type)
	}
}

func TestResolveTLSConfig_CustomProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
				Ciphers: []string{
					"ECDHE-RSA-AES128-GCM-SHA256",
					"ECDHE-RSA-AES256-GCM-SHA384",
				},
			},
		},
	}

	config := resolveTLSConfig(profile)

	if config.MinTLSVersion != "VersionTLS12" {
		t.Errorf("Expected MinTLSVersion VersionTLS12, got %s", config.MinTLSVersion)
	}

	if len(config.CipherSuites) != 2 {
		t.Errorf("Expected 2 cipher suites, got %d", len(config.CipherSuites))
	}

	if config.Profile.Type != configv1.TLSProfileCustomType {
		t.Errorf("Expected profile type Custom, got %s", config.Profile.Type)
	}
}

func TestResolveTLSConfig_CustomProfileNilSpec(t *testing.T) {
	// Custom type but nil custom spec should fallback to Intermediate
	profile := &configv1.TLSSecurityProfile{
		Type:   configv1.TLSProfileCustomType,
		Custom: nil,
	}

	config := resolveTLSConfig(profile)

	// Should fallback to Intermediate
	if config.MinTLSVersion != "VersionTLS12" {
		t.Errorf("Expected MinTLSVersion VersionTLS12 (fallback), got %s", config.MinTLSVersion)
	}
}

func TestTLSConfig_GetGoTLSConfig(t *testing.T) {
	config := resolveTLSConfig(nil)

	goConfig, err := config.GetGoTLSConfig()
	if err != nil {
		t.Fatalf("GetGoTLSConfig failed: %v", err)
	}

	if goConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("Expected MinVersion TLS 1.2 (%d), got %d", tls.VersionTLS12, goConfig.MinVersion)
	}

	if len(goConfig.CipherSuites) == 0 {
		t.Error("Expected cipher suites to be set")
	}
}

func TestTLSConfig_GetGoTLSConfig_Modern(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileModernType,
	}

	config := resolveTLSConfig(profile)

	goConfig, err := config.GetGoTLSConfig()
	if err != nil {
		t.Fatalf("GetGoTLSConfig failed: %v", err)
	}

	if goConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("Expected MinVersion TLS 1.3 (%d), got %d", tls.VersionTLS13, goConfig.MinVersion)
	}
}

func TestTLSConfig_ProfileDescription(t *testing.T) {
	tests := []struct {
		name     string
		profile  *configv1.TLSSecurityProfile
		expected string
	}{
		{
			name:     "nil profile",
			profile:  nil,
			expected: "Intermediate (default)",
		},
		{
			name: "Intermediate profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			expected: "Intermediate",
		},
		{
			name: "Modern profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			expected: "Modern",
		},
		{
			name: "Old profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
			},
			expected: "Old",
		},
		{
			name: "Custom profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
			},
			expected: "Custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := resolveTLSConfig(tt.profile)
			if config.ProfileDescription() != tt.expected {
				t.Errorf("Expected ProfileDescription %s, got %s", tt.expected, config.ProfileDescription())
			}
		})
	}
}

func TestGetSecurityProfileCiphers_AllProfiles(t *testing.T) {
	tests := []struct {
		name              string
		profile           *configv1.TLSSecurityProfile
		expectedMinTLS    string
		expectedMinCipher int // minimum number of ciphers expected
	}{
		{
			name:              "nil profile",
			profile:           nil,
			expectedMinTLS:    "VersionTLS12",
			expectedMinCipher: 5,
		},
		{
			name: "Intermediate",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			expectedMinTLS:    "VersionTLS12",
			expectedMinCipher: 5,
		},
		{
			name: "Modern",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			expectedMinTLS:    "VersionTLS13",
			expectedMinCipher: 1,
		},
		{
			name: "Old",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
			},
			expectedMinTLS:    "VersionTLS10",
			expectedMinCipher: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minTLS, ciphers := getSecurityProfileCiphers(tt.profile)
			if minTLS != tt.expectedMinTLS {
				t.Errorf("Expected minTLS %s, got %s", tt.expectedMinTLS, minTLS)
			}
			if len(ciphers) < tt.expectedMinCipher {
				t.Errorf("Expected at least %d ciphers, got %d", tt.expectedMinCipher, len(ciphers))
			}
		})
	}
}
