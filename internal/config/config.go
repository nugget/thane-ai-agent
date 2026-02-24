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
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/email"
	"github.com/nugget/thane-ai-agent/internal/forge"
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

// searchPathsFunc is the function used to generate search paths.
// Overridden in tests to avoid finding real config files on the host.
var searchPathsFunc = DefaultSearchPaths

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

	for _, p := range searchPathsFunc() {
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

	// KnowledgeBase configures the knowledge base directory for
	// long-form reference documents linked from facts.
	//
	// Deprecated: Use Paths instead. This field is migrated to
	// Paths["kb"] in applyDefaults and will be removed in a future
	// release.
	KnowledgeBase KnowledgeBaseConfig `yaml:"knowledge_base"`

	// Paths maps named prefixes to directory paths for file resolution.
	// Each entry creates a prefix (e.g., "kb" → kb:path resolves to
	// the configured directory). Supports ~ expansion at resolver
	// construction time.
	Paths map[string]string `yaml:"paths"`

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

	// Context configures static context injection into the system prompt.
	Context ContextConfig `yaml:"context"`

	// Archive configures session archive behavior.
	Archive ArchiveConfig `yaml:"archive"`

	// Extraction configures automatic fact extraction from conversations.
	Extraction ExtractionConfig `yaml:"extraction"`

	// Search configures web search providers.
	Search SearchConfig `yaml:"search"`

	// Episodic configures episodic memory context injection (daily
	// memory files and recent conversation history).
	Episodic EpisodicConfig `yaml:"episodic"`

	// Agent configures agent loop behavior, including orchestrator
	// tool gating for delegation-first architecture.
	Agent AgentConfig `yaml:"agent"`

	// CapabilityTags defines named groups of tools and talents that
	// can be activated or deactivated per session. Tags marked
	// always_active are loaded unconditionally. Other tags are
	// activated via request_capability/drop_capability tools or
	// channel-pinned configuration.
	CapabilityTags map[string]CapabilityTagConfig `yaml:"capability_tags"`

	// ChannelTags maps conversation source channels (e.g., "signal",
	// "email") to lists of capability tag names that are automatically
	// activated when a message arrives on that channel. This is
	// additive to always-active tags and any tags the agent requests
	// at runtime. Tag names must reference entries in [CapabilityTags].
	ChannelTags map[string][]string `yaml:"channel_tags"`

	// MCP configures external MCP (Model Context Protocol) server
	// connections for tool discovery. Each server provides additional
	// tools that are discovered dynamically and bridged into the
	// agent's tool registry.
	MCP MCPConfig `yaml:"mcp"`

	// MQTT configures MQTT publishing for Home Assistant device discovery
	// and sensor state reporting. When Broker and DeviceName are both
	// set, Thane connects to the broker and registers as an HA device.
	MQTT MQTTConfig `yaml:"mqtt"`

	// Person configures household member presence tracking. When Track
	// contains entity IDs, the agent receives a "People & Presence"
	// section in its system prompt on every wake, eliminating tool
	// calls for basic presence questions.
	Person PersonConfig `yaml:"person"`

	// Signal configures the Signal message bridge for inbound message
	// reception and response routing via a signal-mcp MCP server.
	Signal SignalConfig `yaml:"signal"`

	// Forge configures code forge integrations (GitHub, Gitea). When
	// configured, Thane can interact with issues, pull requests, and
	// code review directly without an MCP forge server subprocess.
	Forge forge.Config `yaml:"forge"`

	// Email configures native IMAP email access. When configured, Thane
	// can list, read, search, and manage email directly without an MCP
	// email server subprocess.
	Email email.Config `yaml:"email"`

	// StateWindow configures the rolling window of recent Home Assistant
	// state changes injected into the agent's system prompt on every run.
	StateWindow StateWindowConfig `yaml:"state_window"`

	// Unifi configures the UniFi network controller connection for
	// room-level presence detection via wireless AP client associations.
	Unifi UnifiConfig `yaml:"unifi"`

	// Prewarm configures context pre-warming for cold-start loops.
	// When enabled, subject-keyed facts are injected into the system
	// prompt before the model sees the triggering event.
	Prewarm PrewarmConfig `yaml:"prewarm"`

	// Media configures the media transcript retrieval tool. When yt-dlp
	// is available, the agent can fetch transcripts from YouTube, Vimeo,
	// podcasts, and other sources supported by yt-dlp.
	Media MediaConfig `yaml:"media"`

	// Metacognitive configures the perpetual metacognitive attention loop.
	// When enabled, a background goroutine monitors the environment,
	// reasons via LLM, and adapts its own sleep cycle between iterations.
	Metacognitive MetacognitiveConfig `yaml:"metacognitive"`

	// Debug configures diagnostic options for inspecting the assembled
	// system prompt and other internal state.
	Debug DebugConfig `yaml:"debug"`

	// Timezone is the IANA timezone for the household (e.g.,
	// "America/Chicago"). Used in the Current Conditions system prompt
	// section so the agent reasons about local time. If empty, the
	// system's local timezone is used.
	Timezone string `yaml:"timezone"`

	// Pricing maps model names to their per-million-token costs (USD).
	// When empty, built-in defaults for known Anthropic models are applied.
	// Local/Ollama models not listed here default to $0.
	Pricing map[string]PricingEntry `yaml:"pricing"`

	// LogLevel sets the minimum log level. Valid values: trace, debug,
	// info, warn, error. Default: info. See [ParseLogLevel].
	LogLevel string `yaml:"log_level"`

	// LogFormat sets the log output format. Valid values: text, json.
	// Default: text. Text is human-readable; JSON enables structured
	// log aggregation (Loki, Datadog, jq, etc.).
	LogFormat string `yaml:"log_format"`
}

