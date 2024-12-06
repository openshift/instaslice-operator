package config

import (
	"os"
	"strings"
)

const (
	DefaultEmulatorMode = false
	DefaultWebhookMode  = true
	// TODO fix this image
	DefaultDaemonsetImage = "quay.io/amalvank/instaslicev2-daemonset:latest"
)

type Config struct {
	// EmulatorMode enable emulation mode
	EmulatorModeEnable bool

	// Webhook enable webhook endpoint
	WebhookEnable bool

	// DaemonsetImage the daemonset image to use
	DaemonsetImage string
}

func NewConfig() *Config {
	return &Config{
		EmulatorModeEnable: DefaultEmulatorMode,
		WebhookEnable:      DefaultEmulatorMode,
		DaemonsetImage:     DefaultDaemonsetImage,
	}
}

func ConfigFromEnvironment() *Config {
	config := NewConfig()

	if emulatorMode, ok := os.LookupEnv("EMULATOR_MODE"); ok {
		config.EmulatorModeEnable = strings.EqualFold(emulatorMode, "true")
	}

	if webhookEnable, ok := os.LookupEnv("WEBHOOK_ENABLE"); ok {
		config.WebhookEnable = strings.EqualFold(webhookEnable, "true")
	}

	if daemonsetImage, ok := os.LookupEnv("RELATED_IMAGE_INSTASLICE_DAEMONSET"); ok {
		config.DaemonsetImage = daemonsetImage
	}

	return config
}
