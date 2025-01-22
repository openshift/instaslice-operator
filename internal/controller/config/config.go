package config

import (
	"encoding/json"
	"os"
	"strings"
)

const (
	DefaultEmulatorMode = false
	DefaultWebhookMode  = true
	// TODO fix this image
	DefaultDaemonsetImage    = "quay.io/amalvank/instaslicev2-daemonset:latest"
	DefaultManifestConfigDir = "/config"
)

type Config struct {
	// EmulatorMode enable emulation mode
	EmulatorModeEnable bool `json:"emulator_mode_enable"`

	// Webhook enable webhook endpoint
	WebhookEnable bool `json:"webhook_enable"`

	// DaemonsetImage the daemonset image to use
	DaemonsetImage string `json:"daemonset_image"`

	// ManifestConfigDir manifest directory
	ManifestConfigDir string `json:"manifest_config_dir"`
}

func NewConfig() *Config {
	return &Config{
		EmulatorModeEnable: DefaultEmulatorMode,
		WebhookEnable:      DefaultWebhookMode,
		DaemonsetImage:     DefaultDaemonsetImage,
		ManifestConfigDir:  DefaultManifestConfigDir,
	}
}

func (c *Config) ToString() string {
	bytes, _ := json.Marshal(*c)
	return string(bytes)
}

func ConfigFromEnvironment() *Config {
	config := NewConfig()

	if emulatorMode, ok := os.LookupEnv("EMULATOR_MODE"); ok {
		config.EmulatorModeEnable = strings.EqualFold(emulatorMode, "true")
	}

	if webhookEnable, ok := os.LookupEnv("WEBHOOK_ENABLE"); ok {
		config.WebhookEnable = webhookEnable != "false"
	}

	if daemonsetImage, ok := os.LookupEnv("RELATED_IMAGE_INSTASLICE_DAEMONSET"); ok {
		config.DaemonsetImage = daemonsetImage
	}

	if manifestConfigDir, ok := os.LookupEnv("MANIFEST_CONFIG_DIR"); ok {
		config.ManifestConfigDir = manifestConfigDir
	}

	return config
}