// PricingEntry defines per-million-token costs for a model in USD.
type PricingEntry struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
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

	// Subscribe configures WebSocket event subscriptions for real-time
	// state change monitoring. When entity_globs is non-empty, only
	// matching entities are processed; when empty, all state changes
	// are accepted.
	Subscribe SubscribeConfig `yaml:"subscribe"`
}

// SubscribeConfig configures entity-level filtering and rate limiting
// for Home Assistant WebSocket state_changed event subscriptions.
type SubscribeConfig struct {
	// EntityGlobs is a list of glob patterns (using path.Match syntax)
	// that select which entity IDs to process. Examples: "person.*",
	// "binary_sensor.*door*", "light.living_room". An empty list
	// means all entities are accepted.
	EntityGlobs []string `yaml:"entity_globs"`

	// RateLimitPerMinute caps how many state changes per entity are
	// forwarded per minute. Zero means no rate limiting.
	RateLimitPerMinute int `yaml:"rate_limit_per_minute"`

	// CooldownMinutes is the per-anticipation cooldown period in minutes.
	// After an anticipation triggers a wake, it cannot trigger again until
	// this interval elapses. Defaults to 5 minutes via applyDefaults.
	CooldownMinutes int `yaml:"cooldown_minutes"`
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

// ContextConfig configures context injection into the system prompt.
// Files listed in InjectFiles are re-read on every agent turn so that
// external edits (e.g. MEMORY.md updated by another runtime) are
// visible without restart. Paths are resolved once at startup.
type ContextConfig struct {
	// InjectFiles is a list of file paths to re-read and inject into
	// the system prompt on every turn. Paths support ~ expansion.
	// Missing or unreadable files are silently skipped at read time.
	InjectFiles []string `yaml:"inject_files"`
}

// ArchiveConfig configures session archive behavior.
type ArchiveConfig struct {
	// MetadataModel is a soft preference for the LLM model used when
	// generating session metadata (title, tags, summaries). Passed as a
	// hint to the model router; the router has final say. This is a
	// background operation where latency doesn't matter — ideal for
	// local/free models. Default: uses the default model.
	MetadataModel string `yaml:"metadata_model"`

	// SummarizeInterval is how often (in seconds) the background
	// summarizer scans for unsummarized sessions. Default: 300 (5 min).
	SummarizeInterval int `yaml:"summarize_interval"`

	// SummarizeTimeout is the max seconds for a single session's
	// metadata LLM call. Default: 60.
	SummarizeTimeout int `yaml:"summarize_timeout"`
}

// ExtractionConfig configures automatic fact extraction from conversations.
// When enabled, the agent asynchronously analyzes each interaction after
// the response is delivered and persists noteworthy facts to the fact store.
// This is a background operation using local models — zero cost, no latency impact.
type ExtractionConfig struct {
	// Enabled controls whether automatic fact extraction runs.
	// Default: false (opt-in).
	Enabled bool `yaml:"enabled"`

	// Model is the LLM model used for fact extraction. This runs async
	// in the background — local/free models recommended.
	// Default: falls back to archive.metadata_model, then models.default.
	Model string `yaml:"model"`

	// MinMessages is the minimum conversation length (in messages) before
	// extraction is attempted. Very short exchanges rarely contain facts.
	// Default: 2.
	MinMessages int `yaml:"min_messages"`

	// TimeoutSeconds is the maximum time allowed for a single extraction
	// call. Default: 30.
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// EpisodicConfig configures episodic memory context injection. When
// configured, the agent receives curated daily notes and a recency-graded
// summary of recent conversations in its system prompt, giving it
// continuity across sessions.
type EpisodicConfig struct {
	// DailyDir is the directory containing daily memory files named
	// YYYY-MM-DD.md. Supports ~ expansion. If empty, daily memory
	// file injection is disabled.
	DailyDir string `yaml:"daily_dir"`

	// LookbackDays is how many days of daily memory files to include.
	// Today and the previous (LookbackDays-1) days are checked.
	// Default: 2 (today + yesterday).
	LookbackDays int `yaml:"lookback_days"`

	// HistoryTokens is the approximate token budget for recent
	// conversation history injected into the system prompt.
	// Default: 4000.
	HistoryTokens int `yaml:"history_tokens"`

	// SessionGapMinutes is the silence duration (in minutes) between
	// sessions that triggers a gap annotation in the history output.
	// Default: 30.
	SessionGapMinutes int `yaml:"session_gap_minutes"`
}

// AgentConfig configures agent loop behavior. When DelegationRequired
// is true, the agent loop only advertises the tools listed in
// OrchestratorTools, steering the primary model toward delegation
// instead of direct tool use.
type AgentConfig struct {
	// OrchestratorTools lists tool names to advertise when
	// DelegationRequired is true. If empty, a sensible default set
	// is applied (thane_delegate plus lightweight memory tools).
	OrchestratorTools []string `yaml:"orchestrator_tools"`

	// DeprecatedIter0Tools is the legacy name for OrchestratorTools.
	// Kept for backward compatibility with existing config files.
	DeprecatedIter0Tools []string `yaml:"iter0_tools"`

	// DelegationRequired enables orchestrator tool gating. When false
	// (the default), all tools are available on every iteration.
	DelegationRequired bool `yaml:"delegation_required"`
}

// CapabilityTagConfig defines a named group of tools (and optionally
// talents) that can be loaded together. Tags marked AlwaysActive are
// included in every session unconditionally.
type CapabilityTagConfig struct {
	// Description is a human-readable summary shown in the capability
	// manifest so the agent knows what activating this tag provides.
	Description string `yaml:"description"`

	// Tools lists the tool names belonging to this tag. A tool can
	// appear in multiple tags; it loads when any of its tags is active.
	Tools []string `yaml:"tools"`

	// Context lists file paths to read into the system prompt when
	// this tag is active. Paths support kb: prefix resolution and ~
	// expansion, resolved at startup. Files are re-read on every agent
	// turn so external edits are visible without restart. Missing
	// files are logged as warnings and skipped.
	Context []string `yaml:"context"`

	// AlwaysActive tags cannot be deactivated. They are included in
	// every session regardless of channel or agent requests.
	AlwaysActive bool `yaml:"always_active"`
}

// Validate checks that the capability tag configuration is internally
// consistent. It ensures a description is present and the tools list is
// non-empty. Tag names are validated by the caller since they are map
// keys in the parent Config struct.
func (c CapabilityTagConfig) Validate(tagName string) error {
	if strings.TrimSpace(c.Description) == "" {
		return fmt.Errorf("capability_tags.%s.description must not be empty", tagName)
	}
	if len(c.Tools) == 0 {
		return fmt.Errorf("capability_tags.%s.tools must not be empty", tagName)
	}
	return nil
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

// KnowledgeBaseConfig configures the knowledge base directory for
// long-form reference documents. Facts can link to KB pages via the
// ref field, and file tools resolve kb: prefixed paths against this
// directory.
type KnowledgeBaseConfig struct {
	// Path is the root directory for knowledge base files. When set,
	// file tools resolve "kb:foo.md" to files in this directory.
	// Should be within the workspace for write access.
	Path string `yaml:"path"`
}

// MQTTConfig configures the MQTT connection for Home Assistant device
// discovery and sensor state publishing. When [MQTTConfig.Configured]
// returns true, Thane connects to the broker at startup and registers
// as an HA device with availability tracking and runtime sensors.
type MQTTConfig struct {
	// Broker is the MQTT broker URL (e.g., "mqtts://host:8883"
	// or "mqtt://host:1883").
	Broker string `yaml:"broker"`

	// Username for MQTT broker authentication.
	Username string `yaml:"username"`

	// Password for MQTT broker authentication.
	Password string `yaml:"password"`

	// DiscoveryPrefix is the Home Assistant MQTT discovery topic
	// prefix. Default: "homeassistant".
	DiscoveryPrefix string `yaml:"discovery_prefix"`

	// DeviceName drives MQTT topic paths and HA entity IDs. Example:
	// "aimee-thane" produces sensor.aimee_thane_uptime in HA.
	DeviceName string `yaml:"device_name"`

	// PublishIntervalSec is how often (in seconds) sensor states are
	// re-published to the broker. Default: 60. Minimum: 10.
	PublishIntervalSec int `yaml:"publish_interval"`

	// Subscriptions lists MQTT topics to subscribe to for ambient
	// awareness. Messages are received and logged but not autonomously
	// acted upon. Future phases will route messages to the anticipation
	// engine. Supports MQTT wildcard characters (+ and #).
	Subscriptions []SubscriptionConfig `yaml:"subscriptions"`
}

// SubscriptionConfig describes a single MQTT topic subscription.
// Each entry is subscribed on every broker (re-)connect. Wildcards
// (+ and #) are supported per the MQTT specification.
type SubscriptionConfig struct {
	// Topic is the MQTT topic filter (e.g., "homeassistant/+/+/state",
	// "frigate/events"). Supports MQTT wildcard characters.
	Topic string `yaml:"topic"`
}

// Configured reports whether both Broker and DeviceName are set. A
// partial configuration is treated as unconfigured — Thane will start
// without MQTT publishing.
func (c MQTTConfig) Configured() bool {
	return c.Broker != "" && c.DeviceName != ""
}

// PersonConfig configures household member presence tracking. When
// Track contains entity IDs, the person tracker maintains in-memory
// state from Home Assistant and injects a presence summary into the
// agent's system prompt on every wake.
type PersonConfig struct {
	// Track is a list of Home Assistant person entity IDs to monitor
	// (e.g., ["person.nugget", "person.dan"]). Each entry must begin
	// with "person.". An empty list disables person tracking.
	Track []string `yaml:"track"`

	// Devices maps tracked person entity IDs to their wireless device
	// MAC addresses. Used by the UniFi poller to determine which person
	// a wireless client belongs to for room-level presence.
	Devices map[string][]DeviceMapping `yaml:"devices"`

	// APRooms maps AP names (e.g., "ap-hor-office") to human-readable
	// room names (e.g., "office"). Only APs listed here contribute to
	// room presence; unlisted APs are ignored.
	APRooms map[string]string `yaml:"ap_rooms"`
}

// DeviceMapping maps a MAC address to a tracked person's wireless device.
type DeviceMapping struct {
	// MAC is the device's MAC address (e.g., "AA:BB:CC:DD:EE:FF").
	// Case-insensitive; normalized to lowercase at startup.
	MAC string `yaml:"mac"`
}

// UnifiConfig configures the UniFi network controller connection for
// room-level presence detection via AP client associations.
type UnifiConfig struct {
	// URL is the base URL of the UniFi controller
	// (e.g., "https://192.168.1.1").
	URL string `yaml:"url"`

	// APIKey is the API key for UniFi controller authentication.
	// Sent as X-API-KEY header.
	APIKey string `yaml:"api_key"`

	// PollIntervalSec is how often (in seconds) to poll for wireless
	// client station data. Default: 30. Minimum: 10.
	PollIntervalSec int `yaml:"poll_interval"`
}

// Configured reports whether both URL and APIKey are set, indicating
// the UniFi integration should be enabled.
func (c UnifiConfig) Configured() bool {
	return c.URL != "" && c.APIKey != ""
}

// DebugConfig configures diagnostic options for inspecting the
// assembled system prompt and other internal state.
type DebugConfig struct {
	// DumpSystemPrompt enables writing the fully assembled system
	// prompt to disk on every LLM call, with section markers and
	// size annotations. The file is overwritten each call so it
	// always reflects the most recent prompt.
	DumpSystemPrompt bool `yaml:"dump_system_prompt"`

	// DumpDir is the directory where debug output files are written.
	// Created automatically on first write. Default: "./debug".
	DumpDir string `yaml:"dump_dir"`
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

// MCPConfig configures MCP (Model Context Protocol) client connections
// to external tool servers. Each server provides additional tools that
// are discovered dynamically and bridged into the agent's tool registry.
type MCPConfig struct {
	// Servers lists the MCP servers to connect to at startup.
	Servers []MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig describes a single MCP server endpoint. Each server
// is identified by a short name used for tool namespacing and logging.
type MCPServerConfig struct {
	// Name is a short identifier used in tool namespacing and logging
	// (e.g., "home-assistant", "github"). Required.
	Name string `yaml:"name"`

	// Transport is the connection type: "stdio" or "http". Required.
	Transport string `yaml:"transport"`

	// Command is the executable to spawn (stdio transport only).
	Command string `yaml:"command"`

	// Args are command-line arguments for the subprocess (stdio transport only).
	Args []string `yaml:"args"`

	// Env are additional environment variables for the subprocess
	// (stdio transport only). Format: "KEY=VALUE".
	Env []string `yaml:"env"`

	// URL is the MCP server endpoint (http transport only).
	URL string `yaml:"url"`

	// Headers are additional HTTP headers sent with every request
	// (http transport only). Useful for authentication tokens.
	Headers map[string]string `yaml:"headers"`

	// IncludeTools is an optional allowlist of MCP tool names to
	// bridge. When non-empty, only tools in this list are registered.
	// Cannot be used together with ExcludeTools.
	IncludeTools []string `yaml:"include_tools"`

	// ExcludeTools is an optional blocklist of MCP tool names to skip.
	// Cannot be used together with IncludeTools.
	ExcludeTools []string `yaml:"exclude_tools"`
}

// SignalConfig configures the native Signal message bridge using
// signal-cli's jsonRpc mode over stdin/stdout.
type SignalConfig struct {
	// Enabled controls whether the Signal bridge starts.
	Enabled bool `yaml:"enabled"`

	// Command is the signal-cli executable path (e.g., "signal-cli").
	Command string `yaml:"command"`

	// Account is the phone number to use (e.g., "+15124232707").
	// Passed as the -a flag to signal-cli.
	Account string `yaml:"account"`

	// Args are additional command-line arguments appended after the
	// standard "-a ACCOUNT jsonRpc" arguments.
	Args []string `yaml:"args"`

	// RateLimitPerMinute caps how many inbound messages per sender
	// are processed per minute. Zero disables rate limiting.
	// Default: 10.
	RateLimitPerMinute int `yaml:"rate_limit_per_minute"`

	// SessionIdleMinutes is the idle timeout in minutes for session
	// rotation. When a new Signal message arrives and the last message
	// from that sender was more than this many minutes ago, the
	// previous session is ended (triggering background summarization)
	// and a fresh one begins on the next agent loop call. Zero or
	// omitted disables idle rotation.
	SessionIdleMinutes int `yaml:"session_idle_minutes"`

	// Routing configures how Signal messages are routed to LLM models.
	// All fields are optional; defaults preserve the original hardcoded
	// behavior (quality_floor=6, mission=conversation, delegation_gating=disabled).
	Routing SignalRoutingConfig `yaml:"routing"`

	// AttachmentSourceDir is the directory where signal-cli stores
	// downloaded attachments. Defaults to
	// ~/.local/share/signal-cli/attachments when empty.
	AttachmentSourceDir string `yaml:"attachment_source_dir"`

	// AttachmentDir is the workspace subdirectory where received
	// attachments are copied for agent access. Defaults to
	// {workspace}/signal-attachments when empty and workspace is set.
	AttachmentDir string `yaml:"attachment_dir"`

	// MaxAttachmentSize is the maximum attachment size in bytes that
	// will be processed. Attachments exceeding this are described but
	// not copied. Zero means no limit.
	MaxAttachmentSize int64 `yaml:"max_attachment_size"`
}

// SignalRoutingConfig controls model selection for Signal messages.
// When Model is set, the router is bypassed entirely and the named
// model handles every Signal message. The remaining fields are passed
// as routing hints when the router is active.
type SignalRoutingConfig struct {
	// Model sets an explicit model for Signal messages. When non-empty,
	// the router is bypassed entirely. Empty means use the router with
	// the hint-based defaults below.
	Model string `yaml:"model"`

	// QualityFloor is the minimum model quality rating (1-10) passed
	// to the router. Default: "6".
	QualityFloor string `yaml:"quality_floor"`

	// Mission describes the task context for routing. Default: "conversation".
	Mission string `yaml:"mission"`

	// DelegationGating controls whether delegation-first tool gating
	// is active. Default: "disabled".
	DelegationGating string `yaml:"delegation_gating"`
}

// Configured reports whether the Signal bridge has the minimum
// required configuration (enabled with a command and account).
func (c SignalConfig) Configured() bool {
	return c.Enabled && c.Command != "" && c.Account != ""
}

// PrewarmConfig configures context pre-warming for cold-start loops.
// When enabled, subject-keyed facts are injected into the system prompt
// before the model sees the triggering event. This reduces wasted
// iterations where the model discovers facts it should already have.
// See issue #338.
type PrewarmConfig struct {
	// Enabled controls whether subject-keyed fact injection is active.
	// Default: false.
	Enabled bool `yaml:"enabled"`

	// MaxFacts caps the number of subject-matched facts injected per
	// wake. Default: 10.
	MaxFacts int `yaml:"max_facts"`

	// Archive configures Phase 2 pre-warming: injecting relevant past
	// conversation excerpts alongside Layer 1 facts. See issue #404.
	Archive ArchivePrewarmConfig `yaml:"archive"`
}

// ArchivePrewarmConfig configures archive retrieval injection for
// cold-start wakes. When enabled, relevant past conversation excerpts
// are injected into the system prompt so the model has experiential
// judgment — not just knowledge — before responding.
type ArchivePrewarmConfig struct {
	// Enabled controls whether archive injection is active.
	// Requires the parent Prewarm.Enabled to also be true.
	// Default: false.
	Enabled bool `yaml:"enabled"`

	// MaxResults caps the number of archive search results injected.
	// Default: 3.
	MaxResults int `yaml:"max_results"`

	// MaxBytes caps the formatted output in bytes to prevent context
	// flooding. Default: 4000 (~1000 tokens).
	MaxBytes int `yaml:"max_bytes"`
}

// MediaConfig configures the media transcript retrieval tool and
// RSS/Atom feed monitoring.
type MediaConfig struct {
	// YtDlpPath is the explicit path to the yt-dlp binary. If empty,
	// the binary is located via exec.LookPath at startup.
	YtDlpPath string `yaml:"yt_dlp_path"`

	// CookiesFile is an optional path to a Netscape-format cookie file
	// for accessing auth-required content (e.g., age-restricted videos).
	CookiesFile string `yaml:"cookies_file"`

	// SubtitleLanguage is the preferred subtitle language code.
	// Default: "en".
	SubtitleLanguage string `yaml:"subtitle_language"`

	// MaxTranscriptChars limits the transcript text returned in-context.
	// Longer transcripts are truncated. Default: 50000.
	MaxTranscriptChars int `yaml:"max_transcript_chars"`

	// WhisperModel is the Ollama model name for audio transcription
	// fallback when no subtitles are available. Default: "large-v3".
	WhisperModel string `yaml:"whisper_model"`

	// TranscriptDir is the directory for durable transcript storage.
	// Each transcript is saved as a markdown file with YAML frontmatter.
	// If empty, transcripts are returned in-context only (not persisted).
	TranscriptDir string `yaml:"transcript_dir"`

	// SummarizeModel is the preferred model for transcript summarization.
	// When set, it is passed as a routing hint (soft preference, not
	// override). If empty, the router selects an appropriate local model.
	SummarizeModel string `yaml:"summarize_model"`

	// FeedCheckInterval is how often (in seconds) to poll followed RSS/Atom
	// feeds for new entries. Set to a positive value to enable polling (e.g.,
	// 3600 for hourly). Default: 0 (disabled). No default is applied —
	// users must opt in by setting a positive interval.
	FeedCheckInterval int `yaml:"feed_check_interval"`

	// MaxFeeds limits the number of feeds that can be followed.
	// Default: 50.
	MaxFeeds int `yaml:"max_feeds"`
}

// MetacognitiveConfig configures the self-regulating metacognitive loop.
// The loop runs perpetually in a background goroutine, using LLM calls to
// reason about the environment and self-determine its sleep duration
// between iterations. See issue #319.
type MetacognitiveConfig struct {
	// Enabled controls whether the metacognitive loop starts. Default: false.
	Enabled bool `yaml:"enabled"`

	// StateFile is the path to the persistent state file, relative to
	// the workspace root. Default: "metacognitive.md".
	StateFile string `yaml:"state_file"`

	// MinSleep is the minimum allowed sleep duration between iterations.
	// The LLM cannot request a shorter sleep via set_next_sleep.
	// Default: "2m". Parsed as a Go duration string.
	MinSleep string `yaml:"min_sleep"`

	// MaxSleep is the maximum allowed sleep duration between iterations.
	// Default: "30m".
	MaxSleep string `yaml:"max_sleep"`

	// DefaultSleep is used when the LLM does not call set_next_sleep.
	// Default: "10m".
	DefaultSleep string `yaml:"default_sleep"`

	// Jitter is the sleep randomization factor (0.0–1.0). A value of
	// 0.2 means the actual sleep varies by ±20% of the computed
	// duration. Default: 0.2. Set to 0.0 for deterministic timing.
	Jitter float64 `yaml:"jitter"`

	// SupervisorProbability is the chance (0.0–1.0) that each wake
	// uses a frontier model with supervisor-augmented prompt.
	// Default: 0.1. Set to 0.0 to disable supervisor iterations.
	SupervisorProbability float64 `yaml:"supervisor_probability"`

	// Router configures model routing for normal (non-supervisor)
	// iterations.
	Router MetacognitiveRouterConfig `yaml:"router"`

	// SupervisorRouter configures model routing for supervisor
	// iterations (frontier model with augmented prompt).
	SupervisorRouter MetacognitiveRouterConfig `yaml:"supervisor_router"`
}

// MetacognitiveRouterConfig holds routing hints for metacognitive iterations.
type MetacognitiveRouterConfig struct {
	// QualityFloor is the minimum quality rating (1–10) for model
	// selection. Default: 3 for normal iterations, 8 for supervisor.
	QualityFloor int `yaml:"quality_floor"`
}

// StateWindowConfig configures the rolling window of recent Home Assistant
// state changes injected into the agent's system prompt.
type StateWindowConfig struct {
	// MaxEntries is the circular buffer capacity. When the buffer is
	// full, the oldest entry is overwritten. Default: 50.
	MaxEntries int `yaml:"max_entries"`

	// MaxAgeMinutes controls how long entries remain visible. Entries
	// older than this are excluded from the context output at read
	// time. Default: 30.
	MaxAgeMinutes int `yaml:"max_age_minutes"`
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
	if c.Archive.MetadataModel == "" {
		c.Archive.MetadataModel = c.Models.Default
	}
	if c.Archive.SummarizeInterval == 0 {
		c.Archive.SummarizeInterval = 300
	}
	if c.Archive.SummarizeTimeout == 0 {
		c.Archive.SummarizeTimeout = 60
	}
	if c.Extraction.Model == "" {
		c.Extraction.Model = c.Archive.MetadataModel
	}
	if c.Extraction.MinMessages == 0 {
		c.Extraction.MinMessages = 2
	}
	if c.Extraction.TimeoutSeconds == 0 {
		c.Extraction.TimeoutSeconds = 30
	}

	if c.MQTT.DiscoveryPrefix == "" {
		c.MQTT.DiscoveryPrefix = "homeassistant"
	}
	if c.MQTT.PublishIntervalSec == 0 {
		c.MQTT.PublishIntervalSec = 60
	}

	if c.Unifi.PollIntervalSec == 0 {
		c.Unifi.PollIntervalSec = 30
	}

	if c.Media.SubtitleLanguage == "" {
		c.Media.SubtitleLanguage = "en"
	}
	if c.Media.MaxTranscriptChars == 0 {
		c.Media.MaxTranscriptChars = 50000
	}
	if c.Media.WhisperModel == "" {
		c.Media.WhisperModel = "large-v3"
	}
	// FeedCheckInterval is intentionally not defaulted — 0 means disabled.
	// Users must opt in by setting a positive value.

	if c.Media.MaxFeeds == 0 {
		c.Media.MaxFeeds = 50
	}

	if c.Episodic.LookbackDays == 0 {
		c.Episodic.LookbackDays = 2
	}
	if c.Episodic.HistoryTokens == 0 {
		c.Episodic.HistoryTokens = 4000
	}
	if c.Episodic.SessionGapMinutes == 0 {
		c.Episodic.SessionGapMinutes = 30
	}

	if c.Debug.DumpSystemPrompt && c.Debug.DumpDir == "" {
		c.Debug.DumpDir = "./debug"
	}

	if c.Pricing == nil {
		c.Pricing = map[string]PricingEntry{
			"claude-opus-4-20250514":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4-20250514": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-haiku-3-20240307":  {InputPerMillion: 0.25, OutputPerMillion: 1.25},
		}
	}

	// Pre-warm defaults.
	if c.Prewarm.MaxFacts == 0 {
		c.Prewarm.MaxFacts = 10
	}
	if c.Prewarm.Archive.MaxResults == 0 {
		c.Prewarm.Archive.MaxResults = 3
	}
	if c.Prewarm.Archive.MaxBytes == 0 {
		c.Prewarm.Archive.MaxBytes = 4000
	}

	// Metacognitive loop defaults.
	if c.Metacognitive.StateFile == "" {
		c.Metacognitive.StateFile = "metacognitive.md"
	}
	if c.Metacognitive.MinSleep == "" {
		c.Metacognitive.MinSleep = "2m"
	}
	if c.Metacognitive.MaxSleep == "" {
		c.Metacognitive.MaxSleep = "30m"
	}
	if c.Metacognitive.DefaultSleep == "" {
		c.Metacognitive.DefaultSleep = "10m"
	}
	if c.Metacognitive.Jitter == 0 {
		c.Metacognitive.Jitter = 0.2
	}
	if c.Metacognitive.SupervisorProbability == 0 {
		c.Metacognitive.SupervisorProbability = 0.1
	}
	if c.Metacognitive.Router.QualityFloor == 0 {
		c.Metacognitive.Router.QualityFloor = 3
	}
	if c.Metacognitive.SupervisorRouter.QualityFloor == 0 {
		c.Metacognitive.SupervisorRouter.QualityFloor = 8
	}

	// Backward compat: migrate deprecated iter0_tools → orchestrator_tools.
	// Always clear the deprecated field; only copy if the new field is empty.
	if len(c.Agent.DeprecatedIter0Tools) > 0 {
		if len(c.Agent.OrchestratorTools) == 0 {
			c.Agent.OrchestratorTools = c.Agent.DeprecatedIter0Tools
		}
		c.Agent.DeprecatedIter0Tools = nil
	}

	// Backward compat: migrate deprecated knowledge_base.path → paths["kb"].
	if c.KnowledgeBase.Path != "" && (c.Paths == nil || c.Paths["kb"] == "") {
		if c.Paths == nil {
			c.Paths = make(map[string]string)
		}
		c.Paths["kb"] = c.KnowledgeBase.Path
	}

	if c.Agent.DelegationRequired && len(c.Agent.OrchestratorTools) == 0 {
		c.Agent.OrchestratorTools = []string{
			"thane_delegate",
			"recall_fact",
			"remember_fact",
			"save_contact",
			"lookup_contact",
			"session_working_memory",
			"session_close",
			"archive_search",
		}
	}

	if c.HomeAssistant.Subscribe.CooldownMinutes == 0 {
		c.HomeAssistant.Subscribe.CooldownMinutes = 5
	}

	// Signal session idle timeout: 0 disables idle rotation (no default override).
	// Users who want idle rotation must set a positive value explicitly.

	// Signal rate limit: 0 means unlimited (no default override).
	// Users who want limiting must set a positive value explicitly.
	if c.Signal.Routing.QualityFloor == "" {
		c.Signal.Routing.QualityFloor = "6"
	}
	if c.Signal.Routing.Mission == "" {
		c.Signal.Routing.Mission = "conversation"
	}
	if c.Signal.Routing.DelegationGating == "" {
		c.Signal.Routing.DelegationGating = "disabled"
	}
	if c.Signal.AttachmentSourceDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Warn("unable to determine home directory for signal attachment source; set signal.attachment_source_dir explicitly",
				"error", err,
			)
		} else {
			c.Signal.AttachmentSourceDir = filepath.Join(home, ".local", "share", "signal-cli", "attachments")
		}
	}
	if c.Signal.AttachmentDir == "" && c.Workspace.Path != "" {
		c.Signal.AttachmentDir = filepath.Join(c.Workspace.Path, "signal-attachments")
	}

	c.Forge.ApplyDefaults()

	c.Email.ApplyDefaults()

	if c.StateWindow.MaxEntries == 0 {
		c.StateWindow.MaxEntries = 50
	}
	if c.StateWindow.MaxAgeMinutes == 0 {
		c.StateWindow.MaxAgeMinutes = 30
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
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("timezone %q invalid (expected IANA timezone, e.g. America/Chicago): %w", c.Timezone, err)
		}
	}
	if c.MQTT.Configured() {
		u, err := url.Parse(c.MQTT.Broker)
		if err != nil {
			return fmt.Errorf("mqtt.broker %q is not a valid URL: %w", c.MQTT.Broker, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("mqtt.broker %q must include a scheme and host", c.MQTT.Broker)
		}
		switch u.Scheme {
		case "mqtt", "mqtts", "ssl", "ws", "wss":
			// supported schemes
		default:
			return fmt.Errorf("mqtt.broker scheme %q invalid (expected one of mqtt, mqtts, ssl, ws, wss)", u.Scheme)
		}
		if c.MQTT.PublishIntervalSec < 10 {
			return fmt.Errorf("mqtt.publish_interval %d too low (minimum 10 seconds)", c.MQTT.PublishIntervalSec)
		}
		for i, sub := range c.MQTT.Subscriptions {
			if sub.Topic == "" {
				return fmt.Errorf("mqtt.subscriptions[%d].topic must not be empty", i)
			}
		}
	}
	if err := c.validateSubscribe(); err != nil {
		return err
	}
	if err := c.validateMCP(); err != nil {
		return err
	}
	for tagName, tagCfg := range c.CapabilityTags {
		if err := tagCfg.Validate(tagName); err != nil {
			return err
		}
	}
	for channel, tagNames := range c.ChannelTags {
		for _, tagName := range tagNames {
			if _, ok := c.CapabilityTags[tagName]; !ok {
				return fmt.Errorf("channel_tags.%s references undefined capability tag %q", channel, tagName)
			}
		}
	}
	if c.Episodic.LookbackDays < 0 {
		return fmt.Errorf("episodic.lookback_days %d must be non-negative", c.Episodic.LookbackDays)
	}
	if c.Episodic.HistoryTokens < 0 {
		return fmt.Errorf("episodic.history_tokens %d must be non-negative", c.Episodic.HistoryTokens)
	}
	if c.Episodic.SessionGapMinutes < 0 {
		return fmt.Errorf("episodic.session_gap_minutes %d must be non-negative", c.Episodic.SessionGapMinutes)
	}
	for i, id := range c.Person.Track {
		if !strings.HasPrefix(id, "person.") {
			return fmt.Errorf("person.track[%d] %q must start with \"person.\"", i, id)
		}
	}
	// Validate person.devices references only tracked entities.
	tracked := make(map[string]bool, len(c.Person.Track))
	for _, id := range c.Person.Track {
		tracked[id] = true
	}
	for entityID := range c.Person.Devices {
		if !tracked[entityID] {
			return fmt.Errorf("person.devices references untracked entity %q", entityID)
		}
	}
	for entityID, devs := range c.Person.Devices {
		for i, d := range devs {
			if d.MAC == "" {
				return fmt.Errorf("person.devices[%s][%d].mac must not be empty", entityID, i)
			}
		}
	}
	if c.Unifi.Configured() && c.Unifi.PollIntervalSec < 10 {
		return fmt.Errorf("unifi.poll_interval %d too low (minimum 10 seconds)", c.Unifi.PollIntervalSec)
	}
	if err := c.validateSignal(); err != nil {
		return err
	}
	if c.Forge.Configured() {
		if err := c.Forge.Validate(); err != nil {
			return err
		}
	}
	if c.Email.Configured() {
		if err := c.Email.Validate(); err != nil {
			return err
		}
	}
	if c.StateWindow.MaxEntries < 1 {
		return fmt.Errorf("state_window.max_entries %d must be positive", c.StateWindow.MaxEntries)
	}
	if c.StateWindow.MaxAgeMinutes < 1 {
		return fmt.Errorf("state_window.max_age_minutes %d must be positive", c.StateWindow.MaxAgeMinutes)
	}
	if c.Prewarm.Enabled && c.Prewarm.MaxFacts < 1 {
		return fmt.Errorf("prewarm.max_facts %d must be positive when prewarm is enabled", c.Prewarm.MaxFacts)
	}
	if c.Prewarm.Enabled && c.Prewarm.Archive.Enabled {
		if c.Prewarm.Archive.MaxResults < 1 {
			return fmt.Errorf("prewarm.archive.max_results %d must be positive when archive pre-warming is enabled", c.Prewarm.Archive.MaxResults)
		}
		if c.Prewarm.Archive.MaxBytes < 500 {
			return fmt.Errorf("prewarm.archive.max_bytes %d must be at least 500 when archive pre-warming is enabled", c.Prewarm.Archive.MaxBytes)
		}
	}
	if err := c.validateMetacognitive(); err != nil {
		return err
	}
	return nil
}

// validateMetacognitive checks metacognitive loop configuration for
// consistency. Only checked when the loop is enabled.
func (c *Config) validateMetacognitive() error {
	if !c.Metacognitive.Enabled {
		return nil
	}
	if c.Workspace.Path == "" {
		return fmt.Errorf("metacognitive requires workspace.path (state file lives there)")
	}
	minSleep, err := time.ParseDuration(c.Metacognitive.MinSleep)
	if err != nil {
		return fmt.Errorf("metacognitive.min_sleep %q: %w", c.Metacognitive.MinSleep, err)
	}
	maxSleep, err := time.ParseDuration(c.Metacognitive.MaxSleep)
	if err != nil {
		return fmt.Errorf("metacognitive.max_sleep %q: %w", c.Metacognitive.MaxSleep, err)
	}
	defaultSleep, err := time.ParseDuration(c.Metacognitive.DefaultSleep)
	if err != nil {
		return fmt.Errorf("metacognitive.default_sleep %q: %w", c.Metacognitive.DefaultSleep, err)
	}
	if minSleep > maxSleep {
		return fmt.Errorf("metacognitive.min_sleep (%s) exceeds max_sleep (%s)", minSleep, maxSleep)
	}
	if defaultSleep < minSleep || defaultSleep > maxSleep {
		return fmt.Errorf("metacognitive.default_sleep (%s) must be between min_sleep (%s) and max_sleep (%s)",
			defaultSleep, minSleep, maxSleep)
	}
	if c.Metacognitive.Jitter < 0 || c.Metacognitive.Jitter > 1.0 {
		return fmt.Errorf("metacognitive.jitter %.2f must be in [0.0, 1.0]", c.Metacognitive.Jitter)
	}
	if c.Metacognitive.SupervisorProbability < 0 || c.Metacognitive.SupervisorProbability > 1.0 {
		return fmt.Errorf("metacognitive.supervisor_probability %.2f must be in [0.0, 1.0]", c.Metacognitive.SupervisorProbability)
	}
	return nil
}

// validateMCP checks the MCP server configuration for consistency.
func (c *Config) validateMCP() error {
	names := make(map[string]bool, len(c.MCP.Servers))
	for i, srv := range c.MCP.Servers {
		if srv.Name == "" {
			return fmt.Errorf("mcp.servers[%d].name must not be empty", i)
		}
		if names[srv.Name] {
			return fmt.Errorf("mcp.servers[%d].name %q is a duplicate", i, srv.Name)
		}
		names[srv.Name] = true

		switch srv.Transport {
		case "stdio":
			if srv.Command == "" {
				return fmt.Errorf("mcp.servers[%d] (%s): stdio transport requires a command", i, srv.Name)
			}
		case "http":
			if srv.URL == "" {
				return fmt.Errorf("mcp.servers[%d] (%s): http transport requires a url", i, srv.Name)
			}
		default:
			return fmt.Errorf("mcp.servers[%d] (%s): transport %q invalid (expected stdio or http)", i, srv.Name, srv.Transport)
		}

		if len(srv.IncludeTools) > 0 && len(srv.ExcludeTools) > 0 {
			return fmt.Errorf("mcp.servers[%d] (%s): cannot set both include_tools and exclude_tools", i, srv.Name)
		}
	}
	return nil
}

// validateSubscribe checks the Home Assistant subscribe configuration
// for consistency.
func (c *Config) validateSubscribe() error {
	for i, glob := range c.HomeAssistant.Subscribe.EntityGlobs {
		if _, err := path.Match(glob, ""); err != nil {
			return fmt.Errorf("homeassistant.subscribe.entity_globs[%d] %q: invalid glob pattern: %w", i, glob, err)
		}
	}
	if c.HomeAssistant.Subscribe.RateLimitPerMinute < 0 {
		return fmt.Errorf("homeassistant.subscribe.rate_limit_per_minute %d must be non-negative", c.HomeAssistant.Subscribe.RateLimitPerMinute)
	}
	return nil
}

// validateSignal checks the Signal bridge configuration for consistency.
func (c *Config) validateSignal() error {
	if !c.Signal.Enabled {
		return nil
	}
	if c.Signal.Command == "" {
		return fmt.Errorf("signal.command is required when signal.enabled is true")
	}
	if c.Signal.Account == "" {
		return fmt.Errorf("signal.account is required when signal.enabled is true")
	}
	if c.Signal.RateLimitPerMinute < 0 {
		return fmt.Errorf("signal.rate_limit_per_minute %d must be non-negative", c.Signal.RateLimitPerMinute)
	}
	if c.Signal.SessionIdleMinutes < 0 {
		return fmt.Errorf("signal.session_idle_minutes %d must be non-negative", c.Signal.SessionIdleMinutes)
	}
	if c.Signal.MaxAttachmentSize < 0 {
		return fmt.Errorf("signal.max_attachment_size %d must be non-negative", c.Signal.MaxAttachmentSize)
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
