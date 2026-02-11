// Package config handles Thane configuration loading.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultSearchPaths returns the config file search order.
// An explicit path (from -config flag) is checked first.
// Then: ./config.yaml, ~/.config/thane/config.yaml, /etc/thane/config.yaml.
func DefaultSearchPaths() []string {
	paths := []string{"config.yaml"}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "thane", "config.yaml"))
	}

	paths = append(paths, "/etc/thane/config.yaml")
	return paths
}

// FindConfig locates a config file. If explicit is non-empty, it must exist.
// Otherwise, searches DefaultSearchPaths and returns the first that exists.
// Returns the path found, or an error if nothing was found.
func FindConfig(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file not found: %s", explicit)
		}
		return explicit, nil
	}

	for _, p := range DefaultSearchPaths() {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("no config file found (searched: %v)", DefaultSearchPaths())
}

// Config holds all Thane configuration.
type Config struct {
	Listen        ListenConfig        `yaml:"listen"`
	OllamaAPI     OllamaAPIConfig     `yaml:"ollama_api"`
	HomeAssistant HomeAssistantConfig `yaml:"homeassistant"`
	Models        ModelsConfig        `yaml:"models"`
	Anthropic     AnthropicConfig     `yaml:"anthropic"`
	Embeddings    EmbeddingsConfig    `yaml:"embeddings"`
	Workspace     WorkspaceConfig     `yaml:"workspace"`
	ShellExec     ShellExecConfig     `yaml:"shell_exec"`
	DataDir       string              `yaml:"data_dir"`
	TalentsDir    string              `yaml:"talents_dir"`
	PersonaFile   string              `yaml:"persona_file"`
	LogLevel      string              `yaml:"log_level"`
}

// AnthropicConfig defines Anthropic API settings.
type AnthropicConfig struct {
	APIKey string `yaml:"api_key"`
}

// WorkspaceConfig defines the agent's workspace for file operations.
type WorkspaceConfig struct {
	// Path is the root directory for file operations.
	// All file tool paths are relative to this directory.
	// If empty, file tools are disabled.
	Path string `yaml:"path"`
	// ReadOnlyDirs are additional directories the agent can read but not write.
	ReadOnlyDirs []string `yaml:"read_only_dirs"`
}

// ShellExecConfig defines shell execution capabilities.
type ShellExecConfig struct {
	// Enabled allows shell command execution. Disabled by default for safety.
	Enabled bool `yaml:"enabled"`
	// WorkingDir sets the default working directory for commands.
	WorkingDir string `yaml:"working_dir"`
	// DeniedPatterns are command patterns to block (e.g., "rm -rf /").
	DeniedPatterns []string `yaml:"denied_patterns"`
	// AllowedPrefixes limits commands to those starting with these prefixes.
	// Empty means all commands are allowed (subject to denied patterns).
	AllowedPrefixes []string `yaml:"allowed_prefixes"`
	// DefaultTimeoutSec is the default timeout in seconds (default 30).
	DefaultTimeoutSec int `yaml:"default_timeout_sec"`
}

// OllamaAPIConfig defines the optional Ollama-compatible API server.
// When enabled, Thane exposes an Ollama-compatible API on a separate port
// for Home Assistant integration.
type OllamaAPIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // Bind address (default: "" = all interfaces)
	Port    int    `yaml:"port"`    // Default: 11434
}

// EmbeddingsConfig defines embedding generation settings.
type EmbeddingsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`   // Embedding model name (e.g., nomic-embed-text)
	BaseURL string `yaml:"baseurl"` // Ollama URL (defaults to models.ollama_url)
}

// ListenConfig defines the API server settings.
type ListenConfig struct {
	Address string `yaml:"address"` // Bind address (default: "" = all interfaces)
	Port    int    `yaml:"port"`
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
	Provider      string `yaml:"provider"` // ollama, anthropic, openai
	SupportsTools bool   `yaml:"supports_tools"`
	ContextWindow int    `yaml:"context_window"`
	Speed         int    `yaml:"speed"`          // 1-10
	Quality       int    `yaml:"quality"`        // 1-10
	CostTier      int    `yaml:"cost_tier"`      // 0=local, 1=cheap, 2=moderate, 3=expensive
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
