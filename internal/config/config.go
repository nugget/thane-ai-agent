// Package config handles Thane configuration loading.
package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all Thane configuration.
type Config struct {
	Listen        ListenConfig        `yaml:"listen"`
	HomeAssistant HomeAssistantConfig `yaml:"homeassistant"`
	Models        ModelsConfig        `yaml:"models"`
	DataDir       string              `yaml:"data_dir"`
}

// ListenConfig defines the API server settings.
type ListenConfig struct {
	Port int `yaml:"port"`
}

// HomeAssistantConfig defines HA connection settings.
type HomeAssistantConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// ModelsConfig defines model routing settings.
type ModelsConfig struct {
	Default   string `yaml:"default"`
	OllamaURL string `yaml:"ollama_url"`
}

// Load reads configuration from a YAML file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	cfg := &Config{
		Listen: ListenConfig{Port: 8080},
	}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Default returns a default configuration.
func Default() *Config {
	return &Config{
		Listen: ListenConfig{Port: 8080},
		Models: ModelsConfig{Default: "ollama/llama3:8b"},
	}
}
