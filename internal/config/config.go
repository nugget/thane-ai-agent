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
//
// To regenerate examples/config.example.yaml from source:
//
//	go generate ./internal/config
//
//go:generate go run ./gen/gencfg -srcdir . -out ../../examples/config.example.yaml
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/forge"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/search"
	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
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

// Config is the top-level configuration structure for the Thane agent,
// loaded from a single YAML file via [Load]. The struct hierarchy maps
// directly to the YAML key hierarchy; the generated example config
// (examples/config.example.yaml) is derived from these struct definitions
// and their field comments via `go generate ./internal/config`.
//
// After [Load] returns, all fields carry usable values -- callers never
// need empty-string checks or fallback logic. See [Config.applyDefaults]
// for the defaulting rules and [Config.Validate] for consistency checks.
type Config struct {
	// Listen configures the primary HTTP API server (OpenAI-compatible).
	Listen ListenConfig `yaml:"listen"`

	// OllamaAPI configures the optional Ollama-compatible API server,
	// used for Home Assistant integration.
	OllamaAPI OllamaAPIConfig `yaml:"ollama_api"`

	// CardDAV configures the optional CardDAV server for native
	// contact app sync (macOS Contacts.app, iOS, Thunderbird, etc.).
	CardDAV CardDAVConfig `yaml:"carddav"`

	// Platform configures the WebSocket endpoint for native platform
	// provider connections (e.g. macOS app).
	Platform PlatformConfig `yaml:"platform"`

	// HomeAssistant configures the connection to a Home Assistant instance.
	HomeAssistant HomeAssistantConfig `yaml:"homeassistant"`

	// Models configures LLM providers, model routing, and the default model.
	Models ModelsConfig `yaml:"models"`

	// Anthropic configures the Anthropic (Claude) API provider.
	Anthropic AnthropicConfig `yaml:"anthropic"`

	// Embeddings configures vector embedding generation for semantic search.
	Embeddings EmbeddingsConfig `yaml:"embeddings"`

	// Workspace configures the agent's sandboxed file system access.
	// The workspace root is also the anchor for Thane's fixed core
	// document root at {workspace.path}/core, which holds canonical
	// always-on files at stable locations such as persona.md, ego.md,
	// mission.md, and metacognitive.md.
	Workspace WorkspaceConfig `yaml:"workspace"`

	// Paths maps named prefixes to directory paths for file resolution.
	// Each entry creates a prefix (e.g., "kb" → kb:path resolves to
	// the configured directory). Supports ~ expansion at resolver
	// construction time.
	//
	// These prefixes also define the managed local document roots used by
	// the documents capability. Any configured root that exists on disk is
	// eligible for indexed browse/search/section retrieval via doc_* tools,
	// so users can add their own custom corpora here without code changes.
	// See docs/understanding/document-roots.md for the operator-facing
	// contract; keep that document in sync with changes here.
	//
	// Typical prefixes are:
	//   - kb:         curated knowledge / indexed documents
	//   - generated:  model-produced durable outputs (reports, dailies)
	//   - scratchpad: low-integrity writable work area
	//   - dossiers:   private dossiers or long-form reference material
	//
	// The core: prefix is reserved and always derived from
	// {workspace.path}/core; it is not configured here.
	Paths map[string]string `yaml:"paths"`

	// ExtraPath lists additional directories to prepend to the process
	// PATH at startup, ensuring exec.LookPath finds binaries installed
	// outside the default system PATH (e.g., /opt/homebrew/bin on macOS).
	// Environment variables are expanded (e.g., $HOME/bin).
	ExtraPath []string `yaml:"extra_path"`

	// ShellExec configures the agent's ability to run shell commands.
	ShellExec ShellExecConfig `yaml:"shell_exec"`

	// DataDir is the root directory for SQLite databases and other
	// opaque runtime state (memory, facts, scheduler, checkpoints).
	// Keep this separate from human-authored and model-authored
	// document roots. Default: "./db".
	DataDir string `yaml:"data_dir"`

	// TalentsDir is the directory containing talent markdown files that
	// extend the system prompt. In higher-integrity deployments, this is
	// a curated managed root rather than a scratch workspace.
	// Default: "./talents".
	TalentsDir string `yaml:"talents_dir"`

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

	// Delegate configures the thane_delegate tool's split-model execution.
	Delegate DelegateConfig `yaml:"delegate"`

	// CapabilityTags overlays the compiled-in tool/tag baseline with
	// operator-defined descriptions, tool membership overrides, and
	// custom tags. Tags marked always_active are loaded
	// unconditionally. Other tags are activated via
	// activate_capability/deactivate_capability tools or channel-pinned
	// configuration.
	CapabilityTags map[string]CapabilityTagConfig `yaml:"capability_tags"`

	// ChannelTags maps conversation source channels (e.g., "signal",
	// "email") to lists of capability tag names that are automatically
	// activated when a message arrives on that channel. This is
	// additive to always-active tags and any tags the agent requests
	// at runtime. Tag names must reference either compiled-in tags or
	// entries in [CapabilityTags].
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

	// Identity configures the agent's own contact identity for vCard
	// export and self-referencing operations.
	Identity IdentityConfig `yaml:"identity"`

	// Attachments configures content-addressed attachment storage.
	// When StoreDir is set, received attachments (Signal, email, etc.)
	// are stored by SHA-256 hash with a SQLite metadata index for
	// deduplication and provenance tracking.
	Attachments AttachmentsConfig `yaml:"attachments"`

	// Provenance configures git-backed file storage with SSH signature
	// enforcement. Files written through a provenance store are
	// automatically committed with cryptographic signatures, providing
	// tamper detection, audit history, and rollback. Newer core-root
	// layouts read always-on identity documents directly from
	// {workspace.path}/core rather than from this store.
	Provenance ProvenanceConfig `yaml:"provenance"`

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

	// Loops configures immutable loop definitions loaded from the config
	// file. These definitions become the base layer for the loops-ng
	// definition registry, with a persistent dynamic overlay applied at
	// runtime.
	Loops LoopsConfig `yaml:"loops"`

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

	// Logging configures Thane's filesystem datasets, stdout policy, and
	// queryable request/log retention.
	Logging LoggingConfig `yaml:"logging"`

	// LogLevel is deprecated; use Logging.Level instead.
	// Kept for backwards compatibility — migrated in [Config.applyDefaults].
	LogLevel string `yaml:"log_level"`

	// LogFormat is deprecated; use Logging.Format instead.
	// Kept for backwards compatibility — migrated in [Config.applyDefaults].
	LogFormat string `yaml:"log_format"`
}

