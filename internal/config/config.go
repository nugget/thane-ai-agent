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

	paths = append(paths, "/config/config.yaml") // Container convention
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

// Configured reports whether the Home Assistant connection has both a
// URL and a token. A partial configuration (URL without token or vice
// versa) is treated as unconfigured.
func (c HomeAssistantConfig) Configured() bool {
	return c.URL != "" && c.Token != ""
}

// Configured reports whether an Anthropic API key is present.
func (c AnthropicConfig) Configured() bool {
	return c.APIKey != ""
}

// Load reads configuration from a YAML file, expands environment
// variables, applies defaults for any unset fields, and validates
// the result. After Load returns successfully, all fields are usable
// without additional nil/empty checks.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variables (e.g., ${HOME}, ${ANTHROPIC_API_KEY}).
	// This is a convenience for container deployments; the recommended
	// approach is to put values directly in the config file.
	expanded := os.ExpandEnv(string(data))

	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, err
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// applyDefaults fills in zero-value fields with sensible defaults.
// Called automatically by Load. After this, callers can read any field
// without checking for empty strings or zero values.
func (c *Config) applyDefaults() {
	if c.Listen.Port == 0 {
		c.Listen.Port = 8080
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.TalentsDir == "" {
		c.TalentsDir = "./talents"
	}
	if c.Models.OllamaURL == "" {
		c.Models.OllamaURL = "http://localhost:11434"
	}
	if c.OllamaAPI.Port == 0 {
		c.OllamaAPI.Port = 11434
	}
	if c.Embeddings.Model == "" {
		c.Embeddings.Model = "nomic-embed-text"
	}
	if c.Embeddings.BaseURL == "" {
		c.Embeddings.BaseURL = c.Models.OllamaURL
	}
	if c.ShellExec.DefaultTimeoutSec == 0 {
		c.ShellExec.DefaultTimeoutSec = 30
	}

	// Ensure each model has a provider (default: ollama)
	for i := range c.Models.Available {
		if c.Models.Available[i].Provider == "" {
			c.Models.Available[i].Provider = "ollama"
		}
	}
}

// Validate checks that the configuration is internally consistent.
// It runs after applyDefaults, so it can assume defaults are populated.
// Returns an error describing the first problem found, or nil.
func (c *Config) Validate() error {
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("listen.port %d out of range (1-65535)", c.Listen.Port)
	}
	if c.OllamaAPI.Enabled && (c.OllamaAPI.Port < 1 || c.OllamaAPI.Port > 65535) {
		return fmt.Errorf("ollama_api.port %d out of range (1-65535)", c.OllamaAPI.Port)
	}
	if c.LogLevel != "" {
		if _, err := ParseLogLevel(c.LogLevel); err != nil {
			return err
		}
	}
	return nil
}

// ContextWindowForModel returns the context window size for the named
// model, or defaultSize if the model is not found in the configuration.
func (c *Config) ContextWindowForModel(name string, defaultSize int) int {
	for _, m := range c.Models.Available {
		if m.Name == name {
			return m.ContextWindow
		}
	}
	return defaultSize
}

// Default returns a default configuration suitable for local development
// with Ollama. All defaults are already applied.
func Default() *Config {
	cfg := &Config{
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
	cfg.applyDefaults()
	return cfg
}
