// Package config handles loading, defaulting, and validating Thane's
// YAML configuration.
//
// Configuration is loaded from a single YAML file located via
// [FindConfig]. After [Load] returns, all fields carry usable values —
// callers never need empty-string checks or fallback logic. The load
// pipeline is:
//
//  1. Read the file and expand environment variables ([os.ExpandEnv]).
//  2. Unmarshal YAML into a [Config] struct.
//  3. Apply sensible defaults for any unset fields ([Config.applyDefaults]).
//  4. Validate internal consistency ([Config.Validate]).
//
// Secrets (API keys, tokens) can be written directly in the config file.
// Protect the file with appropriate permissions (chmod 600). Environment
// variable expansion is available as a convenience for container and
// 12-factor deployments but is not the recommended default.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nugget/thane-ai-agent/internal/search"
	"gopkg.in/yaml.v3"
)

// DefaultSearchPaths returns the ordered list of paths that [FindConfig]
// checks when no explicit path is provided. The first existing file wins.
//
// The search order is:
//   - ./config.yaml (project directory / working directory)
//   - ~/Thane/config.yaml (macOS role account convention)
//   - ~/.config/thane/config.yaml (XDG user config)
//   - /config/config.yaml (container convention)
//   - /usr/local/etc/thane/config.yaml (macOS/BSD local sysconfig)
//   - /etc/thane/config.yaml (system-wide)
func DefaultSearchPaths() []string {
	paths := []string{"config.yaml"}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "Thane", "config.yaml"))
		paths = append(paths, filepath.Join(home, ".config", "thane", "config.yaml"))
	}

	paths = append(paths, "/config/config.yaml")
	paths = append(paths, "/usr/local/etc/thane/config.yaml")
	paths = append(paths, "/etc/thane/config.yaml")
	return paths
}

// FindConfig locates a configuration file. If explicit is non-empty, that
// exact path must exist or an error is returned. Otherwise, the paths from
// [DefaultSearchPaths] are tried in order, and the first that exists is
// returned. Returns an error if no config file can be found.
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

// Config is the top-level configuration for the Thane agent. It maps
// directly to the YAML config file structure.
type Config struct {
	// Listen configures the primary HTTP API server (OpenAI-compatible).
	Listen ListenConfig `yaml:"listen"`

	// OllamaAPI configures the optional Ollama-compatible API server,
	// used for Home Assistant integration.
	OllamaAPI OllamaAPIConfig `yaml:"ollama_api"`

	// HomeAssistant configures the connection to a Home Assistant instance.
	HomeAssistant HomeAssistantConfig `yaml:"homeassistant"`

	// Models configures LLM providers, model routing, and the default model.
	Models ModelsConfig `yaml:"models"`

	// Anthropic configures the Anthropic (Claude) API provider.
	Anthropic AnthropicConfig `yaml:"anthropic"`

	// Embeddings configures vector embedding generation for semantic search.
	Embeddings EmbeddingsConfig `yaml:"embeddings"`

	// Workspace configures the agent's sandboxed file system access.
	Workspace WorkspaceConfig `yaml:"workspace"`

	// ShellExec configures the agent's ability to run shell commands.
	ShellExec ShellExecConfig `yaml:"shell_exec"`

	// DataDir is the root directory for SQLite databases (memory, facts,
	// scheduler, checkpoints, and anticipations). Default: "./db".
	DataDir string `yaml:"data_dir"`

	// TalentsDir is the directory containing talent markdown files that
	// extend the system prompt. Default: "./talents".
	TalentsDir string `yaml:"talents_dir"`

	// PersonaFile is an optional markdown file that replaces the default
	// system prompt with a custom agent identity.
	PersonaFile string `yaml:"persona_file"`

	// Search configures web search providers.
	Search SearchConfig `yaml:"search"`

	// LogLevel sets the minimum log level. Valid values: trace, debug,
	// info, warn, error. Default: info. See [ParseLogLevel].
	LogLevel string `yaml:"log_level"`

	// LogFormat sets the log output format. Valid values: text, json.
	// Default: text. Text is human-readable; JSON enables structured
	// log aggregation (Loki, Datadog, jq, etc.).
	LogFormat string `yaml:"log_format"`
}