// PricingEntry defines per-million-token costs for a model in USD.
type PricingEntry struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

// LoggingConfig configures Thane's structured filesystem log datasets,
// stdout policy, and SQLite-backed log/query retention.
type LoggingConfig struct {
	// Root is the directory where Thane writes category-partitioned JSONL
	// datasets and logs.db. Relative paths are resolved from the working
	// directory (typically ~/Thane). Defaults to "logs" when omitted.
	// Set to an explicit empty string (root: "") to disable filesystem
	// logging entirely.
	Root *string `yaml:"root"`

	// Dir is the deprecated alias for Root. It is kept for backwards
	// compatibility with older configs.
	Dir *string `yaml:"dir"`

	// Level sets the minimum level retained in the structured datasets and
	// SQLite log index. Valid values: trace, debug, info, warn, error.
	// Default: info.
	Level string `yaml:"level"`

	// Format sets the stdout log format fallback when stdout.format is
	// omitted. "json" produces one JSON object per line; "text" produces
	// human-readable key=value pairs. Default: json.
	Format string `yaml:"format"`

	// Compress is deprecated and has no effect on the dataset-backed
	// logging pipeline. The field is retained so existing YAML configs
	// still parse; app startup logs a warning when it is set explicitly
	// (see DeprecatedLoggingCompressSet).
	Compress *bool `yaml:"compress"`

	// Stdout configures the operator-facing stdout surface separately from
	// the structured filesystem datasets.
	Stdout LoggingStdoutConfig `yaml:"stdout"`

	// Datasets controls which structured filesystem datasets are written
	// under Root.
	Datasets LoggingDatasetsConfig `yaml:"datasets"`

	// RetentionDays controls how many days DEBUG and TRACE log index
	// entries are kept. Entries at INFO and above are kept indefinitely.
	// Default: 7. Set to 0 to disable pruning (keep everything).
	RetentionDays *int `yaml:"retention_days"`

	// RetainContent enables content retention in the log index database.
	// When true, system prompts (deduplicated by SHA-256 hash), tool call
	// arguments/results, and request/response content are persisted to
	// logs.db alongside the existing log index. Default: false.
	RetainContent bool `yaml:"retain_content"`

	// MaxContentLength is the maximum number of characters retained per
	// tool result or message body. Longer content is truncated. This
	// bounds storage growth while preserving enough for diagnostics.
	// Default: 4096. Set to 0 for unlimited.
	MaxContentLength *int `yaml:"max_content_length"`

	// ContentArchiveDays is the age threshold in days for archiving
	// log_request_content rows to JSONL flat files. Rows older than
	// this are exported to ContentArchiveDir and removed from logs.db.
	// Default: 90. Set to 0 to disable archival.
	ContentArchiveDays *int `yaml:"content_archive_days"`

	// ContentArchiveDir is the directory where monthly JSONL archive
	// files are written. Relative paths are resolved from the working
	// directory. Defaults to {logging.root}/archive when unset.
	ContentArchiveDir *string `yaml:"content_archive_dir"`
}

// LoggingStdoutConfig configures the operator-facing stdout stream.
type LoggingStdoutConfig struct {
	// Enabled controls whether Thane writes operator-facing logs to
	// stdout. Default: true.
	Enabled *bool `yaml:"enabled"`

	// Level sets the minimum stdout log level. When empty, it falls back
	// to Logging.Level.
	Level string `yaml:"level"`

	// Format sets stdout formatting. When empty, it falls back to
	// Logging.Format.
	Format string `yaml:"format"`
}

// LoggingDatasetsConfig configures the initial structured JSONL datasets
// written under logging.root.
type LoggingDatasetsConfig struct {
	Events    LoggingDatasetConfig `yaml:"events"`
	Requests  LoggingDatasetConfig `yaml:"requests"`
	Access    LoggingDatasetConfig `yaml:"access"`
	Loops     LoggingDatasetConfig `yaml:"loops"`
	Delegates LoggingDatasetConfig `yaml:"delegates"`
	Envelopes LoggingDatasetConfig `yaml:"envelopes"`
}

// LoggingDatasetConfig controls one structured JSONL dataset.
type LoggingDatasetConfig struct {
	// Enabled controls whether the dataset is written. When omitted, each
	// dataset uses its built-in default.
	Enabled *bool `yaml:"enabled"`
}

// RootPath returns the resolved logging root. When Root is nil and Dir is
// also nil, it returns the default "logs". When either is an explicit empty
// string, it returns "" which signals that filesystem logging is disabled.
func (l LoggingConfig) RootPath() string {
	if l.Root != nil {
		return *l.Root
	}
	if l.Dir != nil {
		return *l.Dir
	}
	return "logs"
}

// DirPath returns the resolved logging root path. It is kept as a
// compatibility alias for older callers that still speak in terms of a
// log directory rather than dataset root.
func (l LoggingConfig) DirPath() string {
	return l.RootPath()
}

