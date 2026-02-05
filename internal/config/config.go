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
	TalentsDir    string              `yaml:"talents_dir"`
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
	Default    string        `yaml:"default"`
	OllamaURL  string        `yaml:"ollama_url"`
	LocalFirst bool          `yaml:"local_first"`
	Available  []ModelConfig `yaml:"available"`
}

// ModelConfig defines a single model's capabilities.
type ModelConfig struct {
	Name          string `yaml:"name"`
	Provider      string `yaml:"provider"`      // ollama, anthropic, openai
	SupportsTools bool   `yaml:"supports_tools"`
	ContextWindow int    `yaml:"context_window"`
	Speed         int    `yaml:"speed"`         // 1-10
	Quality       int    `yaml:"quality"`       // 1-10
	CostTier      int    `yaml:"cost_tier"`     // 0=local, 1=cheap, 2=moderate, 3=expensive
	MinComplexity string `yaml:"min_complexity"` // simple, moderate, complex
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
		Models: ModelsConfig{
			Default:    "qwen3:4b",
			LocalFirst: true,
			Available: []ModelConfig{
				{
					Name:          "qwen3:4b",
					Provider:      "ollama",
					SupportsTools: true,
					ContextWindow: 4096,
					Speed:         9,
					Quality:       5,
					CostTier:      0,
					MinComplexity: "simple",
				},
				{
					Name:          "qwen2.5:72b",
					Provider:      "ollama",
					SupportsTools: true,
					ContextWindow: 32768,
					Speed:         4,
					Quality:       8,
					CostTier:      0,
					MinComplexity: "moderate",
				},
			},
		},
	}
}