// ListenConfig configures an HTTP server's bind address and port.
type ListenConfig struct {
	// Address is the network address to bind to. Empty string means
	// all interfaces (0.0.0.0).
	Address string `yaml:"address"`

	// Port is the TCP port to listen on. Default: 8080.
	Port int `yaml:"port"`
}

// OllamaAPIConfig configures the optional Ollama-compatible API server.
// When Enabled is true, Thane exposes an additional HTTP server that
// speaks the Ollama wire protocol, allowing Home Assistant's built-in
// Ollama integration to use Thane as a drop-in backend.
type OllamaAPIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // Bind address; empty = all interfaces
	Port    int    `yaml:"port"`    // Default: 11434
}

// HomeAssistantConfig configures the connection to a Home Assistant
// instance. Both URL and Token must be set for the connection to be
// established; see [HomeAssistantConfig.Configured].
type HomeAssistantConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// Configured reports whether both URL and Token are set. A partial
// configuration (URL without token or vice versa) is treated as
// unconfigured — Thane will start without Home Assistant tools.
func (c HomeAssistantConfig) Configured() bool {
	return c.URL != "" && c.Token != ""
}

// AnthropicConfig configures the Anthropic (Claude) API provider.
type AnthropicConfig struct {
	APIKey string `yaml:"api_key"`
}

// Configured reports whether an Anthropic API key is present.
func (c AnthropicConfig) Configured() bool {
	return c.APIKey != ""
}

// ModelsConfig configures LLM model routing. Each model in the Available
// list is mapped to a provider; requests are routed based on the model
// name. Unknown models fall through to Ollama.
type ModelsConfig struct {
	// Default is the model name used when no specific model is requested.
	Default string `yaml:"default"`

	// OllamaURL is the base URL of the Ollama API server used as the
	// default LLM backend. Default: "http://localhost:11434".
	OllamaURL string `yaml:"ollama_url"`

	// LocalFirst prefers local (cost_tier=0) models over cloud models
	// when routing decisions are made by the model router.
	LocalFirst bool `yaml:"local_first"`

	// Available lists all models that Thane can route to. Each entry
	// maps a model name to a provider and declares its capabilities.
	Available []ModelConfig `yaml:"available"`
}

// ModelConfig describes a single LLM model's identity and capabilities.
// The model router uses these fields to select the best model for each
// request.
type ModelConfig struct {
	Name          string `yaml:"name"`           // Model identifier (e.g., "claude-opus-4-20250514")
	Provider      string `yaml:"provider"`       // Provider name: ollama, anthropic, openai. Default: ollama
	SupportsTools bool   `yaml:"supports_tools"` // Whether the model can invoke tool calls
	ContextWindow int    `yaml:"context_window"` // Maximum context length in tokens
	Speed         int    `yaml:"speed"`          // Relative speed rating, 1 (slow) to 10 (fast)
	Quality       int    `yaml:"quality"`        // Relative quality rating, 1 (low) to 10 (high)
	CostTier      int    `yaml:"cost_tier"`      // 0=local/free, 1=cheap, 2=moderate, 3=expensive
	MinComplexity string `yaml:"min_complexity"` // Minimum task complexity: simple, moderate, complex
}

// SearchConfig configures web search providers. At least one provider
// must be configured for the web_search tool to be available.
type SearchConfig struct {
	// Default is the provider name to use when the agent doesn't
	// specify one. If empty, the first configured provider is used.
	Default string `yaml:"default"`

	// SearXNG configures the self-hosted SearXNG meta-search provider.
	SearXNG search.SearXNGConfig `yaml:"searxng"`

	// Brave configures the Brave Search API provider.
	Brave search.BraveConfig `yaml:"brave"`
}

// Configured reports whether at least one search provider is configured.
func (c SearchConfig) Configured() bool {
	return c.SearXNG.Configured() || c.Brave.Configured()
}