// RetentionDaysDuration returns the retention period for low-level log
// index entries. When nil (omitted in YAML), defaults to 7 days. A zero
// or negative value disables pruning entirely.
func (l LoggingConfig) RetentionDaysDuration() time.Duration {
	days := 7
	if l.RetentionDays != nil {
		days = *l.RetentionDays
	}
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// ContentMaxLength returns the maximum character count for retained
// content fields. Defaults to 4096 when unset. A value of 0 means
// unlimited; negative values are treated as misconfiguration and
// clamped to the default (4096).
func (l LoggingConfig) ContentMaxLength() int {
	if l.MaxContentLength == nil {
		return 4096
	}
	if *l.MaxContentLength < 0 {
		return 4096
	}
	return *l.MaxContentLength
}

// ContentArchiveDirPath returns the resolved archive directory path.
// When ContentArchiveDir is nil (unset in YAML), it falls back to
// logDir/archive where logDir is the caller-supplied logging root.
func (l LoggingConfig) ContentArchiveDirPath(logDir string) string {
	if l.ContentArchiveDir != nil && *l.ContentArchiveDir != "" {
		return *l.ContentArchiveDir
	}
	return filepath.Join(logDir, "archive")
}

// ContentArchiveDuration returns the age threshold after which retained
// content rows should be archived to JSONL. Defaults to 90 days when
// unset. A value of 0 disables archival.
func (l LoggingConfig) ContentArchiveDuration() time.Duration {
	days := 90
	if l.ContentArchiveDays != nil {
		days = *l.ContentArchiveDays
	}
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// CompressEnabled returns whether rotated log compression is on.
// Defaults to true when Compress is nil (unset in YAML).
func (l LoggingConfig) CompressEnabled() bool {
	if l.Compress == nil {
		return true
	}
	return *l.Compress
}

// StdoutEnabled returns whether the operator-facing stdout stream is on.
// Defaults to true when stdout.enabled is omitted.
func (l LoggingConfig) StdoutEnabled() bool {
	if l.Stdout.Enabled == nil {
		return true
	}
	return *l.Stdout.Enabled
}

// StdoutLevelValue returns the configured stdout level or falls back to
// the dataset/index level.
func (l LoggingConfig) StdoutLevelValue() string {
	if strings.TrimSpace(l.Stdout.Level) != "" {
		return l.Stdout.Level
	}
	return l.Level
}

// StdoutFormatValue returns the configured stdout format or falls back to
// the logging default format.
func (l LoggingConfig) StdoutFormatValue() string {
	if strings.TrimSpace(l.Stdout.Format) != "" {
		return l.Stdout.Format
	}
	return l.Format
}

// DatasetEnabled reports whether a named structured dataset should be
// written under logging.root.
func (l LoggingConfig) DatasetEnabled(dataset string) bool {
	switch dataset {
	case "events":
		return datasetEnabled(l.Datasets.Events, true)
	case "requests":
		return datasetEnabled(l.Datasets.Requests, true)
	case "access":
		return datasetEnabled(l.Datasets.Access, false)
	case "loops":
		return datasetEnabled(l.Datasets.Loops, true)
	case "delegates":
		return datasetEnabled(l.Datasets.Delegates, true)
	case "envelopes":
		return datasetEnabled(l.Datasets.Envelopes, true)
	default:
		return false
	}
}

func datasetEnabled(cfg LoggingDatasetConfig, defaultValue bool) bool {
	if cfg.Enabled == nil {
		return defaultValue
	}
	return *cfg.Enabled
}

// DeprecatedFieldsUsed reports whether the legacy top-level log_level or
// log_format fields are set. Callers use this to emit deprecation warnings.
func (c *Config) DeprecatedFieldsUsed() (level, format bool) {
	return c.LogLevel != "", c.LogFormat != ""
}

// DeprecatedLoggingCompressSet reports whether logging.compress was set
// explicitly in the YAML config. It has no effect on the dataset-backed
// logging pipeline and only exists to keep old configs parseable; the
// app uses this signal to warn operators once on startup.
func (c *Config) DeprecatedLoggingCompressSet() bool {
	return c.Logging.Compress != nil
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

// CardDAVConfig configures the optional CardDAV server for native
// contact app sync.  When Enabled is true and credentials are set,
// Thane exposes a CardDAV endpoint that can be added as an account in
// macOS Contacts.app, iOS, Thunderbird, or any CardDAV client.
type CardDAVConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Listen   []string `yaml:"listen"`   // e.g. ["127.0.0.1:8843"]
	Username string   `yaml:"username"` // Basic Auth username
	Password string   `yaml:"password"` // Basic Auth password
}

// Configured reports whether the CardDAV server has all required
// settings.
func (c CardDAVConfig) Configured() bool {
	return c.Enabled && c.Username != "" && c.Password != ""
}

// PlatformConfig configures the WebSocket endpoint for native platform
// provider connections (e.g. macOS app). When enabled, providers can
// connect and register capabilities for bidirectional service dispatch.
//
// Each entry in Providers maps an account name (e.g. "nugget", "aimee")
// to a set of per-device tokens. Multiple devices under the same account
// share an identity but are independently addressable by client_id.
type PlatformConfig struct {
	Enabled   bool                              `yaml:"enabled"`
	Providers map[string]PlatformProviderConfig `yaml:"providers"`
}

// PlatformProviderConfig defines the tokens for a single account identity.
// Each token typically corresponds to a different device running a
// platform agent (e.g. thane-agent-macos on a laptop vs desktop).
type PlatformProviderConfig struct {
	Tokens []string `yaml:"tokens"`
}

// Configured reports whether the platform provider endpoint is enabled
// and has at least one provider with at least one token.
func (c PlatformConfig) Configured() bool {
	if !c.Enabled || len(c.Providers) == 0 {
		return false
	}
	for _, p := range c.Providers {
		if len(p.Tokens) > 0 {
			return true
		}
	}
	return false
}

// Validate checks platform configuration for internal consistency.
// When enabled, at least one provider must have a non-empty token, and
// tokens must not be shared across accounts.
func (c PlatformConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("platform: enabled but no providers configured")
	}
	hasToken := false
	seen := make(map[string]string) // token → account
	for account, p := range c.Providers {
		for _, tok := range p.Tokens {
			if tok == "" {
				continue
			}
			hasToken = true
			if prev, dup := seen[tok]; dup {
				return fmt.Errorf("platform: duplicate token shared by accounts %q and %q", prev, account)
			}
			seen[tok] = account
		}
	}
	if !hasToken {
		return fmt.Errorf("platform: enabled but no tokens configured for any provider")
	}
	return nil
}

// TokenIndex builds a map from token → account name for O(1) auth lookups.
func (c PlatformConfig) TokenIndex() map[string]string {
	idx := make(map[string]string)
	for account, p := range c.Providers {
		for _, tok := range p.Tokens {
			idx[tok] = account
		}
	}
	return idx
}

// IdentityConfig configures the agent's own contact identity. The
// ContactName must match a contact record in the directory to enable
// self-referencing operations like vCard export.
type IdentityConfig struct {
	// ContactName is the formatted name of the agent's own contact
	// record. When set, export_vcf name="self" resolves to this
	// contact.
	ContactName string `yaml:"contact_name"`

	// OwnerContactName is the formatted name of the primary human
	// owner/operator contact record. When set, the owner_contact tool
	// resolves directly to this contact instead of guessing from trust
	// zones. When empty, owner_contact falls back to the sole admin
	// contact if exactly one exists.
	OwnerContactName string `yaml:"owner_contact_name"`
}

// AttachmentsConfig configures content-addressed attachment storage.
type AttachmentsConfig struct {
	// StoreDir is the root directory for the content-addressed file
	// store. When set, received attachments are stored by SHA-256 hash
	// instead of being copied with their original filenames. The
	// metadata index is stored at {data_dir}/attachments.db.
	// Supports ~ expansion. This is a durable generated-artifact root,
	// not a hand-edited document root. Example:
	// ~/Thane/generated/attachments
	StoreDir string       `yaml:"store_dir"`
	Vision   VisionConfig `yaml:"vision"`
}

// VisionConfig configures automatic vision analysis of image
// attachments. When enabled, images are analyzed on ingest using a
// vision-capable LLM and the resulting description is cached in the
// attachment metadata index.
type VisionConfig struct {
	Enabled bool   `yaml:"enabled"` // enable auto-analysis on image ingest
	Model   string `yaml:"model"`   // vision model name (must be in models.available)
	Prompt  string `yaml:"prompt"`  // custom analysis prompt; empty uses default
	Timeout string `yaml:"timeout"` // per-image timeout (Go duration); empty → 30s
}

// ParsedTimeout returns the configured timeout as a [time.Duration],
// defaulting to 30 seconds when empty. Invalid durations are caught
// by [Config.Validate]; this method assumes the value is already
// validated and falls back to the default on any parse error.
func (v VisionConfig) ParsedTimeout() time.Duration {
	if v.Timeout == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(v.Timeout)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// ProvenanceConfig configures git-backed file storage with SSH
// signature enforcement. When both Path and SigningKey are set, files
// are automatically committed with cryptographic signatures on every
// write.
type ProvenanceConfig struct {
	// Path is the directory for the provenance git repository.
	// Supports ~ expansion. This is a legacy seam toward future
	// integrity-tracked document roots and no longer defines the fixed
	// workspace/core locations of always-on identity files. Example:
	// ~/Thane/core
	Path string `yaml:"path"`

	// SigningKey is the path to an SSH private key used to sign
	// commits. The key is loaded at startup and held in memory.
	// Supports ~ expansion. Example: ~/.ssh/id_ed25519
	SigningKey string `yaml:"signing_key"`
}

// Configured reports whether the provenance store has both a path and
// signing key set.
func (c ProvenanceConfig) Configured() bool {
	return c.Path != "" && c.SigningKey != ""
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

	// OllamaURL is backward-compatible shorthand for a default Ollama
	// resource. It is used when Resources is empty. When Resources is
	// populated, callers should prefer the normalized resource catalog.
	OllamaURL string `yaml:"ollama_url"`

	// Resources defines named model provider resources such as Ollama
	// instances running on different machines. When empty, OllamaURL is
	// treated as a synthetic resource named "default".
	Resources map[string]ModelServerConfig `yaml:"resources"`

	// LocalFirst prefers local (cost_tier=0) models over cloud models
	// when routing decisions are made by the model router.
	LocalFirst bool `yaml:"local_first"`

	// RecoveryModel is a fast, cheap model used to generate summaries
	// when the primary model times out after completing tool calls.
	// When empty, timeout recovery falls back to a static message
	// listing the tools that were used.
	RecoveryModel string `yaml:"recovery_model"`

	// Available lists all models that Thane can route to. Each entry
	// maps a model name to a provider and declares its capabilities.
	Available []ModelConfig `yaml:"available"`
}

// ModelConfig describes a single LLM model's identity and capabilities.
// The model router uses these fields to select the best model for each
// request.
type ModelConfig struct {
	Name              string `yaml:"name"`               // Model identifier (e.g., "claude-opus-4-20250514")
	Provider          string `yaml:"provider"`           // Provider name: ollama, anthropic, lmstudio. Defaults to ollama when no resource is set
	Resource          string `yaml:"resource"`           // Named provider resource from models.resources for this deployment
	SupportsTools     bool   `yaml:"supports_tools"`     // Optional per-deployment tool-use override. When omitted, runtime/provider capability is used.
	SupportsStreaming *bool  `yaml:"supports_streaming"` // Optional per-deployment streaming override. Nil inherits observed runtime/provider capability.
	ContextWindow     int    `yaml:"context_window"`     // Optional per-deployment context-window override. Zero inherits observed runtime metadata.
	Speed             int    `yaml:"speed"`              // Relative speed rating, 1 (slow) to 10 (fast)
	Quality           int    `yaml:"quality"`            // Relative quality rating, 1 (low) to 10 (high)
	CostTier          int    `yaml:"cost_tier"`          // 0=local/free, 1=cheap, 2=moderate, 3=expensive
	MinComplexity     string `yaml:"min_complexity"`     // Minimum task complexity: simple, moderate, complex

	supportsToolsSet bool `yaml:"-"`
}

// SupportsToolsOverride reports whether supports_tools was explicitly
// set in config, returning the configured value when present.
func (m ModelConfig) SupportsToolsOverride() (*bool, bool) {
	if !m.supportsToolsSet {
		return nil, false
	}
	value := m.SupportsTools
	return &value, true
}

// UnmarshalYAML preserves whether optional override fields were
// explicitly authored in config so later layers can distinguish
// operator policy from omitted defaults.
func (m *ModelConfig) UnmarshalYAML(node *yaml.Node) error {
	type raw ModelConfig
	var decoded raw
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*m = ModelConfig(decoded)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := strings.TrimSpace(node.Content[i].Value)
		if key == "supports_tools" {
			m.supportsToolsSet = true
			break
		}
	}
	return nil
}

// ModelServerConfig describes a named model provider resource.
type ModelServerConfig struct {
	URL string `yaml:"url"`
	// Provider name for this resource. Default: ollama.
	Provider string `yaml:"provider"`
	// APIKey is an optional bearer/API key for providers that require auth.
	APIKey string `yaml:"api_key"`
	// IdleTTLSeconds asks supported local runners to keep models warm for
	// this many idle seconds after an inference request. LM Studio honors
	// this via the native `ttl` request field on inference endpoints.
	// Zero lets the runner use its default behavior.
	IdleTTLSeconds int `yaml:"idle_ttl_seconds"`
}

// PreferredOllamaURL returns the best available Ollama URL for callers
// that still need one local endpoint outside the routed model catalog.
// Preference order is: a resource named "default", then the first
// configured Ollama resource by name, then the legacy OllamaURL field.
func (c ModelsConfig) PreferredOllamaURL() string {
	if len(c.Resources) > 0 {
		if srv, ok := c.Resources["default"]; ok && srv.Provider == "ollama" && srv.URL != "" {
			return srv.URL
		}
		names := make([]string, 0, len(c.Resources))
		for name := range c.Resources {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			srv := c.Resources[name]
			if srv.Provider == "" {
				srv.Provider = "ollama"
			}
			if srv.Provider == "ollama" && srv.URL != "" {
				return srv.URL
			}
		}
	}
	return c.OllamaURL
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
// recall features. When Enabled is false, Thane still stores facts and
// contacts, but vector-backed lookup and similarity search are disabled
// for those stores and related ingest paths.
type EmbeddingsConfig struct {
	// Enabled controls whether Thane generates embeddings at ingest and
	// lookup time for semantic search and related recall paths.
	Enabled bool `yaml:"enabled"`

	// Model is the embedding model name. Default: "nomic-embed-text".
	Model string `yaml:"model"`

	// BaseURL overrides the Ollama endpoint used for embeddings. Empty
	// falls back to the default model resource/provider selection.
	BaseURL string `yaml:"baseurl"`
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

	// SessionIdleMinutes is the backstop idle timeout for the
	// summarizer worker. Active sessions with no message activity
	// for this many minutes are silently closed and become eligible
	// for summarization. This complements the channel-specific idle
	// check (e.g. signal.session_idle_minutes) which sends farewell
	// messages.
	//
	// Pointer type distinguishes "omitted" (nil → inherit from
	// signal.session_idle_minutes) from "explicitly set to 0"
	// (disabled). A positive value overrides the inherited default.
	SessionIdleMinutes *int `yaml:"session_idle_minutes"`
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
	// extraction is attempted. Very short exchanges rarely contain knowledge.
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
	// file injection is disabled. Prefer a generated/provenance-aware
	// root (for example ~/Thane/generated/daily) over legacy shared
	// application state directories.
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

	// DelegationRequired enables orchestrator tool gating. When false
	// (the default), all tools are available on every iteration.
	DelegationRequired bool `yaml:"delegation_required"`
}

// DelegateConfig configures the thane_delegate tool's split-model
// execution behavior.
type DelegateConfig struct {
	// Profiles contains per-profile overrides. The map key is the
	// profile name (e.g., "general", "ha"). Only fields that are set
	// override the builtin defaults — omitted fields keep their
	// compiled-in values.
	Profiles map[string]DelegateProfileConfig `yaml:"profiles"`
}

// DelegateProfileConfig holds configurable overrides for a delegate
// profile. Zero-value fields are ignored (builtin defaults apply).
type DelegateProfileConfig struct {
	// ToolTimeout is the maximum time a single tool call may run
	// before being cancelled. Accepts Go duration strings (e.g.,
	// "30s", "3m", "5m"). Zero keeps the builtin default (30s).
	ToolTimeout time.Duration `yaml:"tool_timeout"`

	// MaxDuration is the maximum wall clock time for the entire
	// delegation loop. Zero keeps the builtin default (90s).
	MaxDuration time.Duration `yaml:"max_duration"`

	// MaxIter is the maximum number of tool-calling iterations.
	// Zero keeps the builtin default (15).
	MaxIter int `yaml:"max_iter"`

	// MaxTokens is the maximum cumulative output tokens before
	// budget exhaustion. Zero keeps the builtin default (25000).
	MaxTokens int `yaml:"max_tokens"`
}

// CapabilityTagConfig defines a named group of tools (and optionally
// talents) that can be loaded together. For compiled-in tags, empty
// Description and Tools act as "keep the built-in defaults". Tags
// marked AlwaysActive are included in every session unconditionally.
// Tags marked Protected are runtime-asserted and cannot be manually
// activated or deactivated by the model.
type CapabilityTagConfig struct {
	// Description is a human-readable summary shown in the capability
	// manifest so the agent knows what activating this tag provides. For
	// compiled-in tags, empty keeps the built-in description.
	Description string `yaml:"description"`

	// Tools lists the tool names belonging to this tag. A tool can
	// appear in multiple tags; it loads when any of its tags is active.
	//
	// For compiled-in tags, empty keeps the built-in tool membership.
	// Prefer leaving this empty for built-in or MCP-backed tags unless
	// you intentionally want to replace the compiled/operator-derived
	// membership with an explicit list.
	Tools []string `yaml:"tools"`

	// AlwaysActive tags cannot be deactivated. They are included in
	// every session regardless of channel or agent requests.
	AlwaysActive bool `yaml:"always_active"`

	// Protected tags are reserved for runtime trust and environment
	// assertions (for example an owner-authenticated conversation).
	// They are visible to the model when active, but cannot be toggled
	// via activate_capability or deactivate_capability.
	Protected bool `yaml:"protected"`
}

// Validate checks that the capability tag configuration is internally
// consistent. It ensures a description is present and the tools list is
// non-empty. Tag names are validated by the caller since they are map
// keys in the parent Config struct.
func (c CapabilityTagConfig) Validate(tagName string, builtin bool) error {
	if strings.TrimSpace(c.Description) == "" && !builtin {
		return fmt.Errorf("capability_tags.%s.description must not be empty", tagName)
	}
	if len(c.Tools) == 0 && !builtin {
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
	// tools are disabled entirely. In multi-root setups, this should be
	// the common writable parent that contains Thane-owned roots such as
	// core/, talents/, knowledge/, generated/, and scratchpad/.
	Path string `yaml:"path"`

	// ReadOnlyDirs are additional directories the agent can read from
	// but not write to. Useful for compatibility or reference roots that
	// must remain outside Thane's writable authority, such as a legacy
	// workspace or an external vault mirror.
	ReadOnlyDirs []string `yaml:"read_only_dirs"`
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
	// acted upon. Supports MQTT wildcard characters (+ and #).
	Subscriptions []SubscriptionConfig `yaml:"subscriptions"`

	// Telemetry configures operational metric publishing. When enabled,
	// a separate mqtt-telemetry loop publishes system health, token usage,
	// loop states, and other operational data as native HA sensors.
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

// SubscriptionConfig describes a single MQTT topic subscription.
// Each entry is subscribed on every broker (re-)connect. Wildcards
// (+ and #) are supported per the MQTT specification.
type SubscriptionConfig struct {
	// Topic is the MQTT topic filter (e.g., "homeassistant/+/+/state",
	// "frigate/events"). Supports MQTT wildcard characters.
	Topic string `yaml:"topic"`

	// Wake, when non-nil, enables agent wake on this topic. Messages
	// arriving on the topic trigger an agent conversation using the
	// profile's routing configuration. When nil, messages are received
	// for ambient awareness only (debug-logged, not acted upon).
	Wake *router.LoopProfile `yaml:"wake,omitempty"`
}

// TelemetryConfig configures MQTT telemetry publishing. When Enabled
// is true and MQTT is configured, a dedicated loop publishes
// operational metrics (DB sizes, token usage, loop states, etc.) as
// native Home Assistant sensors via MQTT Discovery.
type TelemetryConfig struct {
	// Enabled activates the mqtt-telemetry loop. Requires MQTT to be
	// configured (broker + device_name).
	Enabled bool `yaml:"enabled"`

	// Interval is how often (in seconds) telemetry metrics are
	// collected and published. Default: 60. Minimum: 10.
	Interval int `yaml:"interval"`
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

// DebugConfig configures diagnostic options for development and testing.
type DebugConfig struct {
	// DemoLoops spawns simulated loops covering all visual variants
	// (categories, parent/child, error states, node churn) so the
	// dashboard can be iterated on without real service dependencies.
	DemoLoops bool `yaml:"demo_loops"`
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

	// DefaultTags lists tool tags/toolsets assigned to bridged MCP tools
	// from this server unless a per-tool override replaces them. Prefer
	// using this to attach MCP tools to existing capability/toolbox
	// groups instead of hand-maintaining every bridged tool name inside
	// capability_tags.*.tools.
	DefaultTags []string `yaml:"default_tags"`

	// Tools contains optional metadata overrides keyed by the raw MCP tool
	// name reported by the server.
	Tools map[string]MCPToolConfig `yaml:"tools"`
}

// MCPToolConfig configures operator-supplied metadata for a bridged MCP tool.
type MCPToolConfig struct {
	// Enabled controls whether the tool is bridged. Nil keeps the default
	// include/exclude behavior.
	Enabled *bool `yaml:"enabled"`

	// Tags replaces the server default tags for this tool when non-empty.
	Tags []string `yaml:"tags"`

	// Description overrides the description reported by the MCP server.
	Description string `yaml:"description"`
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

	// HandleTimeout bounds how long a single inbound message may be
	// processed (agent loop + response send). This needs to be long
	// enough to cover tool execution (e.g., media_transcript) plus
	// the subsequent LLM response. Default: 10m.
	HandleTimeout time.Duration `yaml:"handle_timeout"`
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

// LoopProfile converts the Signal routing config into the shared
// LoopProfile representation used by wake-style entrypoints.
//
// It intentionally maps only the fields exposed by SignalRoutingConfig.
// LoopProfile-only fields such as ExcludeTools and InitialTags are omitted
// until Signal grows explicit config for them.
func (c SignalRoutingConfig) LoopProfile() router.LoopProfile {
	return router.LoopProfile{
		Model:            c.Model,
		QualityFloor:     c.QualityFloor,
		Mission:          c.Mission,
		DelegationGating: c.DelegationGating,
	}
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
	// conversation excerpts alongside Layer 1 knowledge. See issue #404.
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
	// Mutually exclusive with CookiesFromBrowser.
	CookiesFile string `yaml:"cookies_file"`

	// CookiesFromBrowser extracts cookies directly from an installed
	// browser, eliminating the need for manual cookie file export.
	// Value is passed to yt-dlp's --cookies-from-browser flag.
	// Examples: "chrome", "firefox", "chrome:Profile 1".
	// Mutually exclusive with CookiesFile.
	CookiesFromBrowser string `yaml:"cookies_from_browser"`

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
	// This is typically a generated/artifact root rather than a curated
	// knowledge root.
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

	// Analysis configures the structured media analysis pipeline
	// that writes analysis output to an Obsidian-compatible vault.
	Analysis AnalysisConfig `yaml:"analysis"`
}

// AnalysisConfig configures the media analysis pipeline that produces
// structured markdown output in an Obsidian-compatible vault. Each feed
// can override the output path; otherwise the default is used.
type AnalysisConfig struct {
	// DefaultOutputPath is the base directory for analysis output when
	// a feed has no per-feed output_path configured. Supports ~ expansion.
	// Example: ~/Sync/Aimee/Vault/Media
	DefaultOutputPath string `yaml:"default_output_path"`

	// DatabasePath is the SQLite database file for engagement tracking.
	// If empty, defaults to {data_dir}/media_engagement.db at startup.
	DatabasePath string `yaml:"database_path"`
}

// MetacognitiveConfig configures the self-regulating metacognitive loop.
// The loop runs perpetually in a background goroutine, using LLM calls to
// reason about the environment and self-determine its sleep duration
// between iterations. See issue #319.
type MetacognitiveConfig struct {
	// Enabled controls whether the metacognitive loop starts. Default: false.
	Enabled bool `yaml:"enabled"`

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

// LoopsConfig configures immutable loops-ng definitions loaded from the
// config file.
type LoopsConfig struct {
	// MaxRunning caps the number of concurrently running loops across
	// the live registry. Zero means unlimited.
	MaxRunning int `yaml:"max_running"`

	// Definitions is the set of config-defined loop specs. These specs
	// are immutable at runtime; dynamic loop creation lives in the
	// persistent overlay registry instead.
	Definitions []looppkg.Spec `yaml:"definitions"`
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
	// --- Logging migration: deprecated top-level fields → Logging struct ---
	// If the user still has log_level / log_format at the top level and
	// hasn't set the new Logging.Level / Logging.Format, migrate silently.
	// The deprecated fields are validated later and warned at startup.
	if c.Logging.Level == "" && c.LogLevel != "" {
		c.Logging.Level = c.LogLevel
	}
	if c.Logging.Format == "" && c.LogFormat != "" {
		c.Logging.Format = c.LogFormat
	}

	// Root/Dir use *string pointers — nil defaults to "logs" via
	// RootPath()/DirPath(). Explicit empty string disables filesystem
	// logging entirely.
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Logging.Stdout.Level == "" {
		c.Logging.Stdout.Level = c.Logging.Level
	}
	if c.Logging.Stdout.Format == "" {
		c.Logging.Stdout.Format = c.Logging.Format
	}
	// Intentionally do not default Logging.Compress. The field is
	// deprecated and has no effect on the dataset-backed logging
	// pipeline; leaving it nil lets DeprecatedLoggingCompressSet
	// report accurately whether the user set it in their YAML.
	//
	// Note: we intentionally do NOT back-sync Logging.Format → LogFormat.
	// The deprecated fields are only populated if the user's YAML set them.
	// DeprecatedFieldsUsed() relies on that to emit warnings accurately.

	if c.Listen.Port == 0 {
		c.Listen.Port = 8080
	}
	if c.DataDir == "" {
		c.DataDir = "./db"
	}
	if c.TalentsDir == "" {
		c.TalentsDir = "./talents"
	}
	if c.Models.OllamaURL == "" && len(c.Models.Resources) == 0 {
		c.Models.OllamaURL = "http://localhost:11434"
	}
	for name, srv := range c.Models.Resources {
		srv.Provider = strings.ToLower(strings.TrimSpace(srv.Provider))
		if srv.Provider == "" {
			srv.Provider = "ollama"
		}
		c.Models.Resources[name] = srv
	}
	if c.OllamaAPI.Port == 0 {
		c.OllamaAPI.Port = 11434
	}
	if c.CardDAV.Enabled && len(c.CardDAV.Listen) == 0 {
		c.CardDAV.Listen = []string{"127.0.0.1:8843"}
	}
	if c.Embeddings.Model == "" {
		c.Embeddings.Model = "nomic-embed-text"
	}
	if c.Embeddings.BaseURL == "" {
		c.Embeddings.BaseURL = c.Models.PreferredOllamaURL()
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
	// The archive idle timeout is a crash-recovery backstop (silent close
	// via DB timestamps). The signal idle timeout is the interactive path
	// (farewell message on next inbound). Inherit when omitted (nil) so
	// users only need to set signal.session_idle_minutes for both to work.
	// Explicit 0 disables the backstop without affecting the signal path.
	if c.Signal.HandleTimeout == 0 {
		c.Signal.HandleTimeout = 10 * time.Minute
	}
	if c.Archive.SessionIdleMinutes == nil && c.Signal.SessionIdleMinutes > 0 {
		v := c.Signal.SessionIdleMinutes
		c.Archive.SessionIdleMinutes = &v
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
	if c.MQTT.Telemetry.Interval == 0 {
		c.MQTT.Telemetry.Interval = 60
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
	if c.Media.Analysis.DatabasePath == "" {
		c.Media.Analysis.DatabasePath = filepath.Join(c.DataDir, "media_engagement.db")
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

	if c.Agent.DelegationRequired && len(c.Agent.OrchestratorTools) == 0 {
		c.Agent.OrchestratorTools = []string{
			"thane_delegate",
			"recall_fact",
			"remember_fact",
			"save_contact",
			"lookup_contact",
			"owner_contact",
			"session_working_memory",
			"session_close",
			"archive_search",
		}
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
		if c.Models.Available[i].Provider == "" && c.Models.Available[i].Resource == "" {
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
	if c.CardDAV.Enabled {
		if c.CardDAV.Username == "" {
			return fmt.Errorf("carddav.username required when carddav.enabled is true")
		}
		if c.CardDAV.Password == "" {
			return fmt.Errorf("carddav.password required when carddav.enabled is true")
		}
		if len(c.CardDAV.Listen) == 0 {
			return fmt.Errorf("carddav.listen requires at least one address")
		}
		for _, addr := range c.CardDAV.Listen {
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return fmt.Errorf("carddav.listen %q: %w", addr, err)
			}
		}
	}
	// Validate logging — both new and deprecated fields.
	if c.Logging.Level != "" {
		if _, err := ParseLogLevel(c.Logging.Level); err != nil {
			return fmt.Errorf("logging.level: %w", err)
		}
	}
	if c.Logging.Stdout.Level != "" {
		if _, err := ParseLogLevel(c.Logging.Stdout.Level); err != nil {
			return fmt.Errorf("logging.stdout.level: %w", err)
		}
	}
	switch c.Logging.Format {
	case "text", "json", "":
		// valid
	default:
		return fmt.Errorf("logging.format %q invalid (expected text or json)", c.Logging.Format)
	}
	switch c.Logging.Stdout.Format {
	case "text", "json", "":
		// valid
	default:
		return fmt.Errorf("logging.stdout.format %q invalid (expected text or json)", c.Logging.Stdout.Format)
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
		if c.MQTT.Telemetry.Enabled && c.MQTT.Telemetry.Interval < 10 {
			return fmt.Errorf("mqtt.telemetry.interval %d too low (minimum 10 seconds)", c.MQTT.Telemetry.Interval)
		}
	}
	if c.Media.CookiesFile != "" && c.Media.CookiesFromBrowser != "" {
		return fmt.Errorf("media: cookies_file and cookies_from_browser are mutually exclusive")
	}
	if err := c.validateSubscribe(); err != nil {
		return err
	}
	if err := c.validateMCP(); err != nil {
		return err
	}
	allowedTags := make(map[string]bool)
	for tagName := range toolcatalog.BuiltinTagSpecs() {
		allowedTags[tagName] = true
	}
	for _, srv := range c.MCP.Servers {
		for _, tag := range srv.DefaultTags {
			if trimmed := strings.TrimSpace(tag); trimmed != "" {
				allowedTags[trimmed] = true
			}
		}
		for _, toolCfg := range srv.Tools {
			for _, tag := range toolCfg.Tags {
				if trimmed := strings.TrimSpace(tag); trimmed != "" {
					allowedTags[trimmed] = true
				}
			}
		}
	}
	for tagName, tagCfg := range c.CapabilityTags {
		builtin := toolcatalog.HasBuiltinTag(tagName) || allowedTags[tagName]
		if err := tagCfg.Validate(tagName, builtin); err != nil {
			return err
		}
		allowedTags[tagName] = true
	}
	for channel, tagNames := range c.ChannelTags {
		for _, tagName := range tagNames {
			if !allowedTags[tagName] {
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
	if c.Archive.SessionIdleMinutes != nil && *c.Archive.SessionIdleMinutes < 0 {
		return fmt.Errorf("archive.session_idle_minutes %d must be non-negative", *c.Archive.SessionIdleMinutes)
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
	if c.Attachments.Vision.Enabled {
		if c.Attachments.StoreDir == "" {
			return fmt.Errorf("attachments.store_dir required when attachments.vision.enabled is true")
		}
		if c.Attachments.Vision.Model == "" {
			return fmt.Errorf("attachments.vision.model required when attachments.vision.enabled is true")
		}
		if c.Attachments.Vision.Timeout != "" {
			if _, err := time.ParseDuration(c.Attachments.Vision.Timeout); err != nil {
				return fmt.Errorf("attachments.vision.timeout %q: %w", c.Attachments.Vision.Timeout, err)
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
	if c.Provenance.Path != "" && c.Provenance.SigningKey == "" {
		return fmt.Errorf("provenance.signing_key is required when provenance.path is set")
	}
	if c.Provenance.SigningKey != "" && c.Provenance.Path == "" {
		return fmt.Errorf("provenance.path is required when provenance.signing_key is set")
	}
	if err := c.validateMetacognitive(); err != nil {
		return err
	}
	if err := c.validateLoops(); err != nil {
		return err
	}
	if err := c.validateDelegate(); err != nil {
		return err
	}
	if err := c.Platform.Validate(); err != nil {
		return err
	}
	if err := c.validateModels(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateModels() error {
	for name, srv := range c.Models.Resources {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("models.resources contains an empty resource name")
		}
		if strings.TrimSpace(srv.URL) == "" {
			return fmt.Errorf("models.resources.%s.url is required", name)
		}
		if srv.IdleTTLSeconds < 0 {
			return fmt.Errorf("models.resources.%s.idle_ttl_seconds must be >= 0", name)
		}
	}
	for i, m := range c.Models.Available {
		if strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("models.available[%d].name must not be empty", i)
		}
		provider := strings.ToLower(strings.TrimSpace(m.Provider))
		if m.Resource != "" {
			srv, ok := c.Models.Resources[m.Resource]
			if !ok {
				return fmt.Errorf("models.available[%d] (%s): unknown resource %q", i, m.Name, m.Resource)
			}
			if provider != "" && provider != srv.Provider {
				return fmt.Errorf("models.available[%d] (%s): provider %q conflicts with resource %q provider %q", i, m.Name, m.Provider, m.Resource, srv.Provider)
			}
		}
		switch m.MinComplexity {
		case "", "simple", "moderate", "complex":
		default:
			return fmt.Errorf("models.available[%d] (%s): min_complexity %q invalid (expected simple, moderate, complex)", i, m.Name, m.MinComplexity)
		}
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
		return fmt.Errorf("metacognitive requires workspace.path (state file lives under workspace/core)")
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

// CoreRoot returns the fixed high-integrity core document root derived
// from [Workspace.Path]. When workspace.path is unset, CoreRoot returns
// the empty string.
func (c *Config) CoreRoot() string {
	if strings.TrimSpace(c.Workspace.Path) == "" {
		return ""
	}
	return filepath.Join(c.Workspace.Path, "core")
}

// CoreFile returns the absolute-or-relative path to a named file in the
// fixed core document root. When workspace.path is unset, CoreFile
// returns the empty string.
func (c *Config) CoreFile(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	root := c.CoreRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, name)
}

// CoreInjectFiles returns the curated always-on files that should be
// re-read and injected into the system prompt on every turn.
func (c *Config) CoreInjectFiles() []string {
	mission := c.CoreFile("mission.md")
	if mission == "" {
		return nil
	}
	return []string{mission}
}

func (c *Config) validateLoops() error {
	if c.Loops.MaxRunning < 0 {
		return fmt.Errorf("loops.max_running must be >= 0, got %d", c.Loops.MaxRunning)
	}
	if len(c.Loops.Definitions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(c.Loops.Definitions))
	for i, spec := range c.Loops.Definitions {
		if err := spec.ValidatePersistable(); err != nil {
			return fmt.Errorf("loops.definitions[%d]: %w", i, err)
		}
		if _, exists := seen[spec.Name]; exists {
			return fmt.Errorf("loops.definitions[%d]: duplicate definition %q", i, spec.Name)
		}
		seen[spec.Name] = struct{}{}
	}
	return nil
}

// validateDelegate checks delegate profile overrides for invalid values.
func (c *Config) validateDelegate() error {
	for name, p := range c.Delegate.Profiles {
		if p.ToolTimeout < 0 {
			return fmt.Errorf("delegate.profiles.%s.tool_timeout must be >= 0, got %s", name, p.ToolTimeout)
		}
		if p.MaxDuration < 0 {
			return fmt.Errorf("delegate.profiles.%s.max_duration must be >= 0, got %s", name, p.MaxDuration)
		}
		if p.MaxIter < 0 {
			return fmt.Errorf("delegate.profiles.%s.max_iter must be >= 0, got %d", name, p.MaxIter)
		}
		if p.MaxTokens < 0 {
			return fmt.Errorf("delegate.profiles.%s.max_tokens must be >= 0, got %d", name, p.MaxTokens)
		}
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
	if c.Signal.HandleTimeout < 0 {
		return fmt.Errorf("signal.handle_timeout %s must be non-negative", c.Signal.HandleTimeout)
	}
	profile := c.Signal.Routing.LoopProfile()
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("signal.routing: %w", err)
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
		if m.Resource != "" && m.Resource+"/"+m.Name == name {
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
