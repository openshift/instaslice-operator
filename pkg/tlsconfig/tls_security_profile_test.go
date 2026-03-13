package tlsconfig

import (
	"crypto/tls"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestResolveTLSConfig_NilProfile(t *testing.T) {
	cfg := ResolveTLSConfig(nil)

	if cfg.MinTLSVersion != string(configv1.VersionTLS12) {
		t.Errorf("Expected MinTLSVersion to be %s, got %s", configv1.VersionTLS12, cfg.MinTLSVersion)
	}

	if len(cfg.CipherSuites) == 0 {
		t.Error("Expected CipherSuites to be non-empty for nil profile")
	}

	if cfg.ProfileDescription() != "Intermediate (default)" {
		t.Errorf("Expected ProfileDescription to be 'Intermediate (default)', got %s", cfg.ProfileDescription())
	}
}

func TestResolveTLSConfig_IntermediateProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileIntermediateType,
	}

	cfg := ResolveTLSConfig(profile)

	if cfg.MinTLSVersion != string(configv1.VersionTLS12) {
		t.Errorf("Expected MinTLSVersion to be %s, got %s", configv1.VersionTLS12, cfg.MinTLSVersion)
	}

	if len(cfg.CipherSuites) == 0 {
		t.Error("Expected CipherSuites to be non-empty for Intermediate profile")
	}

	if cfg.ProfileDescription() != string(configv1.TLSProfileIntermediateType) {
		t.Errorf("Expected ProfileDescription to be 'Intermediate', got %s", cfg.ProfileDescription())
	}
}

func TestResolveTLSConfig_ModernProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileModernType,
	}

	cfg := ResolveTLSConfig(profile)

	if cfg.MinTLSVersion != string(configv1.VersionTLS13) {
		t.Errorf("Expected MinTLSVersion to be %s, got %s", configv1.VersionTLS13, cfg.MinTLSVersion)
	}

	if cfg.ProfileDescription() != string(configv1.TLSProfileModernType) {
		t.Errorf("Expected ProfileDescription to be 'Modern', got %s", cfg.ProfileDescription())
	}
}

func TestResolveTLSConfig_OldProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileOldType,
	}

	cfg := ResolveTLSConfig(profile)

	if cfg.MinTLSVersion != string(configv1.VersionTLS10) {
		t.Errorf("Expected MinTLSVersion to be %s, got %s", configv1.VersionTLS10, cfg.MinTLSVersion)
	}

	if len(cfg.CipherSuites) == 0 {
		t.Error("Expected CipherSuites to be non-empty for Old profile")
	}

	if cfg.ProfileDescription() != string(configv1.TLSProfileOldType) {
		t.Errorf("Expected ProfileDescription to be 'Old', got %s", cfg.ProfileDescription())
	}
}

func TestResolveTLSConfig_CustomProfile(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
				Ciphers: []string{
					"TLS_AES_128_GCM_SHA256",
					"TLS_AES_256_GCM_SHA384",
				},
			},
		},
	}

	cfg := ResolveTLSConfig(profile)

	if cfg.MinTLSVersion != string(configv1.VersionTLS12) {
		t.Errorf("Expected MinTLSVersion to be %s, got %s", configv1.VersionTLS12, cfg.MinTLSVersion)
	}

	if len(cfg.CipherSuites) != 2 {
		t.Errorf("Expected 2 cipher suites, got %d", len(cfg.CipherSuites))
	}

	if cfg.ProfileDescription() != string(configv1.TLSProfileCustomType) {
		t.Errorf("Expected ProfileDescription to be 'Custom', got %s", cfg.ProfileDescription())
	}
}

func TestResolveTLSConfig_CustomProfileNilSpec(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type:   configv1.TLSProfileCustomType,
		Custom: nil,
	}

	cfg := ResolveTLSConfig(profile)

	// Should fall back to Intermediate
	if cfg.MinTLSVersion != string(configv1.VersionTLS12) {
		t.Errorf("Expected MinTLSVersion to be %s (fallback to Intermediate), got %s", configv1.VersionTLS12, cfg.MinTLSVersion)
	}
}

func TestGetGoTLSConfig(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileIntermediateType,
	}

	cfg := ResolveTLSConfig(profile)
	goTLSConfig, err := cfg.GetGoTLSConfig()

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if goTLSConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("Expected Go TLS MinVersion to be TLS 1.2 (%d), got %d", tls.VersionTLS12, goTLSConfig.MinVersion)
	}

	if len(goTLSConfig.CipherSuites) == 0 {
		t.Error("Expected CipherSuites to be non-empty")
	}
}

func TestGetGoTLSConfig_InvalidMinVersion(t *testing.T) {
	cfg := &TLSConfig{
		MinTLSVersion:    "InvalidVersion",
		CipherSuites:     []string{"TLS_AES_128_GCM_SHA256"},
		CipherSuitesIANA: []string{"TLS_AES_128_GCM_SHA256"},
	}

	goTLSConfig, err := cfg.GetGoTLSConfig()

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should fall back to TLS 1.2
	if goTLSConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("Expected Go TLS MinVersion to fall back to TLS 1.2 (%d), got %d", tls.VersionTLS12, goTLSConfig.MinVersion)
	}
}

func TestProfileDescription(t *testing.T) {
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
			name:     "Intermediate profile",
			profile:  &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType},
			expected: "Intermediate",
		},
		{
			name:     "Modern profile",
			profile:  &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expected: "Modern",
		},
		{
			name:     "Old profile",
			profile:  &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expected: "Old",
		},
		{
			name:     "Custom profile",
			profile:  &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expected: "Custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ResolveTLSConfig(tt.profile)
			if cfg.ProfileDescription() != tt.expected {
				t.Errorf("Expected ProfileDescription to be %s, got %s", tt.expected, cfg.ProfileDescription())
			}
		})
	}
}