// EmbeddingsConfig configures vector embedding generation for semantic
// search over the fact store. When Enabled is false, ingested facts are
// stored without embeddings and semantic search is unavailable.
type EmbeddingsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`   // Embedding model name. Default: "nomic-embed-text"
	BaseURL string `yaml:"baseurl"` // Ollama URL for embeddings. Default: models.ollama_url
}

// WorkspaceConfig configures the agent's sandboxed file system access.
// When Path is set, the agent can read and write files within that
// directory. All paths passed to file tools are resolved relative to
// Path and cannot escape it.
type WorkspaceConfig struct {
	// Path is the root directory for file operations. If empty, file
	// tools are disabled entirely.
	Path string `yaml:"path"`

	// ReadOnlyDirs are additional directories the agent can read from
	// but not write to. Useful for giving the agent access to reference
	// material outside its workspace.
	ReadOnlyDirs []string `yaml:"read_only_dirs"`
}

// ShellExecConfig configures the agent's ability to execute shell
// commands on the host. Disabled by default for safety. When enabled,
// commands are filtered through allow and deny lists before execution.
type ShellExecConfig struct {
	// Enabled must be true for the agent to execute any shell commands.
	Enabled bool `yaml:"enabled"`

	// WorkingDir is the working directory for command execution. If
	// empty, the process's current directory is used.
	WorkingDir string `yaml:"working_dir"`

	// DeniedPatterns are substrings that cause a command to be rejected.
	// Checked before AllowedPrefixes. Example: "rm -rf /".
	DeniedPatterns []string `yaml:"denied_patterns"`

	// AllowedPrefixes restricts commands to those whose first token
	// matches one of these prefixes. An empty list means all commands
	// are allowed (subject to DeniedPatterns).
	AllowedPrefixes []string `yaml:"allowed_prefixes"`

	// DefaultTimeoutSec is the maximum wall-clock time a command may
	// run before being killed. Default: 30.
	DefaultTimeoutSec int `yaml:"default_timeout_sec"`
}

// Load reads a YAML configuration file, expands environment variables,
// applies defaults for any unset fields, and validates the result.
//
// After [Load] returns a non-nil [Config], every field is usable without
// additional nil or empty-string checks. The load pipeline is:
//
//  1. Read the file.
//  2. Expand environment variables (e.g., ${HOME}, ${ANTHROPIC_API_KEY}).
//  3. Unmarshal YAML into a [Config].
//  4. Apply defaults via [Config.applyDefaults].
//  5. Validate via [Config.Validate].
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

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

// applyDefaults fills zero-value fields with sensible defaults. It is
// called automatically by [Load] and [Default]. After this method
// returns, callers can read any field without conditional fallbacks.
//
// Cross-field defaults are resolved here too — for example,
// Embeddings.BaseURL defaults to Models.OllamaURL when unset.
func (c *Config) applyDefaults() {
	if c.LogFormat == "" {
		c.LogFormat = "text"
	}
	if c.Listen.Port == 0 {
		c.Listen.Port = 8080
	}
	if c.DataDir == "" {
		c.DataDir = "./db"
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

	for i := range c.Models.Available {
		if c.Models.Available[i].Provider == "" {
			c.Models.Available[i].Provider = "ollama"
		}
	}
}

// Validate checks that the configuration is internally consistent after
// defaults have been applied. It returns an error describing the first
// problem found, or nil if the configuration is valid.
//
// Validation checks include port ranges and log level syntax. It does
// not check reachability of external services (that happens at runtime).
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
	switch c.LogFormat {
	case "text", "json", "":
		// valid
	default:
		return fmt.Errorf("log_format %q invalid (expected text or json)", c.LogFormat)
	}
	return nil
}

// ContextWindowForModel returns the configured context window size for
// the named model. If the model is not found in [ModelsConfig.Available],
// defaultSize is returned. This avoids the need for callers to loop over
// the model list themselves.
func (c *Config) ContextWindowForModel(name string, defaultSize int) int {
	for _, m := range c.Models.Available {
		if m.Name == name {
			return m.ContextWindow
		}
	}
	return defaultSize
}

// Default returns a configuration suitable for local development with
// Ollama. All defaults are applied, so the returned Config is immediately
// usable without calling [Load].
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
