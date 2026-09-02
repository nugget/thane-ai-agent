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
//	go generate ./internal/platform/config
//
//go:generate go run ./gen/gencfg -srcdir . -out ../../../examples/config.example.yaml
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/integrations/search"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// DefaultWorkspace is the workspace directory used when none is given
// on the command line. It matches the role-account convention the
// installer and the packaged deployment both use.
const DefaultWorkspace = "~/Thane"

// ConfigFileName is the fixed name of the runtime config inside core.
const ConfigFileName = "config.yaml"

// CoreRootName, SelfRootName, ContactsRootName, and DossiersRootName are
// document roots whose paths are derived from the workspace rather than
// declared. They may be named in roots: to set policy, and none takes a path
// from there.
const (
	CoreRootName     = "core"
	SelfRootName     = "self"
	ContactsRootName = "contacts"
	DossiersRootName = "dossiers"
)

// legacyConfigLocations are the paths Thane searched before the config
// moved inside the trust boundary. They are no longer loaded; they are
// probed only so a failure to find the real config can say where the old
// one is and how to move it.
//
// The instance's own workspace comes first. That is where an existing
// instance's config actually sits, and it is the one location the fixed
// list cannot express: a workspace given with -workspace matches neither
// the working directory nor ~/Thane, so without it the migration
// instructions would go missing in precisely the case that needs them.
func legacyConfigLocations(workspace string) []string {
	var paths []string
	if abs, err := ExpandWorkspace(workspace); err == nil {
		paths = append(paths, filepath.Join(abs, ConfigFileName))
	}
	paths = append(paths, ConfigFileName)
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, "Thane", ConfigFileName),
			filepath.Join(home, ".config", "thane", ConfigFileName),
		)
	}
	return append(paths,
		filepath.Join("/config", ConfigFileName),
		filepath.Join("/usr/local/etc/thane", ConfigFileName),
		filepath.Join("/etc/thane", ConfigFileName),
	)
}

// ExpandWorkspace resolves a workspace argument to an absolute path,
// expanding a leading ~ and defaulting when empty.
func ExpandWorkspace(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	if workspace == "~" || strings.HasPrefix(workspace, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand %q: no home directory: %w", workspace, err)
		}
		workspace = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(workspace, "~"), "/"))
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace %q: %w", workspace, err)
	}
	return abs, nil
}

// ConfigPathForWorkspace returns the one location Thane loads runtime
// config from: {workspace}/core/config.yaml.
//
// There is deliberately no search. The config names the allowed-signers
// source, sets verify_signatures, chooses model endpoints, and points
// every document root at a path — so it decides what the rest of the
// system is willing to trust. A trust anchor discovered by probing the
// filesystem is not an anchor: whichever file is found first becomes
// authoritative, and the answer changes with the working directory. One
// derived location means the config always has a name, which is the
// precondition for verifying it.
func ConfigPathForWorkspace(workspace string) (string, error) {
	abs, err := ExpandWorkspace(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, "core", ConfigFileName), nil
}

// FindConfig returns the config path for a workspace, or the explicit
// path when one is given (the -insecure-config escape hatch, whose
// caller is responsible for degrading the runtime accordingly).
//
// When the canonical config is absent, the error names any config left
// at a legacy location and gives the exact commands to adopt it, so the
// migration is one unambiguous step rather than a hunt.
func FindConfig(explicit, workspace string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file not found: %s", explicit)
		}
		return explicit, nil
	}

	canonical, err := ConfigPathForWorkspace(workspace)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(canonical); err == nil {
		return canonical, nil
	}

	if legacy := firstExistingLegacyConfig(workspace); legacy != "" {
		coreDir := filepath.Dir(canonical)
		return "", fmt.Errorf(
			"no config at %s\n\nA config still exists at the pre-core location %s. Thane now loads config only from inside the instance trust boundary, so it can be signed and version-controlled. Move it and commit it:\n\n  mkdir -p %s\n  git -C %s init    # if core is not a repo yet\n  mv %s %s\n  git -C %s add %s && git -C %s commit -S -m 'adopt runtime config into core'",
			canonical, legacy,
			coreDir,
			coreDir,
			legacy, canonical,
			coreDir, ConfigFileName, coreDir,
		)
	}

	return "", fmt.Errorf("no config at %s (run 'thane init %s' to create one, or pass -workspace to point at a different instance)",
		canonical, filepath.Dir(filepath.Dir(canonical)))
}

// firstExistingLegacyConfig reports the first pre-core config location
// that still holds a file, or empty when none do.
func firstExistingLegacyConfig(workspace string) string {
	for _, p := range legacyConfigLocations(workspace) {
		if _, err := os.Stat(p); err == nil {
			abs, absErr := filepath.Abs(p)
			if absErr != nil {
				return p
			}
			return abs
		}
	}
	return ""
}

// Config is the top-level configuration structure for the Thane agent,
// loaded from a single YAML file via [Load]. The struct hierarchy maps
// directly to the YAML key hierarchy; the generated example config
// (examples/config.example.yaml) is derived from these struct definitions
// and their field comments via `go generate ./internal/platform/config`.
//
// After [Load] returns, all fields carry usable values -- callers never
// need empty-string checks or fallback logic. See [Config.applyDefaults]
// for the defaulting rules and [Config.Validate] for consistency checks.
type Config struct {
	// loadedFrom is the path this config was read from. It is not a
	// configurable value — it records provenance, and workspace.path is
	// derived from it, so the runtime knows where its instance lives
	// without trusting the file to say.
	loadedFrom string

	// unverified marks a config loaded from outside the instance trust
	// boundary. It is set by the loader rather than declared, because a
	// config cannot be trusted to describe its own trustworthiness.
	unverified bool

	// Listen configures the primary HTTP API server (OpenAI-compatible).
	Listen ListenConfig `yaml:"listen"`

	// OllamaAPI configures the optional Ollama-compatible API server,
	// used for Home Assistant integration.
	OllamaAPI OllamaAPIConfig `yaml:"ollama_api"`

	// OpenAIAPI configures the optional OpenAI-compatible API server,
	// serving the frozen OpenAI shim on its own port (separate from the
	// Thane-native /v1 API on the primary listen port).
	OpenAIAPI OpenAIAPIConfig `yaml:"openai_api"`

	// TLS configures the optional HTTPS front door: in-process TLS
	// termination with certificates from certmagic, routing each
	// configured hostname to one of the surfaces above.
	TLS TLSConfig `yaml:"tls"`

	// CardDAV configures the optional CardDAV server for native
	// contact app sync (macOS Contacts.app, iOS, Thunderbird, etc.).
	CardDAV CardDAVConfig `yaml:"carddav"`

	// Companion configures native companion app connections.
	Companion CompanionConfig `yaml:"companion"`

	// MemoryGuard watches process memory and restarts thane before a leak
	// can OOM the host (writing a heap profile first). Opt-in.
	MemoryGuard MemoryGuardConfig `yaml:"memory_guard"`

	// HomeAssistant configures the connection to a Home Assistant instance.
	HomeAssistant HomeAssistantConfig `yaml:"homeassistant"`

	// Models configures LLM providers, model routing, and the default model.
	Models ModelsConfig `yaml:"models"`

	// Anthropic configures the Anthropic (Claude) API provider.
	Anthropic AnthropicConfig `yaml:"anthropic"`

	// Embeddings configures vector embedding generation for semantic search.
	Embeddings EmbeddingsConfig `yaml:"embeddings"`

	// Workspace configures the agent's sandboxed file system access.
	// The workspace root also anchors Thane's derived document roots.
	// {workspace.path}/core holds what the operator declares Thane to be;
	// {workspace.path}/self holds what Thane has made of that; the optional
	// {workspace.path}/contacts holds contact dossiers; and the optional
	// {workspace.path}/dossiers holds dossiers for non-contact subjects.
	Workspace WorkspaceConfig `yaml:"workspace"`

	// Roots is the unified document-root config. Each entry names one
	// managed root and combines its path with optional per-root policy
	// (indexing, authoring, git-backed provenance, signing, signature
	// verification). See docs/understanding/document-roots.md for the
	// operator-facing contract.
	//
	// Each entry accepts either a bare-string shorthand (path only,
	// default policy) or a full mapping form. Both forms are valid in
	// the same block:
	//
	//   roots:
	//     kb: ~/Sync/Vault                  # bare string, defaults
	//     scratchpad:
	//       path: ~/Thane/scratchpad
	//       authoring: managed
	//     secure:
	//       path: ~/secure/notes
	//       git:
	//         enabled: true
	//         sign_commits: true
	//         verify_signatures: required
	//         signing_key: ~/Thane/core/identity/signing_ed25519
	//
	// Typical names: kb (curated knowledge), generated (model-produced
	// durable outputs), and scratchpad (low-integrity work area). The core,
	// self, contacts, and dossiers names are reserved and derived from
	// workspace.path; declaring them under roots: sets policy. Paths on core,
	// self, and contacts are ignored for compatibility; an explicit dossiers
	// path is rejected with its migration recipe.
	Roots map[string]RootEntry `yaml:"roots,omitempty"`

	// Paths is the legacy directory-mapping block. Replaced by roots:.
	// Still parsed for backwards compatibility with a deprecation
	// warning. Cannot be used in the same config as roots:. New
	// configs should use roots: instead.
	Paths map[string]string `yaml:"paths,omitempty"`

	// DocRoots is the legacy per-root policy overlay. Replaced by
	// roots: (where path and policy live in the same entry). Still
	// parsed for backwards compatibility with a deprecation warning.
	// Cannot be used in the same config as roots:.
	DocRoots map[string]DocumentRootConfig `yaml:"doc_roots,omitempty"`

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
	// document roots. Relative paths are resolved from the workspace;
	// without a workspace they remain relative to the working directory.
	// Default: "./db".
	DataDir string `yaml:"data_dir"`

	// TalentsDir is the directory holding the talent markdown that extends
	// the system prompt.
	//
	// Derived as {workspace}/core/talents, never authored: talents steer
	// every turn, so they carry the same signed history and the same
	// cleanliness rule as the prompts beside them. Declaring it is refused
	// with the migration — see [rejectRetiredKeys].
	TalentsDir string `yaml:"-"`

	// Archive configures session archive behavior.
	Archive ArchiveConfig `yaml:"archive"`

	// Extraction configures automatic fact extraction from conversations.
	Extraction ExtractionConfig `yaml:"extraction"`

	// Search configures web search providers.
	Search SearchConfig `yaml:"search"`

	// Episodic configures episodic memory context injection (daily
	// memory files and recent conversation history).
	Episodic EpisodicConfig `yaml:"episodic"`

	// Compaction bounds per-conversation working memory: once a
	// conversation's active token count crosses the trigger, older
	// history folds into a single summary message.
	Compaction CompactionConfig `yaml:"compaction"`

	// Agent configures agent loop behavior, including orchestrator
	// tool gating for delegation-first architecture.
	Agent AgentConfig `yaml:"agent"`

	// Delegate configures the thane_* delegation tools' split-model execution.
	Delegate DelegateConfig `yaml:"delegate"`

	// CapabilityTags optionally overlays the compiled-in capability-tag catalog.
	// Use this for deliberate operator overrides or custom tags. Built-in
	// tags usually do not need entries here. Tags marked core are
	// loaded unconditionally. Other tags are activated via
	// tag_activate/tag_deactivate tools, runtime source
	// policy, or channel-pinned configuration.
	CapabilityTags map[string]CapabilityTagConfig `yaml:"capability_tags"`

	// ChannelTags maps source channels to broad optional capability tags.
	// Use this for coarse source defaults, not runtime facts such as operator
	// identity or current message-channel affordances. Runtime-only tags
	// such as owner and message_channel are skipped here; they must be
	// asserted by trusted current-run evidence. This is additive to
	// core tags and any tags the agent requests at runtime. Tag
	// names must reference either compiled-in tags or entries in [CapabilityTags].
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

	// Signal configures native Signal message routing through signal-cli jsonRpc.
	Signal SignalConfig `yaml:"signal"`

	// Forge configures code forge integrations (GitHub, Gitea). When
	// configured, Thane can interact with issues, pull requests, and
	// code review directly without an MCP forge server subprocess.
	Forge ForgeConfig `yaml:"forge"`

	// Email configures native IMAP email access. When configured, Thane
	// can list, read, search, and manage email directly without an MCP
	// email server subprocess.
	Email email.Config `yaml:"email"`

	// Identity configures contact identities for the agent and human operator.
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

	// Signing is a placeholder for future instance-wide signing settings.
	// It no longer holds a trust set: signing keys are declared per root
	// as roots.<name>.seed_signers, so the keys entitled to sign a corpus
	// shared over a remote are not automatically entitled to sign the
	// config that decides what the whole system trusts.
	Signing SigningConfig `yaml:"signing"`

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
	// Defaults: min_sleep 15m, max_sleep 60m, default_sleep 30m, jitter 0.2,
	// supervisor_probability 0.1, router.quality_floor 3 (supervisor 8).
	Metacognitive MetacognitiveConfig `yaml:"metacognitive"`

	// Ego configures the self-reflection loop. When enabled, a long-cycle
	// service loop maintains self/ego.md via a declared maintained-document
	// output, with supervisor randomization for periodic frontier review.
	// Defaults: min_sleep 30m, max_sleep 24h, default_sleep 6h, jitter 0.2,
	// supervisor_probability 0.2, router.quality_floor 5 (supervisor 8).
	Ego EgoConfig `yaml:"ego"`

	// Archivist configures the memory archivist loop. When enabled, a
	// self-paced service loop tends thane's understanding across the
	// memory silos by authoring dossiers — long-lived synthesis
	// documents keyed by subject. It drains a durable work queue that
	// producers (session close, frontier expansion) enqueue into; it is
	// never event-woken, so a burst of activity can't amplify into a
	// burst of work. Maintains core/archivist.md as its working state.
	// Defaults: min_sleep 15m, max_sleep 12h, default_sleep 1h, jitter 0.2,
	// supervisor_probability 0.1, router.quality_floor 5 (supervisor 8).
	Archivist ArchivistConfig `yaml:"archivist"`

	// Loops configures immutable loop definitions loaded from the config
	// file. These definitions become the base layer for the loop
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
}

// PricingEntry defines per-million-token costs for a model in USD.
type PricingEntry struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

// LoggingConfig configures Thane's structured filesystem log datasets,
// stdout policy, and SQLite-backed log/query retention.
type LoggingConfig struct {
	// Root is the archive root — the top of the layout described in
	// archive/README.md (interactions/, sources/, meta/). Thane writes
	// every per-dataset JSONL stream under root/sources/thane/<dataset>/.
	// Relative paths are resolved from the working directory (typically
	// ~/Thane). Defaults to "archive" when omitted. Set to an explicit
	// empty string (root: "") to disable filesystem archiving entirely.
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
	// arguments/results, request/response content, and the model-facing
	// chat message payload are persisted to logs.db alongside the existing
	// log index. Default: false.
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
	Events        LoggingDatasetConfig `yaml:"events"`
	Requests      LoggingDatasetConfig `yaml:"requests"`
	Access        LoggingDatasetConfig `yaml:"http_access"`
	Loops         LoggingDatasetConfig `yaml:"loops"`
	Delegates     LoggingDatasetConfig `yaml:"delegates"`
	Envelopes     LoggingDatasetConfig `yaml:"envelopes"`
	Conversations LoggingDatasetConfig `yaml:"conversations"`
}

// LoggingDatasetConfig controls one structured JSONL dataset.
type LoggingDatasetConfig struct {
	// Enabled controls whether the dataset is written. When omitted, each
	// dataset uses its built-in default.
	Enabled *bool `yaml:"enabled"`
}

// RootPath returns the resolved archive root. When Root is nil and Dir
// is also nil, it returns the default "archive" — fresh installs land
// at <workspace>/archive/. When either is an explicit empty string, it
// returns "" which signals that filesystem archiving is disabled.
//
// The archive root is the top of the layout described in
// archive/README.md: it holds interactions/, sources/, and meta/.
func (l LoggingConfig) RootPath() string {
	if l.Root != nil {
		return *l.Root
	}
	if l.Dir != nil {
		return *l.Dir
	}
	return "archive"
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
	case "http_access":
		return datasetEnabled(l.Datasets.Access, false)
	case "loops":
		return datasetEnabled(l.Datasets.Loops, true)
	case "delegates":
		return datasetEnabled(l.Datasets.Delegates, true)
	case "envelopes":
		return datasetEnabled(l.Datasets.Envelopes, true)
	case "conversations":
		return datasetEnabled(l.Datasets.Conversations, true)
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

// ListenConfig configures an HTTP server's bind address and port.
type ListenConfig struct {
	// Address is the network address to bind to. Empty string means
	// all interfaces (0.0.0.0).
	Address string `yaml:"address"`

	// Port is the TCP port to listen on. Default: 8080.
	Port int `yaml:"port"`

	// Auth gates the native /v1 API and the console. See ListenAuthConfig.
	Auth ListenAuthConfig `yaml:"auth"`
}

// ListenAuthConfig configures authentication for the native API and the
// web console. When at least one token is configured the API requires a
// credential on every route except the enumerated public ones (health,
// version, identity evidence, console assets, and the endpoints that
// authenticate themselves); with no tokens the API is open, as it was
// before this block existed, and thane init closes that door on new
// workspaces by minting a token. API clients present a token as
// Authorization: Bearer. The console exchanges one once at
// POST /v1/auth/login for an HttpOnly, SameSite=Strict session cookie.
// Companion account tokens (companion.providers.<account>.tokens) are
// accepted as bearer credentials on the same routes, so companion apps
// that already send them keep working; they authenticate as the account.
type ListenAuthConfig struct {
	// Tokens are the operator's API credentials. Each is compared as a
	// SHA-256 digest in constant time; the plaintext is never retained
	// after load. Label names the holder in logs and in the session
	// endpoint, never the token itself.
	Tokens []APIToken `yaml:"tokens"`

	// SessionTTL is how long a console session cookie stays valid
	// without use; each authenticated request extends it. Default: 168h
	// (7 days). Sessions live in memory and end on restart.
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// APIToken is one operator credential for the native API.
type APIToken struct {
	// Label identifies the holder (a person, a script, a device); it is
	// what logs and the session endpoint report.
	Label string `yaml:"label"`
	// Token is the secret the holder presents. Treat this file
	// accordingly.
	Token string `yaml:"token"`
}

// Configured reports whether the native API gate is on, which is exactly
// when at least one token is present.
func (a ListenAuthConfig) Configured() bool {
	return len(a.Tokens) > 0
}

// TokenIndex builds a token → label map for the constant-time token set.
func (a ListenAuthConfig) TokenIndex() map[string]string {
	idx := make(map[string]string, len(a.Tokens))
	for _, t := range a.Tokens {
		idx[t.Token] = t.Label
	}
	return idx
}

// OllamaAPIConfig configures the optional Ollama-compatible API server.
// When Enabled is true, Thane exposes an additional HTTP server that
// speaks the Ollama wire protocol, allowing Home Assistant's built-in
// Ollama integration to use Thane as a drop-in backend.
type OllamaAPIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // Bind address; empty = all interfaces
	Port    int    `yaml:"port"`    // Default: 11434
	// APIKey, when set, requires every request to the Ollama-compatible
	// surface to present it as a bearer token (Authorization: Bearer
	// <key>). This surface drives the full agent loop — tools, memory,
	// delegation — so leaving it empty (open) is appropriate only when
	// every host that can reach the port is trusted. Note that Home
	// Assistant's Ollama integration cannot send bearer tokens; enable
	// this only for token-capable clients (e.g. Open WebUI) or when HA
	// connects through a proxy that injects the header.
	APIKey string `yaml:"api_key"`
}

// OpenAIAPIConfig configures the optional OpenAI-compatible API server.
// When Enabled is true, Thane serves the OpenAI-compatible shim
// (/v1/chat/completions, /v1/models) on its own port, separate from the
// Thane-native /v1 API on the primary listen port.
type OpenAIAPIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // Bind address; empty = all interfaces
	Port    int    `yaml:"port"`    // Default: 8081
	// APIKey, when set, requires every request to the OpenAI-compatible
	// surface to present it as a bearer token (Authorization: Bearer
	// <key>), which is the header every OpenAI client library already
	// sends. This surface drives the full agent loop — tools, memory,
	// delegation — so leaving it empty (open) is appropriate only when
	// every host that can reach the port is trusted.
	APIKey string `yaml:"api_key"`
}

// TLSConfig configures Thane's HTTPS front door: one TLS listener that
// holds a certificate for each configured hostname and routes each
// hostname to one of the plaintext surfaces (the native API, the Ollama
// shim, or the OpenAI shim), plus a plain-HTTP listener whose only job is
// to redirect. Certificates are obtained and renewed in-process by
// certmagic over the ACME DNS-01 challenge, so hostnames that resolve
// only on a private network still get publicly trusted certificates.
//
// The CertMagic block is a pass-through: its fields mirror certmagic's
// own configuration rather than a curated subset, so an operator who
// knows certmagic or Caddy already knows this block. Thane adds only what
// certmagic cannot know: which hostnames to hold and which surface each
// reaches.
type TLSConfig struct {
	// Enabled starts the HTTPS front door. Off by default; the plaintext
	// listeners are unaffected either way.
	Enabled bool `yaml:"enabled"`

	// HTTPS is the TLS listener. Port defaults to 443. macOS permits an
	// unprivileged process to bind it; Linux needs CAP_NET_BIND_SERVICE
	// or the unprivileged-port sysctl.
	HTTPS TLSListenConfig `yaml:"https"`

	// HTTP is the plain listener that answers every request with a
	// permanent redirect to HTTPS and nothing else. Port defaults to 80.
	HTTP TLSRedirectConfig `yaml:"http"`

	// HSTSMaxAge is the Strict-Transport-Security max-age sent on every
	// HTTPS response. Zero uses the default of 4320h (180 days); to send
	// no header at all, set HSTSDisabled.
	HSTSMaxAge time.Duration `yaml:"hsts_max_age"`

	// HSTSDisabled omits the Strict-Transport-Security header entirely.
	HSTSDisabled bool `yaml:"hsts_disabled"`

	// Hostnames maps each hostname to the surface it reaches: "native"
	// (the /v1 API and dashboard), "ollama", or "openai". Every hostname
	// listed gets its own managed certificate; a request for a hostname
	// not listed is refused with 421 Misdirected Request. Names are
	// explicit, never issued on demand.
	Hostnames map[string]string `yaml:"hostnames"`

	// ClientAuth controls verification of client certificates presented
	// during the handshake. See TLSClientAuthConfig.
	ClientAuth TLSClientAuthConfig `yaml:"client_auth"`

	// CertMagic is the certificate-management pass-through. See
	// CertMagicConfig.
	CertMagic CertMagicConfig `yaml:"certmagic"`
}

// TLSListenConfig is a bind address and port for the HTTPS listener.
type TLSListenConfig struct {
	// Address is the bind address; empty means all interfaces.
	Address string `yaml:"address"`
	// Port is the TCP port. Default: 443.
	Port int `yaml:"port"`
}

// TLSRedirectConfig is the bind for the redirect-only HTTP listener.
type TLSRedirectConfig struct {
	// Disabled turns the redirect listener off entirely.
	Disabled bool `yaml:"disabled"`
	// Address is the bind address; empty means all interfaces.
	Address string `yaml:"address"`
	// Port is the TCP port. Default: 80.
	Port int `yaml:"port"`
}

// TLSClientAuthConfig governs client certificates on the HTTPS front
// door. When enabled (the default), the listener requests a client
// certificate, verifies any it is given against the instance's channel
// CA (core/ca/channel_root.crt) plus the trusted peer CAs listed here,
// and attaches the verified identity to the request as a principal that
// later authentication layers can consume. A connection that presents
// no certificate is not refused; requiring one is a later policy.
type TLSClientAuthConfig struct {
	// Disabled turns client-certificate verification off. Certificates a
	// client presents are then ignored rather than verified.
	Disabled bool `yaml:"disabled"`
	// TrustedPeerCAs lists additional CA certificate files (PEM) whose
	// issued client certificates are accepted alongside the channel CA.
	// Relative paths resolve against the core root.
	TrustedPeerCAs []string `yaml:"trusted_peer_cas"`
}

// CertMagicConfig mirrors certmagic's certificate-management settings.
// Field names follow certmagic's, in snake_case, so its documentation
// applies directly.
type CertMagicConfig struct {
	// CA is the ACME directory URL. Empty selects Let's Encrypt
	// production. Point it at the staging directory
	// (https://acme-staging-v02.api.letsencrypt.org/directory) while
	// bringing the front door up beside an existing proxy.
	CA string `yaml:"ca"`
	// Email is the ACME account contact. Strongly recommended: the CA
	// uses it for expiry and revocation notices.
	Email string `yaml:"email"`
	// Agreed records acceptance of the CA's subscriber agreement. Must be
	// true; the CA refuses account creation otherwise.
	Agreed bool `yaml:"agreed"`
	// KeyType selects the certificate key: ed25519, p256, p384, rsa2048,
	// or rsa4096. Empty uses certmagic's default (p256).
	KeyType string `yaml:"key_type"`
	// RenewalWindowRatio is the fraction of a certificate's lifetime
	// after which renewal is attempted. Zero uses certmagic's default
	// (1/3, so a 90-day certificate renews at 60 days).
	RenewalWindowRatio float64 `yaml:"renewal_window_ratio"`
	// Storage is the directory for ACME account keys and issued
	// certificates. Default: {workspace}/tls. Runtime state, never
	// inside core; a path under the core root is refused.
	Storage string `yaml:"storage"`
	// MustStaple requests certificates with the OCSP must-staple
	// extension.
	MustStaple bool `yaml:"must_staple"`
	// CertObtainTimeout bounds one issuance attempt end to end, including
	// DNS propagation waits. Zero uses certmagic's default; set it above
	// dns.propagation_delay plus dns.propagation_timeout or issuance is
	// cut off while still waiting for DNS.
	CertObtainTimeout time.Duration `yaml:"cert_obtain_timeout"`
	// DNS configures the DNS-01 challenge solver, which is the only
	// challenge the front door uses.
	DNS CertMagicDNSConfig `yaml:"dns"`
}

// CertMagicDNSConfig mirrors certmagic's DNS-01 solver settings plus the
// provider selection Thane adds.
type CertMagicDNSConfig struct {
	// Provider names the DNS provider from Thane's registry (currently
	// "linode"). Required.
	Provider string `yaml:"provider"`
	// PropagationDelay is how long to wait after creating the challenge
	// record before starting propagation checks. Providers whose
	// authoritative servers lag their API need this; Linode's guidance is
	// ten minutes, which is the registry default for that provider when
	// this is zero.
	PropagationDelay time.Duration `yaml:"propagation_delay"`
	// PropagationTimeout is the longest to keep checking for the record
	// after the delay. Zero uses the provider's registry default; -1
	// (certmagic's sentinel) disables propagation checks so issuance
	// proceeds as soon as the delay elapses.
	PropagationTimeout time.Duration `yaml:"propagation_timeout"`
	// Resolvers lists DNS servers to query during propagation checks,
	// as host or host:port. Pointing these at the zone's authoritative
	// nameservers avoids waiting on recursive-resolver caches.
	Resolvers []string `yaml:"resolvers"`
	// TTL is the challenge record's TTL. Zero uses the provider default.
	TTL time.Duration `yaml:"ttl"`
	// OverrideDomain delegates the challenge record to another domain
	// (a CNAME'd _acme-challenge target).
	OverrideDomain string `yaml:"override_domain"`
	// Settings is passed through to the provider verbatim, with the
	// provider's own field names (for linode: api_token, and optionally
	// api_url and api_version). Unknown keys are rejected.
	Settings map[string]any `yaml:"settings"`
}

// TLSSurfaceNames are the values Hostnames may route to.
var TLSSurfaceNames = []string{"native", "ollama", "openai"}

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

// CompanionConfig configures the WebSocket endpoint for native companion
// app connections (e.g. thane-agent-macos). When enabled, providers can
// connect and register capabilities for bidirectional service dispatch.
//
// Each entry in Providers maps an account name (e.g. "nugget", "aimee")
// to a set of per-device tokens. Multiple devices under the same account
// share an identity but are independently addressable by client_id.
type CompanionConfig struct {
	Enabled   bool                               `yaml:"enabled"`
	Providers map[string]CompanionProviderConfig `yaml:"providers"`
}

// MemoryGuardConfig configures the in-process memory guard
// (internal/platform/memguard). Opt-in: Enabled defaults false. Zero-valued
// numeric fields are defaulted by the guard at startup (memguard.New) —
// soft 1024 MB / hard 2048 MB / 15s poll — not by config Load, so the Config
// object does not hold these effective defaults after loading.
type MemoryGuardConfig struct {
	Enabled         bool   `yaml:"enabled"`
	SoftLimitMB     int    `yaml:"soft_limit_mb"`
	HardLimitMB     int    `yaml:"hard_limit_mb"`
	ProfileDir      string `yaml:"profile_dir"`
	IntervalSeconds int    `yaml:"interval_seconds"`
}

// CompanionProviderConfig defines the tokens for a single account identity.
// Each token typically corresponds to a different device running a
// companion app (e.g. thane-agent-macos on a laptop vs desktop).
type CompanionProviderConfig struct {
	Tokens []string `yaml:"tokens"`

	// Contact binds every device authenticating through this account to
	// one contact record (a contact UUID): the counterparty layer's
	// person attribution (#1450). The account names a credential
	// namespace; this names whose devices those are. Optional — empty
	// means unbound, and devices degrade to account-only attribution.
	// Living in config is deliberate custody: bindings confer inherited
	// trust and must not be writable through contact-editing tools.
	Contact string `yaml:"contact"`
}

// Configured reports whether the companion endpoint is enabled
// and has at least one provider with at least one token.
func (c CompanionConfig) Configured() bool {
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

// Validate checks companion configuration for internal consistency.
// When enabled, at least one provider must have a non-empty token, and
// tokens must not be shared across accounts.
func (c CompanionConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("companion: enabled but no providers configured")
	}
	hasToken := false
	seen := make(map[string]string) // token → account
	for account, p := range c.Providers {
		if p.Contact != "" {
			if _, err := uuid.Parse(p.Contact); err != nil {
				return fmt.Errorf("companion: account %q contact binding %q is not a contact UUID", account, p.Contact)
			}
		}
		for _, tok := range p.Tokens {
			if tok == "" {
				continue
			}
			hasToken = true
			if prev, dup := seen[tok]; dup {
				return fmt.Errorf("companion: duplicate token shared by accounts %q and %q", prev, account)
			}
			seen[tok] = account
		}
	}
	if !hasToken {
		return fmt.Errorf("companion: enabled but no tokens configured for any provider")
	}
	return nil
}

// TokenIndex builds a map from token → account name for O(1) auth lookups.
func (c CompanionConfig) TokenIndex() map[string]string {
	idx := make(map[string]string)
	for account, p := range c.Providers {
		for _, tok := range p.Tokens {
			idx[tok] = account
		}
	}
	return idx
}

// IdentityConfig configures the agent's own contact identity and the primary
// human operator's stable contact reference. ContactName must match a contact
// record in the directory to enable self-referencing operations like vCard
// export.
type IdentityConfig struct {
	// ContactName is the formatted name of the agent's own contact
	// record. When set, contact_export_vcf name="self" resolves to this
	// contact.
	ContactName string `yaml:"contact_name"`

	// OperatorContactID is the stable UUID of the primary human operator's
	// contact record. It is preferred over the legacy name-based selector
	// because contact display names can change. The referenced active
	// contact must exist at startup. Ignored when the config is unverified.
	OperatorContactID string `yaml:"operator_contact_id"`

	// OwnerContactName is the formatted name of the primary human
	// operator's contact record. This legacy selector remains for
	// compatibility; prefer OperatorContactID. The two selectors are
	// mutually exclusive. When neither is set, contact_owner falls back
	// to the sole admin contact if exactly one exists. Ignored when the
	// config is unverified.
	OwnerContactName string `yaml:"owner_contact_name,omitempty"`
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

// SigningConfig is retained for future instance-wide signing settings.
// The trust set itself is declared per root as seed_signers, because
// roots have different trust domains: the config that decides what the
// system trusts and a corpus shared over a remote should not draw their
// signers from one list.
type SigningConfig struct{}

// AllowedSigner is one trusted signing identity: an SSH public key bound to
// a principal, with an optional label and validity window. It renders to
// one line of an OpenSSH allowed_signers file at startup.
type AllowedSigner struct {
	// Principal is the OpenSSH signer identity, conventionally an email
	// such as "alice@example.com". Required. It must not contain
	// whitespace or control characters — the allowed_signers format is
	// space-delimited, so either would corrupt the line or smuggle a
	// second entry.
	Principal string `yaml:"principal"`

	// Key is the SSH public key in authorized_keys form, such as
	// "ssh-ed25519 AAAA...". Required. It must parse as a valid public
	// key.
	Key string `yaml:"key"`

	// Label is an optional human note rendered as the key's trailing
	// comment. It must not contain control characters.
	Label string `yaml:"label,omitempty"`

	// ValidAfter and ValidBefore optionally bound when the key is trusted.
	// Each is an RFC3339 timestamp; when both are set, ValidAfter must be
	// strictly before ValidBefore. Enforcement is delegated to OpenSSH at
	// verify time; thane does not yet surface expiry.
	ValidAfter  string `yaml:"valid_after,omitempty"`
	ValidBefore string `yaml:"valid_before,omitempty"`
}

// RootEntry combines a managed root's path with its per-root policy
// in the unified roots: block. Accepts either a bare-string shorthand
// (the entire entry is the path, all policy fields default) or a
// mapping with explicit path: and policy fields.
type RootEntry struct {
	// Path is the directory the root resolves to. Supports ~ expansion at
	// resolver construction time. Reserved derived roots take their paths from
	// workspace.path. Paths on core, self, and contacts are ignored for
	// compatibility; an explicit dossiers path is rejected with its migration
	// recipe.
	Path string `yaml:"path"`

	// Indexing controls whether markdown files in this root are
	// scanned into the document index. Omit to keep indexing enabled.
	Indexing *bool `yaml:"indexing,omitempty"`

	// Authoring controls whether managed document mutation tools may
	// write this root. Empty defaults to "managed". Supported values:
	// "managed", "read_only", "restricted".
	Authoring string `yaml:"authoring,omitempty"`

	// Git configures optional git-backed write provenance for this
	// root. Same fields as the legacy doc_roots[name].git block.
	Git DocumentRootGitConfig `yaml:"git,omitempty"`

	// Context governs how this root's documents may reach a model:
	// whether they can be injected into a prompt and how they surface
	// in search. Omit to keep the legacy behavior for this root.
	Context RootContextPolicy `yaml:"context,omitempty"`

	// SeedSigners are the keys entitled to establish this root. They
	// seed its .allowed_signers at birth and are not re-applied after,
	// because from then on the root's own file is its record of whom it
	// trusts.
	//
	// Declared per root rather than shared, so the keys that may sign a
	// corpus synced from a remote are not automatically the keys that
	// may sign the config deciding what the whole system trusts.
	//
	// They are also what the root is admitted against at startup. A
	// signed root must have been born in a commit one of these keys
	// signed, and every later change to its .allowed_signers must carry
	// a seed signer's signature too. Keys the trust file delegates to
	// may sign ordinary content but cannot widen the trust file itself,
	// so trust only ever grows by a decision recorded here.
	//
	// Admission deliberately does not consult the root's own trust file.
	// A repository that vouched for its own trust surface would decide
	// its own admission, since whoever wrote that file also chose what
	// it says.
	//
	// The agent's own key is not implicitly entitled. A root Thane
	// creates is born signed by the agent, so such a root must list
	// thane@provenance.local here — and a root that omits it, such as a
	// core established by an operator, is one the agent cannot
	// establish or re-establish on its own.
	SeedSigners []AllowedSigner `yaml:"seed_signers,omitempty"`
}

// Root context policy values. Injection is the narrower and more
// consequential of the two: a root that may inject can place text into a
// system prompt without anyone asking for it, so eligibility is declared
// per root rather than inferred from a document's own frontmatter.
const (
	// RootInjectNone keeps every document in this root out of prompt
	// assembly. It is the safe default for any root Thane does not
	// author, and for any root synced from somewhere Thane does not
	// control.
	RootInjectNone = "none"
	// RootInjectTagged lets a document whose frontmatter tags match an
	// active capability tag inject as tagged guidance.
	RootInjectTagged = "tagged"

	// RootSearchDefault includes the root in unscoped document search.
	RootSearchDefault = "default"
	// RootSearchOnRequest keeps the root out of unscoped search but
	// reachable when a query names it. Large foreign corpora want this:
	// searchable on purpose, never drowning an open-ended query.
	RootSearchOnRequest = "on_request"
	// RootSearchNever excludes the root from document search entirely.
	RootSearchNever = "never"

	// RootUntaggedIgnore skips a document carrying no tags. It is the
	// default, and it is what every injecting root has always done.
	RootUntaggedIgnore = "ignore"
	// RootUntaggedRefuse makes a tagless document an error naming the
	// file, for a root whose contents must all be classified.
	//
	// The alternative to refusing is guessing, and the guess has already
	// proven brittle: the talent loader treats a tagless file as
	// permanent guidance, which forced a heuristic excluding filenames
	// that begin with a capital so a README would not be injected into
	// every prompt. Capitalization is not a semantic. A root that
	// refuses says which file is unclassified instead of inferring an
	// answer from its name.
	RootUntaggedRefuse = "refuse"

	// RootAdvertiseNever keeps this root's documents off the context
	// advertisement rail entirely. The default: ambient attention is a
	// per-turn spend, and a corpus earns it by opting in.
	RootAdvertiseNever = "never"
	// RootAdvertiseTagged lets documents offer themselves only while the
	// root's requires_tag capability tag is active on the turn, so a
	// specialist corpus surfaces exactly when its capability does.
	RootAdvertiseTagged = "tagged"
	// RootAdvertiseAlways lets documents make ambient offers on every
	// eligible turn — the posture for a small curated corpus like a
	// schedule root, whose freshest signal is worth a standing seat at
	// the discriminator's table.
	RootAdvertiseAlways = "always"
	// RootAdvertiseExactSubject lets a document offer itself only when one
	// of its tags exactly matches a canonical request subject. It is the
	// posture for private per-subject corpora such as contact dossiers:
	// relevant records get a trailhead without unrelated records spending
	// ambient attention or matching on loose words.
	RootAdvertiseExactSubject = "exact_subject"
)

// RootContextPolicy declares how one document root may reach a model.
// It sits beside the storage policy (authoring, git, signing) because it
// answers the same class of question — how is this corpus governed — at
// the same granularity, and because a corpus is the only place the
// answer can be given for documents Thane does not own and cannot
// annotate.
type RootContextPolicy struct {
	// Inject controls prompt-assembly eligibility: "none" (default for
	// a root that declares a context policy) or "tagged".
	Inject string `yaml:"inject,omitempty"`

	// Search controls search visibility: "default", "on_request", or
	// "never".
	Search string `yaml:"search,omitempty"`

	// RequiresTag optionally gates the whole root behind one capability
	// tag. It is a coarse companion to per-document tags: cheap to
	// enforce, and the right shape when an entire corpus is only
	// relevant while a capability is active.
	//
	// It applies to tagged injection and tagged advertising. Gating search
	// the same way would need the document store to see active capability
	// tags, which it deliberately does not.
	RequiresTag string `yaml:"requires_tag,omitempty"`

	// Advertise controls whether this root's documents may offer
	// themselves to context assembly through the advertisement rail
	// (#1431): "never" (default), "tagged" (offers only while
	// RequiresTag's capability tag is active), "always" (ambient offers
	// on every eligible turn), or "exact_subject" (offers only when a
	// document tag exactly matches a canonical request subject).
	// Advertising is a third door
	// beside Inject and Search rather than a mode of either: injection
	// pushes whole documents on a tag, search answers an explicit ask,
	// and advertising lets a document compete for a bounded slice of
	// unasked-for attention. A root must opt in — the safe reading of
	// silence is that a corpus does not volunteer itself.
	Advertise string `yaml:"advertise,omitempty"`

	// Untagged decides what a document carrying no tags means in this
	// root: "ignore" (default) skips it, "refuse" makes it an error.
	//
	// Refusing matters where the documents are load-bearing. Guidance
	// that shapes every turn should not be able to go missing quietly
	// because someone forgot a frontmatter line — the instance should
	// name the file and stop, the way it does for an uncommitted one.
	Untagged string `yaml:"untagged,omitempty"`
}

// Declared reports whether this root states a context policy at all.
// An undeclared policy keeps the root's historical behavior so an
// existing config does not silently change how context is assembled.
func (p RootContextPolicy) Declared() bool {
	return p.Inject != "" || p.Search != "" || p.RequiresTag != "" || p.Advertise != ""
}

// EffectiveInject resolves the injection policy. A root that declares a
// context policy without naming inject gets "none": declaring policy is
// an act of governance, and the safe reading of an unstated answer is
// that the corpus stays out of the prompt.
func (p RootContextPolicy) EffectiveInject() string {
	if p.Inject == "" {
		return RootInjectNone
	}
	return p.Inject
}

// EffectiveSearch resolves the search policy, defaulting to full
// visibility. Search is a pull: the model asked, so the conservative
// default is the useful one.
func (p RootContextPolicy) EffectiveSearch() string {
	if p.Search == "" {
		return RootSearchDefault
	}
	return p.Search
}

// EffectiveAdvertise resolves the advertisement policy. The default is
// "never" for the same reason EffectiveInject defaults closed: ambient
// attention is spent on every turn, and a corpus earns it by explicit
// opt-in, not by existing.
func (p RootContextPolicy) EffectiveAdvertise() string {
	if p.Advertise == "" {
		return RootAdvertiseNever
	}
	return p.Advertise
}

// EffectiveUntagged resolves what a tagless document means here,
// defaulting to skipping it.
func (p RootContextPolicy) EffectiveUntagged() string {
	if p.Untagged == "" {
		return RootUntaggedIgnore
	}
	return p.Untagged
}

// Validate checks the declared policy values.
func (p RootContextPolicy) Validate(rootName string) error {
	switch p.Inject {
	case "", RootInjectNone, RootInjectTagged:
	default:
		return fmt.Errorf("roots.%s.context.inject must be %q or %q, got %q", rootName, RootInjectNone, RootInjectTagged, p.Inject)
	}
	switch p.Search {
	case "", RootSearchDefault, RootSearchOnRequest, RootSearchNever:
	default:
		return fmt.Errorf("roots.%s.context.search must be %q, %q, or %q, got %q", rootName, RootSearchDefault, RootSearchOnRequest, RootSearchNever, p.Search)
	}
	switch p.Advertise {
	case "", RootAdvertiseNever, RootAdvertiseTagged, RootAdvertiseAlways, RootAdvertiseExactSubject:
	default:
		return fmt.Errorf("roots.%s.context.advertise must be %q, %q, %q, or %q, got %q", rootName, RootAdvertiseNever, RootAdvertiseTagged, RootAdvertiseAlways, RootAdvertiseExactSubject, p.Advertise)
	}
	if p.Advertise == RootAdvertiseTagged && p.RequiresTag == "" {
		return fmt.Errorf("roots.%s.context.advertise %q needs requires_tag to name the gating capability tag", rootName, RootAdvertiseTagged)
	}
	switch p.Untagged {
	case "", RootUntaggedIgnore, RootUntaggedRefuse:
	default:
		return fmt.Errorf("roots.%s.context.untagged must be %q or %q, got %q", rootName, RootUntaggedIgnore, RootUntaggedRefuse, p.Untagged)
	}
	if p.Untagged == RootUntaggedRefuse && p.EffectiveInject() != RootInjectTagged {
		// Refusing tagless documents is a statement about what may
		// inject. On a root that never injects it would reject files
		// for failing to qualify for something they were never eligible
		// for, which reads as a broken config rather than a policy.
		return fmt.Errorf("roots.%s.context.untagged is %q but the root does not inject; set inject: %q or drop the untagged policy", rootName, RootUntaggedRefuse, RootInjectTagged)
	}
	// requires_tag gates the capability-aware doors: tagged injection
	// and tagged advertising. Search runs below the capability layer —
	// the document store has no view of which tags are active — so
	// accepting the field on a root that neither injects nor advertises
	// by tag would silently do nothing, which is worse than refusing it.
	if p.RequiresTag != "" && p.EffectiveInject() != RootInjectTagged && p.EffectiveAdvertise() != RootAdvertiseTagged {
		return fmt.Errorf("roots.%s.context.requires_tag gates tagged injection and tagged advertising; set inject: %q or advertise: %q (got inject %q, advertise %q). It does not gate search, because search does not see active capability tags", rootName, RootInjectTagged, RootAdvertiseTagged, p.EffectiveInject(), p.EffectiveAdvertise())
	}
	return nil
}

// UnmarshalYAML accepts either the bare-string shorthand or the full
// mapping form for a root entry. Bare strings populate Path with all
// policy fields defaulted; mappings decode normally.
//
// Scalar shorthand requires a non-empty string. Null (e.g. `kb:` with
// no value) and non-string scalars (numbers, bools) are rejected so a
// typo can't silently become a path.
func (r *RootEntry) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "" && node.Tag != "!!str" {
			return fmt.Errorf("roots entry shorthand must be a string path, got %s", node.Tag)
		}
		if strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("roots entry shorthand path must not be empty")
		}
		r.Path = node.Value
		return nil
	case yaml.MappingNode:
		// Use a type alias to avoid recursing into this method.
		type rawRootEntry RootEntry
		var raw rawRootEntry
		if err := node.Decode(&raw); err != nil {
			return err
		}
		*r = RootEntry(raw)
		return nil
	default:
		return fmt.Errorf("roots entry must be a string path or a mapping, got node kind %d", node.Kind)
	}
}

// DocumentRootConfig configures policy for one managed document root.
// The root itself is still named under paths:, except for core:, which
// is derived from workspace.path.
type DocumentRootConfig struct {
	// Indexing controls whether markdown files in this root are scanned
	// into the document index. Omit to keep indexing enabled.
	Indexing *bool `yaml:"indexing,omitempty"`

	// Authoring controls whether managed document mutation tools may
	// write this root. Empty defaults to "managed". Supported values:
	// "managed", "read_only", and "restricted". Restricted is intended
	// for high-integrity roots whose writes must come from narrower
	// future flows.
	Authoring string `yaml:"authoring,omitempty"`

	// Git configures optional git-backed write provenance for this root.
	Git DocumentRootGitConfig `yaml:"git,omitempty"`

	// Context governs how this root's documents may reach a model.
	Context RootContextPolicy `yaml:"context,omitempty"`

	// SeedSigners are the keys entitled to establish this root, and the
	// set its history is admitted against at startup.
	SeedSigners []AllowedSigner `yaml:"seed_signers,omitempty"`
}

// DocumentRootGitConfig configures git-backed provenance for one
// managed document root.
type DocumentRootGitConfig struct {
	// Enabled controls whether this root participates in git-backed
	// provenance. When false, git settings are ignored.
	Enabled bool `yaml:"enabled,omitempty"`

	// SignCommits creates a signed git commit for each managed document
	// mutation. Requires signing_key.
	SignCommits bool `yaml:"sign_commits,omitempty"`

	// VerifySignatures is the verification policy for consumers of this
	// root: "none", "warn", or "required". Required roots block managed
	// reads, indexed results, and tagged context injection when content
	// is not covered by trusted signed git history.
	VerifySignatures string `yaml:"verify_signatures,omitempty"`

	// RepoPath optionally points at the git repository to use for this
	// root. Empty means the root directory itself is the repository.
	RepoPath string `yaml:"repo_path,omitempty"`

	// SigningKey is the SSH private key used to sign managed commits.
	// Supports ~ expansion at startup.
	SigningKey string `yaml:"signing_key,omitempty"`

	// AllowedSigners is the older spelling of this root's seed signers
	// and is merged with roots.<name>.seed_signers, which is where new
	// configs should declare them.
	//
	// Both feed the same set, so keys listed here also govern admission:
	// they can establish the root and amend its .allowed_signers. That
	// is more authority than the name suggests, which is why the
	// seed_signers spelling exists.
	AllowedSigners []AllowedSigner `yaml:"allowed_signers,omitempty"`

	// Remote optionally makes this root a full git citizen that fetches,
	// verifies, and (in bidirectional mode) pushes against a shared remote.
	// Nil means local-only — behavior is byte-for-byte as it is without a
	// remote. The remote is untrusted transport: it conveys commits but
	// never confers trust, which comes only from signature verification
	// against the out-of-tree trust_anchor. Ignored unless Enabled.
	Remote *DocumentRootGitRemoteConfig `yaml:"remote,omitempty"`
}

// DocumentRootGitRemoteConfig configures optional sync of a git-backed
// document root against a shared remote.
type DocumentRootGitRemoteConfig struct {
	// URL is the remote git URL (SSH like user@host:path, ssh://…, or an
	// https URL). Required.
	URL string `yaml:"url"`

	// Branch is the remote branch to track. Empty means the sync uses
	// "main" (applied where the remote is consumed, not by config Load).
	Branch string `yaml:"branch,omitempty"`

	// Mode is required and explicit: "fetch" (pull the operator's signed
	// commits in) or "bidirectional" (also push thane's own signed commits
	// out). There is no default, so a one-way setup is never silent.
	Mode string `yaml:"mode"`

	// Interval is the sync poll cadence as a Go duration (e.g. "60s").
	// Empty means the sync uses 60s (applied where the remote is consumed,
	// not by config Load); "0" disables the timer (manual/trigger-only).
	Interval string `yaml:"interval,omitempty"`

	// TrustAnchor is an OPTIONAL out-of-tree OpenSSH allowed_signers file for
	// verification. It is not required: by default verification uses the
	// in-tree .allowed_signers (rendered from signing.allowed_signers), which
	// the sync engine checks safely because a fetch never rewrites the
	// worktree before the incoming range is verified. An out-of-tree anchor is
	// extra hardening for those who want the trust set removed from the synced
	// tree entirely — but it is not yet wired: set it and the root is refused
	// at construction. See #1135, #1147.
	TrustAnchor string `yaml:"trust_anchor,omitempty"`

	// Auth carries transport credentials only — never commit-signing
	// material.
	Auth DocumentRootGitRemoteAuthConfig `yaml:"auth,omitempty"`
}

// DocumentRootGitRemoteAuthConfig carries transport credentials for a remote.
// The transport key proves "who may reach the remote"; the signing key proves
// "who authored the commit" — they are deliberately distinct.
type DocumentRootGitRemoteAuthConfig struct {
	// SSHKey is the private key path for SSH transport, passed via
	// GIT_SSH_COMMAND with IdentitiesOnly. Must not equal git.signing_key.
	SSHKey string `yaml:"ssh_key,omitempty"`

	// KnownHosts is the pinned known_hosts file, required for an SSH url:
	// host-key verification is strict, with no trust-on-first-use.
	KnownHosts string `yaml:"known_hosts,omitempty"`

	// Token is an https personal access token. Documented, but SSH is the
	// recommended transport.
	Token string `yaml:"token,omitempty"`
}

// HomeAssistantConfig configures the connection to a Home Assistant
// instance. Both URL and Token must be set for the connection to be
// established; see [HomeAssistantConfig.Configured].
type HomeAssistantConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`

	// FloorAlias optionally gives HA floor registry entries an
	// operator-local semantic alias in model-facing metadata. Set to
	// "building" when floors represent buildings in this deployment.
	FloorAlias string `yaml:"floor_alias,omitempty"`

	// RegistryCacheTTL bounds how long entity/device/area/label/floor
	// registry snapshots are reused across native HA tool calls before a
	// refetch. These registries change only on HA config edits, so a
	// short window collapses the repeated (multi-MB at scale) registry
	// pulls that metadata-bearing tool calls would otherwise issue.
	// Accepts a Go duration string ("30s", "1m"); empty uses the 30s
	// default; "0" disables caching (always refetch). Live entity state
	// is never cached.
	RegistryCacheTTL string `yaml:"registry_cache_ttl,omitempty"`

	// IngestRateLimitPerMinute caps how many state changes per entity
	// the state watcher forwards per minute. Zero means no rate
	// limiting. The ingestion *filter* is runtime state (ingest-mode
	// entity subscriptions, #1192); this protective limit stays
	// operator policy in config.
	IngestRateLimitPerMinute int `yaml:"ingest_rate_limit_per_minute"`
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
	Name              string `yaml:"name"`               // Model identifier (e.g., "claude-opus-4-8")
	Provider          string `yaml:"provider"`           // Provider name: openai_compat, lmstudio, anthropic, ollama. Defaults to ollama when no resource is set
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
	// Provider name for this resource: "openai_compat" for any server
	// speaking the OpenAI chat protocol (vLLM, SGLang, llama-server,
	// NIM, and Ollama's own /v1 surface), "lmstudio" to add LM Studio's
	// load/unload and native inventory on top of that protocol,
	// "anthropic" for the hosted API, or "ollama" for Ollama's native
	// /api surface. Prefer "openai_compat" for new self-hosted
	// resources: the native Ollama client is retained for existing
	// deployments and does not receive new work. Default: ollama.
	Provider string `yaml:"provider"`
	// APIKey is an optional bearer/API key for providers that require auth.
	APIKey string `yaml:"api_key"`
	// IdleTTLSeconds asks supported local runners to keep models warm for
	// this many idle seconds after an inference request. LM Studio honors
	// this via the native `ttl` request field on inference endpoints.
	// Zero lets the runner use its default behavior.
	IdleTTLSeconds int `yaml:"idle_ttl_seconds"`
	// StreamIdleTimeout bounds how long this endpoint may send nothing
	// at all before a request is abandoned. It measures silence, not
	// duration: a slow generation is still allowed to be slow, and the
	// window resets on every byte received. Unset uses a default
	// generous enough to cover prefill on a large prompt; zero disables
	// the guard, which restores the older behavior where a server that
	// went quiet after returning headers hung the request until the
	// process restarted.
	StreamIdleTimeout time.Duration `yaml:"stream_idle_timeout"`
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

	// SessionIdleMinutes is the idle timeout for the summarizer
	// worker. Active sessions with no message activity for this many
	// minutes are silently closed and become eligible for summarization.
	// This is the sole owner of session idle close — message-channel
	// continuity across the rotation boundary is delivered via the
	// message_channel context provider's verbatim tail, not an LLM-
	// driven carry-forward.
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

// CompactionConfig controls when conversation compaction runs.
type CompactionConfig struct {
	// MaxTokens is the conversation token budget compaction defends;
	// compaction triggers at 70% of it. Default: 32000. The previous
	// hardcoded 8000 compacted interactive conversations far too
	// early for modern context windows (#1168).
	MaxTokens int `yaml:"max_tokens"`
}

// EpisodicConfig configures episodic memory context injection. When
// configured, the agent receives curated daily notes plus a JSON
// catalog of recent closed sessions in its system prompt, giving it
// continuity and discoverable archive entry points across sessions.
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

	// HistoryTokens is the approximate token budget for the recent-
	// sessions JSON catalog injected into the system prompt. Internally
	// converted to a byte cap (×4 ≈ 1 token / 4 bytes) when the
	// catalog is rendered. Default: 4000.
	HistoryTokens int `yaml:"history_tokens"`
}

// AgentConfig configures agent loop behavior. When DelegationRequired
// is true, the agent loop only advertises the tools listed in
// OrchestratorTools, steering the primary model toward delegation
// instead of direct tool use.
type AgentConfig struct {
	// OrchestratorTools lists tool names to advertise when
	// DelegationRequired is true. If empty, a sensible default set
	// is applied (the thane_now / thane_assign delegation front
	// doors plus lightweight memory tools).
	OrchestratorTools []string `yaml:"orchestrator_tools"`

	// DelegationRequired enables orchestrator tool gating. When false
	// (the default), all tools are available on every iteration.
	DelegationRequired bool `yaml:"delegation_required"`
}

// DelegateConfig configures the thane_* delegation tools' split-model
// execution behavior.
type DelegateConfig struct {
	// Profiles contains per-profile budget and timeout overrides. The map
	// key is the profile name (e.g., "general", "ha"). Only fields that
	// are set override the builtin defaults — omitted fields keep their
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

// CapabilityTagConfig is the operator overlay for a capability tag.
// Membership is computed by the resolver as the union of native catalog
// declarations, MCP server tag bindings, and operator-supplied Include,
// minus operator-supplied Exclude. Description, Core, and Protected
// override the compiled-in builtin tag spec when non-zero.
//
// Runtime-only tags such as owner and message_channel should be
// asserted by the integration or trusted channel binding that has
// current-run evidence, not by channel_tags or contact origin policy.
type CapabilityTagConfig struct {
	// Description is a human-readable summary shown in the capability
	// manifest so the agent knows what activating this tag provides. For
	// compiled-in tags, empty keeps the built-in description.
	Description string `yaml:"description"`

	// Include adds tools to this tag beyond what native catalog and MCP
	// server tag bindings already contribute. Use this for the rare
	// case where a tool's natural source does not declare the tag and
	// you want it grouped here at this site.
	Include []string `yaml:"include"`

	// Exclude removes tools from this tag's resolved membership at this
	// site. Useful when a default-shipped tool should not be reachable
	// in this deployment without removing it everywhere.
	Exclude []string `yaml:"exclude"`

	// Core tags cannot be deactivated. They are included in every
	// session regardless of channel or agent requests — operator-pinned
	// baseline scope.
	Core bool `yaml:"core"`

	// Protected tags are reserved for runtime trust and environment
	// assertions (for example an owner-authenticated conversation).
	// They are visible to the model when active, but cannot be toggled
	// via tag_activate or tag_deactivate.
	Protected bool `yaml:"protected"`

	// Tools is the resolved tool membership populated by the resolver
	// as native ∪ mcp ∪ Include − Exclude. Not user-supplied via YAML.
	// Available to runtime consumers that need the final set.
	Tools []string `yaml:"-"`
}

// Validate checks that the capability tag configuration is internally
// consistent. A description is required for non-builtin tags so the
// operator-defined intent is documented in the capability menu. Tools
// are optional — capability tags also gate talents, KB articles
// (via `tags:` and `tags_all:` frontmatter), and tag-context providers,
// so a content-only tag with no tool membership is a legitimate shape.
// Tag names are validated by the caller since they are map keys in the
// parent Config struct.
func (c CapabilityTagConfig) Validate(tagName string, builtin bool) error {
	if strings.TrimSpace(c.Description) == "" && !builtin {
		return fmt.Errorf("capability_tags.%s.description must not be empty", tagName)
	}
	return nil
}

// WorkspaceConfig configures the agent's sandboxed file system access.
// When Path is set, the agent can read and write files within that
// directory. All paths passed to file tools are resolved relative to
// Path and cannot escape it.
type WorkspaceConfig struct {
	// Path is the root directory for file operations: the writable
	// parent holding Thane-owned roots such as core/, knowledge/,
	// generated/, and scratchpad/.
	//
	// It is derived, never authored. A config lives at
	// {workspace}/core/config.yaml, so it states its workspace by
	// sitting there; a declared value could only agree or drift, and it
	// made the file describe one machine's layout. A config loaded from
	// outside a core takes the workspace from the -workspace flag.
	Path string `yaml:"-"`

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
//
// Every active subscription routes matching messages to a wake_loop
// target loop, which sees the message in its pending notifications
// and runs under its own Spec.Profile / SupervisorProfile. No new
// loop is spawned per message. Subscriptions with neither WakeLoop
// nor the legacy Wake field are ambient-awareness only.
//
// The pre-PR-T2b inline-routing form (Wake + InitialTags) is kept
// here for backwards compatibility: at config load time entries that
// declare only Wake are rewritten onto the built-in
// "mqtt-default-handler" loop with a WARN log, preserving any
// Instructions and InitialTags as wake target Tags. Remove the
// legacy fields from your YAML at your convenience.
type SubscriptionConfig struct {
	// Topic is the MQTT topic filter (e.g., "homeassistant/+/+/state",
	// "frigate/events"). Supports MQTT wildcard characters.
	Topic string `yaml:"topic"`

	// WakeLoop identifies the existing loop that should receive
	// matching MQTT messages as event-source notifications. When
	// the target name resolves to a registered loop at startup, this
	// is how every wake delivers; the target's own Spec.Profile
	// governs routing. Point this at "mqtt-default-handler" for the
	// built-in triage loop when you don't have a bespoke handler.
	WakeLoop *messages.LoopWakeTarget `yaml:"wake_loop,omitempty"`

	// Wake is the deprecated inline-routing form. Entries that use
	// it are auto-migrated onto the default handler at config load
	// (a one-shot upgrade). New subscriptions should declare
	// WakeLoop directly instead.
	Wake *router.LoopProfile `yaml:"wake,omitempty"`

	// InitialTags is the deprecated companion to Wake. Migrated
	// values flow into the resulting wake_loop target's Tags field
	// at load time, so per-iteration capability tag activation
	// continues to work even after the inline-routing form retires.
	InitialTags []string `yaml:"initial_tags,omitempty"`
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

// PersonConfig configures household member presence tracking. When Track
// contains entity IDs, the person tracker maintains in-memory state from Home
// Assistant, follows each person's linked device trackers for supported room
// providers, and injects a presence summary into the agent's system prompt on
// every wake.
type PersonConfig struct {
	// Track is a list of Home Assistant person entity IDs to monitor
	// (e.g., ["person.nugget", "person.dan"]). Each entry must begin
	// with "person.". An empty list disables person tracking.
	Track []string `yaml:"track"`

	// ContactBindings maps stable contact UUIDs to tracked Home
	// Assistant person entity IDs. When this key is present, including
	// as an empty map, signed configuration is the exact source of truth:
	// startup atomically replaces the stored bindings and CardDAV exposes
	// X-THANE-HA-PERSON as read-only. When absent, legacy CardDAV-managed
	// bindings remain enabled. Unverified configs cannot own or reconcile
	// bindings; this key is ignored in recovery mode.
	ContactBindings map[string]string `yaml:"contact_bindings"`

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

	// Tags lists capability tags assigned to every bridged MCP tool from
	// this server unless a per-tool override replaces them. Use this to
	// attach MCP tools to existing capability tags so the model gates
	// them behind tag_activate the same as native tools.
	Tags []string `yaml:"tags"`

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
// LoopProfile-only fields such as ExcludeTools are omitted until Signal
// grows explicit config for them.
func (c SignalRoutingConfig) LoopProfile() router.LoopProfile {
	// SignalRoutingConfig.QualityFloor stays string-shaped in its
	// own YAML schema for now — converting it is a separate
	// operator-config cleanup. Atoi here at the boundary so the
	// downstream [router.LoopProfile.QualityFloor] (int) is happy.
	// Unparseable values drop to zero ("unset") here;
	// [Config.validateSignal] enforces parseability at config-load
	// time so this conversion never sees a "high"-shaped string in
	// a validated config.
	floor, _ := strconv.Atoi(strings.TrimSpace(c.QualityFloor))
	return router.LoopProfile{
		Model:            c.Model,
		QualityFloor:     floor,
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
	// Example: ~/Thane/generated/media
	DefaultOutputPath string `yaml:"default_output_path"`

	// DatabasePath is the SQLite database file for engagement tracking.
	// If empty, defaults to {data_dir}/media_engagement.db at startup.
	DatabasePath string `yaml:"database_path"`
}

// ServiceLoopConfig is the shared configuration shape for thane's three
// model-facing core service loops — metacognitive, ego, and archivist.
// The three expose structurally identical operator-tunable knobs (the
// sleep envelope, jitter, supervisor odds, and routing floors) and differ
// only in their default values — seeded per loop by [Config.applyDefaults]
// — and in their documentation. Each loop keeps a distinct config key (see
// [MetacognitiveConfig], [EgoConfig], [ArchivistConfig]) so operator config
// is unchanged; the single type collapses what were three byte-identical
// structs plus three parallel applyDefaults/validate blocks (#999).
type ServiceLoopConfig struct {
	// Enabled seeds the loop's shipped definition document when no
	// document in {core}/loops declares it. When a document exists it is
	// authoritative — its own enabled: key decides whether the loop
	// runs, and this flag is ignored. Default: false.
	Enabled bool `yaml:"enabled"`

	// MinSleep is the minimum allowed sleep duration between iterations;
	// the loop cannot request a shorter sleep via set_next_sleep. Parsed
	// as a Go duration string. Default is per-loop (see the loop's section).
	MinSleep string `yaml:"min_sleep"`

	// MaxSleep is the maximum allowed sleep duration between iterations, a
	// Go duration string. Default is per-loop.
	MaxSleep string `yaml:"max_sleep"`

	// DefaultSleep is used when the LLM does not call set_next_sleep, a Go
	// duration string. Default is per-loop.
	DefaultSleep string `yaml:"default_sleep"`

	// Jitter is the sleep randomization factor (0.0–1.0). A value of 0.2
	// means the actual sleep varies by ±20% of the computed duration.
	// Default: 0.2. Set to 0.0 for deterministic timing.
	Jitter *float64 `yaml:"jitter,omitempty"`

	// SupervisorProbability is the per-wake chance (0.0–1.0) that this
	// iteration runs as a supervisor turn — a more capable model with the
	// loop's supervisor-turn prompt prefix. Default is per-loop. Set to 0.0
	// to disable supervisor turns entirely.
	SupervisorProbability *float64 `yaml:"supervisor_probability,omitempty"`

	// Router configures model routing for normal (non-supervisor)
	// iterations.
	Router RouterConfig `yaml:"router"`

	// SupervisorRouter configures model routing for supervisor turns
	// (frontier model with the augmented prompt).
	SupervisorRouter RouterConfig `yaml:"supervisor_router"`
}

// MetacognitiveConfig configures the self-regulating metacognitive loop.
// The loop runs perpetually in a background goroutine, using LLM calls to
// reason about the environment and self-determine its sleep duration
// between iterations. See issue #319. Defaults: min_sleep 15m, max_sleep
// 60m, default_sleep 30m, jitter 0.2, supervisor_probability 0.1,
// router.quality_floor 3 (supervisor 8).
type MetacognitiveConfig = ServiceLoopConfig

// EgoConfig configures the self-reflection ego loop. The loop runs as a
// service loop: bounded voluntary sleep, supervisor randomization, and a
// declared maintained-document output at self/ego.md. Replaces the legacy
// periodic_reflection scheduled task. Defaults: min_sleep 30m, max_sleep
// 24h, default_sleep 6h, jitter 0.2, supervisor_probability 0.2,
// router.quality_floor 5 (supervisor 8).
type EgoConfig = ServiceLoopConfig

// ArchivistConfig configures the memory archivist loop. The loop runs as a
// service loop: bounded voluntary sleep, supervisor randomization, and a
// declared maintained-document output at core/archivist.md. Where ego
// maintains self-reflection and metacognitive watches in-flight behavior,
// the archivist synthesizes durable knowledge across the memory silos into
// long-lived dossiers keyed by subject.
//
// Self-paced and pull-based by design: each iteration drains a durable
// work queue (subjects and closed sessions enqueued by producers) at the
// loop's own cadence rather than being woken per event. The Go-side
// session summarizer still fires on session close and stamps session
// metadata; it also enqueues the closed session for the archivist, which
// works above that layer, turning the corpus into per-subject dossiers.
//
// Defaults: min_sleep 15m, max_sleep 12h, default_sleep 1h, jitter 0.2,
// supervisor_probability 0.1, router.quality_floor 5 (supervisor 8).
type ArchivistConfig = ServiceLoopConfig

// RouterConfig holds routing hints used by the built-in services
// (metacognitive, ego, archivist) for both normal and supervisor
// turns. It is deliberately narrow — operator-tunable knobs only —
// and gets translated into the spec-level [router.LoopProfile] /
// [router.LoopProfile.QualityFloor] at hydration time. Replaces
// the previously-duplicated `MetacognitiveRouterConfig` and
// `EgoRouterConfig` types which were byte-identical apart from
// their default floors.
type RouterConfig struct {
	// QualityFloor is the minimum quality rating (1–10) for model
	// selection. Built-in service defaults: metacognitive uses 3
	// (normal) / 8 (supervisor); ego uses 5 (normal) / 8 (supervisor).
	QualityFloor int `yaml:"quality_floor"`
}

// LoopsConfig configures immutable loop definitions loaded from the
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

// deriveWorkspace sets workspace.path from the config's own location
// rather than trusting the file to describe where it lives.
//
// A config at {workspace}/core/config.yaml already states its workspace
// by sitting there, so a declared workspace.path can only agree or lie.
// Deriving it removes a field that could point the instance's roots,
// state, and identity somewhere other than the directory the config was
// loaded from — and a declared value that disagrees is rejected rather
// than silently overridden, because the disagreement itself means one of
// the two is wrong in a way the operator needs to see.
func (c *Config) deriveWorkspace() error {
	if c.loadedFrom == "" {
		return nil
	}
	abs, err := filepath.Abs(c.loadedFrom)
	if err != nil {
		return fmt.Errorf("resolve config path %q: %w", c.loadedFrom, err)
	}
	coreDir := filepath.Dir(abs)
	if filepath.Base(coreDir) != "core" {
		// A config outside core — the recovery escape hatch, or a
		// minimal config for a one-shot command — has no location to
		// derive from. The caller supplies the workspace from the
		// -workspace flag, or leaves it unset; an absent workspace is
		// already a handled state, and the features needing one say so.
		return nil
	}
	c.Workspace.Path = filepath.Dir(coreDir)
	return nil
}

// MarkUnverified records that this config came from outside the trust
// boundary, so the runtime can withhold the capabilities that an
// unverified instance should not have.
func (c *Config) MarkUnverified() {
	if c != nil {
		c.unverified = true
	}
}

// Unverified reports whether the running config came from outside the
// instance trust boundary.
//
// The runtime consults this to decide what to withhold. Verification is
// not a label on the file but a claim about who was entitled to write
// it, so an instance that cannot make the claim should not be able to
// act on the world as though it could.
func (c *Config) Unverified() bool {
	return c != nil && c.unverified
}

// LoadedFrom reports the path this config was read from, or empty when
// it was not produced by [Load]. The integrity gate needs it to verify
// the file it is actually running on.
func (c *Config) LoadedFrom() string {
	if c == nil {
		return ""
	}
	return c.loadedFrom
}

// CoreConfigPath returns the canonical config location for this
// instance: {workspace.path}/core/config.yaml. Empty when no workspace
// is configured.
func (c *Config) CoreConfigPath() string {
	if c == nil || strings.TrimSpace(c.Workspace.Path) == "" {
		return ""
	}
	return filepath.Join(c.CoreRoot(), "config.yaml")
}

// Load reads and parses a config whose workspace is derived from its own
// location. It is [LoadWithWorkspace] with no fallback workspace; the
// load pipeline and the post-load guarantee are documented there.
func Load(path string) (*Config, error) {
	return LoadWithWorkspace(path, "")
}

// LoadWithWorkspace reads a YAML configuration file, expands environment
// variables, applies defaults for any unset fields, and validates the
// result.
//
// After it returns a non-nil [Config], every field is usable without
// additional nil or empty-string checks. The load pipeline is:
//
//  1. Read the file.
//  2. Expand environment variables (e.g., ${HOME}, ${ANTHROPIC_API_KEY}).
//  3. Refuse retired keys via [rejectRetiredKeys], so a renamed or
//     removed key fails loudly instead of silently deconfiguring the
//     subsystem it used to govern.
//  4. Unmarshal YAML into a [Config].
//  5. Derive workspace.path from the config's own location via
//     [Config.deriveWorkspace], falling back to fallbackWorkspace when
//     the config lives outside any core.
//  6. Normalize via [Config.normalizeRoots] (desugar roots: into
//     legacy paths/doc_roots; emit deprecation warning for legacy
//     shape).
//  7. Apply defaults via [Config.applyDefaults].
//  8. Validate via [Config.Validate].
//
// The fallback in step 5 exists only for a config loaded from outside
// any core, where the -workspace flag is the sole remaining source. A
// derived workspace always wins: the config's own location is the more
// trustworthy of the two, and a fallback that could override it would
// reintroduce the drift that retiring workspace.path removed.
func LoadWithWorkspace(path, fallbackWorkspace string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	expanded := os.ExpandEnv(string(data))

	if err := rejectRetiredKeys([]byte(expanded)); err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, err
	}
	cfg.loadedFrom = path

	if err := cfg.deriveWorkspace(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Workspace.Path) == "" && strings.TrimSpace(fallbackWorkspace) != "" {
		cfg.Workspace.Path = strings.TrimSpace(fallbackWorkspace)
	}

	if err := cfg.normalizeRoots(); err != nil {
		return nil, err
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// rejectRetiredSigningKeys refuses the instance-wide signer list.
//
// Silently dropping it would leave every signed root with no declared
// trust set while the operator believed they had configured one — the
// failure this whole change exists to make impossible.
func rejectRetiredSigningKeys(signing *yaml.Node) error {
	if signing == nil || signing.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(signing.Content); i += 2 {
		if signing.Content[i].Value == "allowed_signers" {
			return fmt.Errorf("config declares signing.allowed_signers, which is now per root. Move each key to roots.<name>.seed_signers for the roots it should be able to establish — declaring it once for every root is what let a key trusted for a shared corpus also sign the config that decides what the system trusts")
		}
	}
	return nil
}

// rejectRetiredWorkspaceKeys refuses a declared workspace.path.
//
// Silently ignoring it would be worse than refusing: an operator who
// sets it believes they have pointed the instance somewhere, and the
// instance would run from a different directory without saying so. The
// error names the two things that actually decide the workspace now.
func rejectRetiredWorkspaceKeys(workspace *yaml.Node) error {
	if workspace == nil || workspace.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(workspace.Content); i += 2 {
		if workspace.Content[i].Value == "path" {
			return fmt.Errorf("config declares workspace.path, which is now derived from the config's own location ({workspace}/core/config.yaml) rather than authored. Remove the path: line — keep the rest of the workspace: block — and select a different instance with the -workspace flag instead")
		}
	}
	return nil
}

// rejectRetiredKeys returns an actionable error when the YAML
// document contains a key that was renamed or removed and whose
// silent ignore could leave a real subsystem misconfigured. yaml.v3's
// default is to ignore unknown keys, which is fine for fields where
// the surface area is small but dangerous for whole subsystem blocks
// (the Companion section, formerly platform:, is the canonical
// example: silent ignore would leave Companion unconfigured without
// any signal to the operator).
//
// The walker handles both top-level keys (platform → companion) and
// targeted nested-path checks: capability_tags.<tag>.tools (replaced
// by include/exclude) and mcp.servers[].default_tags (renamed to
// tags). Each retired key gets a specific message pointing the
// operator at the new shape.
func rejectRetiredKeys(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Defer parse-error reporting to the caller's main Unmarshal.
		return nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		valueNode := root.Content[i+1]
		switch keyNode.Value {
		case "platform":
			return fmt.Errorf("config has top-level platform: section, which was renamed to companion: in v0.9.x. Rename it (the field shape is unchanged) and re-load")
		case "signing":
			if err := rejectRetiredSigningKeys(valueNode); err != nil {
				return err
			}
		case "talents_dir":
			return fmt.Errorf("config declares talents_dir, which is now derived as {workspace}/core/talents rather than authored. Talents steer every turn, so they belong under the same signed history as the prompts beside them: move the directory into core (mv <talents_dir> {workspace}/core/talents), commit it, and remove the talents_dir: line")
		case "curator":
			return fmt.Errorf("config has top-level curator: section, which was renamed to archivist: when the loop became a self-paced queue consumer. Rename it (the field shape is unchanged) and re-load")
		case "workspace":
			if err := rejectRetiredWorkspaceKeys(valueNode); err != nil {
				return err
			}
		case "capability_tags":
			if err := rejectRetiredCapabilityTagKeys(valueNode); err != nil {
				return err
			}
		case "mcp":
			if err := rejectRetiredMCPKeys(valueNode); err != nil {
				return err
			}
		case "homeassistant":
			if err := rejectRetiredHomeAssistantKeys(valueNode); err != nil {
				return err
			}
		}
	}
	return nil
}

// rejectRetiredHomeAssistantKeys rejects the legacy
// homeassistant.subscribe block, retired when the state-watch
// ingestion filter became registry-derived (#1192): silently ignoring
// it would let a deploy quietly change what gets captured, since the
// old entity_globs no longer feed the filter.
func rejectRetiredHomeAssistantKeys(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "subscribe" {
			return fmt.Errorf("homeassistant.subscribe was retired: state-change capture now derives from the subscription registry (ingest/transitions/wake subscriptions plus person.track — the old entity_globs no longer feed the filter). Move rate_limit_per_minute to homeassistant.ingest_rate_limit_per_minute, re-declare any globs you still want as ingest-mode subscriptions after boot, and delete the subscribe: block")
		}
	}
	return nil
}

// rejectRetiredCapabilityTagKeys walks each capability_tags.<tag>
// entry and rejects the legacy tools: key, which was replaced by the
// additive include/exclude pair.
func rejectRetiredCapabilityTagKeys(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		tagName := node.Content[i].Value
		entry := node.Content[i+1]
		if entry == nil || entry.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(entry.Content); j += 2 {
			if entry.Content[j].Value == "tools" {
				return fmt.Errorf("capability_tags.%s.tools: was removed; capability membership is now native ∪ mcp ∪ include − exclude. Move site-specific additions to capability_tags.%s.include: and removals to capability_tags.%s.exclude:, then drop the tools: key", tagName, tagName, tagName)
			}
		}
	}
	return nil
}

// rejectRetiredMCPKeys walks mcp.servers[] entries and rejects the
// legacy default_tags: key, which was renamed to tags:.
func rejectRetiredMCPKeys(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "servers" {
			continue
		}
		seq := node.Content[i+1]
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for idx, entry := range seq.Content {
			if entry == nil || entry.Kind != yaml.MappingNode {
				continue
			}
			serverName := fmt.Sprintf("[%d]", idx)
			for j := 0; j+1 < len(entry.Content); j += 2 {
				if entry.Content[j].Value == "name" {
					serverName = entry.Content[j+1].Value
				}
			}
			for j := 0; j+1 < len(entry.Content); j += 2 {
				if entry.Content[j].Value == "default_tags" {
					return fmt.Errorf("mcp.servers[%s].default_tags: was renamed to tags: (drop the default_ prefix; semantics are unchanged)", serverName)
				}
			}
		}
	}
	return nil
}

// normalizeRoots desugars the unified roots: block into the legacy
// Paths and DocRoots maps so downstream consumers (resolver
// construction, doc-store options) keep working unchanged. It also
// rejects configs that declare both shapes simultaneously, and emits
// a deprecation warning for the legacy shape.
//
// Called from [Load] between yaml.Unmarshal and applyDefaults.
// Programmatic constructions that bypass Load can call this directly
// after populating Roots.
func (c *Config) normalizeRoots() error {
	if len(c.Roots) > 0 {
		if len(c.Paths) > 0 || len(c.DocRoots) > 0 {
			return fmt.Errorf("config: cannot declare both roots: and the legacy paths:/doc_roots: blocks; pick one (roots: is preferred)")
		}
		if c.Paths == nil {
			c.Paths = make(map[string]string, len(c.Roots))
		}
		if c.DocRoots == nil {
			c.DocRoots = make(map[string]DocumentRootConfig, len(c.Roots))
		}
		seen := make(map[string]string, len(c.Roots))
		for name, entry := range c.Roots {
			trimmed := strings.TrimSuffix(strings.TrimSpace(name), ":")
			if trimmed == "" {
				return fmt.Errorf("config: roots: contains an empty entry name")
			}
			if prev, ok := seen[trimmed]; ok {
				return fmt.Errorf("config: roots: keys %q and %q both canonicalize to %q", prev, name, trimmed)
			}
			seen[trimmed] = name
			pathValue := strings.TrimSpace(entry.Path)
			if trimmed == DossiersRootName && pathValue != "" {
				return dossiersPathMigrationError("roots.dossiers.path", entry.Path)
			}
			// Derived roots are reserved — their paths are always derived
			// from workspace.path. Dossiers rejects an explicit path above so
			// upgrades cannot silently redirect a prior custom root. The older
			// derived roots retain their path-ignoring compatibility behavior.
			if IsDerivedRootName(trimmed) {
				if pathValue != "" && !entryHasPolicy(entry) {
					slog.Default().Warn("config: derived root path is ignored (it comes from workspace.path); declare this root only when setting policy",
						"root", trimmed, "path", entry.Path)
				}
			} else {
				if pathValue == "" {
					return fmt.Errorf("config: roots.%s.path must be set; only the derived roots (%s, %s, %s, %s) take their path from workspace.path", trimmed, CoreRootName, SelfRootName, ContactsRootName, DossiersRootName)
				}
				c.Paths[trimmed] = entry.Path
			}
			if entryHasPolicy(entry) {
				c.DocRoots[trimmed] = DocumentRootConfig{
					Indexing:    entry.Indexing,
					Authoring:   entry.Authoring,
					Git:         entry.Git,
					Context:     entry.Context,
					SeedSigners: entry.SeedSigners,
				}
			}
		}
		// Clear Roots after desugaring so there is exactly one
		// canonical representation downstream and no risk of drift.
		c.Roots = nil
		return nil
	}

	if len(c.Paths) > 0 || len(c.DocRoots) > 0 {
		for name, path := range c.Paths {
			if strings.TrimSuffix(strings.TrimSpace(name), ":") == DossiersRootName && strings.TrimSpace(path) != "" {
				return dossiersPathMigrationError("paths.dossiers", path)
			}
		}
		slog.Default().Warn("config: paths: and doc_roots: are deprecated in favor of the unified roots: block; see docs/understanding/document-roots.md for the new shape")
	}
	return nil
}

func dossiersPathMigrationError(field, configuredPath string) error {
	return fmt.Errorf("config: %s %q is no longer accepted because dossiers is now a workspace-derived root: move or clone that corpus to {workspace.path}/dossiers, remove the explicit path, and retain its policy under roots.dossiers", field, configuredPath)
}

// entryHasPolicy reports whether a root entry has any policy fields
// set beyond its path. Used to decide whether to emit a DocRoots
// entry during normalization (entries with only a path get default
// policy and need no DocRoots row).
func entryHasPolicy(entry RootEntry) bool {
	if entry.Indexing != nil {
		return true
	}
	if strings.TrimSpace(entry.Authoring) != "" {
		return true
	}
	if entry.Context.Declared() {
		return true
	}
	if len(entry.SeedSigners) > 0 {
		return true
	}
	g := entry.Git
	return g.Enabled || g.SignCommits || g.VerifySignatures != "" ||
		g.RepoPath != "" || g.SigningKey != "" || len(g.AllowedSigners) > 0
}

// applyServiceLoopDefaults fills the unset fields of one core service-loop
// config with that loop's per-loop defaults. Only zero-valued fields are
// touched, so an operator-set value always wins. See [ServiceLoopConfig].
func applyServiceLoopDefaults(cfg *ServiceLoopConfig, minSleep, maxSleep, defaultSleep string, jitter, supervisorProb float64, floor, supervisorFloor int) {
	if cfg.MinSleep == "" {
		cfg.MinSleep = minSleep
	}
	if cfg.MaxSleep == "" {
		cfg.MaxSleep = maxSleep
	}
	if cfg.DefaultSleep == "" {
		cfg.DefaultSleep = defaultSleep
	}
	if cfg.Jitter == nil {
		cfg.Jitter = &jitter
	}
	if cfg.SupervisorProbability == nil {
		cfg.SupervisorProbability = &supervisorProb
	}
	if cfg.Router.QualityFloor == 0 {
		cfg.Router.QualityFloor = floor
	}
	if cfg.SupervisorRouter.QualityFloor == 0 {
		cfg.SupervisorRouter.QualityFloor = supervisorFloor
	}
}

// applyDefaults fills zero-value fields with sensible defaults. It is
// called automatically by [Load] and [Default]. After this method
// returns, callers can read any field without conditional fallbacks.
//
// Cross-field defaults are resolved here too — for example,
// Embeddings.BaseURL defaults to Models.OllamaURL when unset.
func (c *Config) applyDefaults() {
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

	if c.Listen.Port == 0 {
		c.Listen.Port = 8080
	}
	if c.Listen.Auth.SessionTTL == 0 {
		c.Listen.Auth.SessionTTL = 168 * time.Hour
	}
	c.DataDir = ResolveDataDir(c.Workspace.Path, c.DataDir)
	c.applyTLSDefaults()
	if c.TalentsDir == "" {
		// Derived, never authored: talents live inside core so they carry the
		// same signed history and the same cleanliness rule as the prompts
		// they sit beside. Falls back to a relative path only when there is no
		// workspace to derive from, which is the in-memory-config case tests
		// and one-shot commands use.
		if root := c.CoreRoot(); root != "" {
			c.TalentsDir = filepath.Join(root, "talents")
		} else {
			c.TalentsDir = "./talents"
		}
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
	if c.OpenAIAPI.Port == 0 {
		c.OpenAIAPI.Port = 8081
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
	// The archive idle timeout drives the summarizer worker's silent
	// close. Inherit from signal.session_idle_minutes when omitted (nil)
	// so users still get the same effective threshold without setting
	// both. Explicit 0 disables the worker's idle close.
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
	if c.Compaction.MaxTokens == 0 {
		c.Compaction.MaxTokens = 32000
	}
	if c.Episodic.HistoryTokens == 0 {
		c.Episodic.HistoryTokens = 4000
	}

	if c.Pricing == nil {
		c.Pricing = map[string]PricingEntry{
			// Current models (per-million USD, input/output).
			"claude-opus-4-8":   {InputPerMillion: 5.0, OutputPerMillion: 25.0},
			"claude-sonnet-4-6": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-haiku-4-5":  {InputPerMillion: 1.0, OutputPerMillion: 5.0},
			// Deprecated models kept for pricing historical usage records
			// (retire 2026-06-15 / 2026-04-19); note Opus 4 was $15/$75,
			// far above Opus 4.8's $5/$25.
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

	// Core service-loop defaults. The three loops share one config shape
	// ([ServiceLoopConfig]); only their default sleep envelope, supervisor
	// odds, and routing floors differ.
	//
	// The envelopes widen with the timescale each loop reasons over.
	// Metacognition watches in-flight behaviour, so it is the tightest —
	// but tens of minutes rather than the couple of minutes it once
	// defaulted to, which woke it far more often than it had new
	// behaviour to observe and spent supervisor turns on the repetition.
	// These match what the reference install converged on in practice.
	// The archivist sits between: an hour gives the corpus time to
	// accumulate new evidence between passes without going stale. Ego
	// reasons in hours to days.
	for _, d := range []struct {
		cfg                              *ServiceLoopConfig
		minSleep, maxSleep, defaultSleep string
		jitter, supervisorProb           float64
		floor, supervisorFloor           int
	}{
		{&c.Metacognitive, "15m", "60m", "30m", 0.2, 0.1, 3, 8},
		{&c.Ego, "30m", "24h", "6h", 0.2, 0.2, 5, 8},
		{&c.Archivist, "15m", "12h", "1h", 0.2, 0.1, 5, 8},
	} {
		applyServiceLoopDefaults(d.cfg, d.minSleep, d.maxSleep, d.defaultSleep, d.jitter, d.supervisorProb, d.floor, d.supervisorFloor)
	}

	if c.Agent.DelegationRequired && len(c.Agent.OrchestratorTools) == 0 {
		c.Agent.OrchestratorTools = []string{
			"thane_now",
			"thane_assign",
			"recall_fact",
			"remember_fact",
			"contact_save",
			"contact_lookup",
			"contact_owner",
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

// ResolveDataDir applies the data directory default and anchors a relative
// path to workspace. Config loaded from core always has a derived workspace,
// so its opaque runtime state follows the instance instead of the process's
// current working directory. Callers without a workspace retain the historical
// working-directory-relative behavior.
func ResolveDataDir(workspace, dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = "./db"
	}
	if filepath.IsAbs(dataDir) || strings.TrimSpace(workspace) == "" {
		return dataDir
	}
	return filepath.Join(workspace, dataDir)
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
	if c.OpenAIAPI.Enabled && (c.OpenAIAPI.Port < 1 || c.OpenAIAPI.Port > 65535) {
		return fmt.Errorf("openai_api.port %d out of range (1-65535)", c.OpenAIAPI.Port)
	}
	if err := c.validateTLS(); err != nil {
		return err
	}
	if err := c.validateListenAuth(); err != nil {
		return err
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
	if c.Identity.OperatorContactID != "" {
		operatorID, err := uuid.Parse(c.Identity.OperatorContactID)
		if err != nil || operatorID == uuid.Nil || operatorID.String() != c.Identity.OperatorContactID {
			return fmt.Errorf("identity.operator_contact_id %q must be a canonical non-nil UUID", c.Identity.OperatorContactID)
		}
		if strings.TrimSpace(c.Identity.OwnerContactName) != "" {
			return fmt.Errorf("identity.operator_contact_id and legacy identity.owner_contact_name are mutually exclusive")
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
		for _, tag := range srv.Tags {
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
	contactIDs := make([]string, 0, len(c.Person.ContactBindings))
	for contactID := range c.Person.ContactBindings {
		contactIDs = append(contactIDs, contactID)
	}
	sort.Strings(contactIDs)
	claimedPeople := make(map[string]string, len(contactIDs))
	for _, contactID := range contactIDs {
		parsed, err := uuid.Parse(contactID)
		if err != nil || parsed == uuid.Nil || parsed.String() != contactID {
			return fmt.Errorf("person.contact_bindings key %q must be a canonical non-nil contact UUID", contactID)
		}
		entityID := c.Person.ContactBindings[contactID]
		if !validHAPersonEntityID(entityID) {
			return fmt.Errorf("person.contact_bindings[%s] %q must match person.<object_id> (lowercase letters, digits, underscores)", contactID, entityID)
		}
		if !tracked[entityID] {
			return fmt.Errorf("person.contact_bindings[%s] references untracked entity %q", contactID, entityID)
		}
		if holder, exists := claimedPeople[entityID]; exists {
			return fmt.Errorf("person.contact_bindings assigns %q to both %s and %s", entityID, holder, contactID)
		}
		claimedPeople[entityID] = contactID
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
	if err := c.validateDocRoots(); err != nil {
		return err
	}
	if err := c.validateSigning(); err != nil {
		return err
	}
	if err := c.validateMetacognitive(); err != nil {
		return err
	}
	if err := c.validateArchivist(); err != nil {
		return err
	}
	if err := c.validateEgo(); err != nil {
		return err
	}
	if err := c.validateLoops(); err != nil {
		return err
	}
	if err := c.validateDelegate(); err != nil {
		return err
	}
	if err := c.Companion.Validate(); err != nil {
		return err
	}
	if err := c.validateModels(); err != nil {
		return err
	}
	return nil
}

func validHAPersonEntityID(entityID string) bool {
	const prefix = "person."
	if !strings.HasPrefix(entityID, prefix) || len(entityID) == len(prefix) {
		return false
	}
	for _, r := range entityID[len(prefix):] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
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

func (c *Config) validateDocRoots() error {
	for root, policy := range c.DocRoots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		if root == "" {
			return fmt.Errorf("doc_roots contains an empty root name")
		}
		switch strings.TrimSpace(policy.Authoring) {
		case "", "managed", "read_only", "restricted":
		default:
			return fmt.Errorf("doc_roots.%s.authoring %q must be one of [managed, read_only, restricted]", root, policy.Authoring)
		}
		if err := policy.Context.Validate(root); err != nil {
			return err
		}
		git := policy.Git
		switch strings.TrimSpace(git.VerifySignatures) {
		case "", "none", "warn", "required":
		default:
			return fmt.Errorf("doc_roots.%s.git.verify_signatures %q must be one of [none, warn, required]", root, git.VerifySignatures)
		}
		if git.SignCommits && !git.Enabled {
			return fmt.Errorf("doc_roots.%s.git.enabled must be true when sign_commits is true", root)
		}
		if git.Enabled && git.SignCommits && strings.TrimSpace(git.SigningKey) == "" {
			return fmt.Errorf("doc_roots.%s.git.signing_key is required when sign_commits is true", root)
		}
		if err := validateAllowedSigners(fmt.Sprintf("doc_roots.%s.git.allowed_signers", root), git.AllowedSigners); err != nil {
			return err
		}
		if err := validateGitRemote(root, git); err != nil {
			return err
		}
	}
	return nil
}

// validateGitRemote fail-closed-checks an optional remote-sync block. The
// structural invariants are enforced here; checks that need the resolved
// on-disk repo path (that trust_anchor is genuinely outside the tree) run at
// wiring time.
func validateGitRemote(root string, git DocumentRootGitConfig) error {
	r := git.Remote
	if r == nil {
		return nil
	}
	label := fmt.Sprintf("doc_roots.%s.git.remote", root)
	if !git.Enabled {
		return fmt.Errorf("%s requires doc_roots.%s.git.enabled", label, root)
	}
	if strings.TrimSpace(r.URL) == "" {
		return fmt.Errorf("%s.url is required", label)
	}
	switch strings.TrimSpace(r.Mode) {
	case "fetch":
	case "bidirectional":
		if !git.SignCommits {
			return fmt.Errorf("%s.mode=bidirectional requires git.sign_commits: thane must sign the commits it pushes", label)
		}
	case "":
		return fmt.Errorf("%s.mode is required and must be \"fetch\" or \"bidirectional\"", label)
	default:
		return fmt.Errorf("%s.mode %q must be \"fetch\" or \"bidirectional\"", label, r.Mode)
	}
	if v := strings.TrimSpace(r.Interval); v != "" && v != "0" {
		if _, err := time.ParseDuration(v); err != nil {
			return fmt.Errorf("%s.interval %q must be a Go duration like \"60s\" (or \"0\" to disable the timer): %w", label, r.Interval, err)
		}
	}
	if isSSHGitURL(r.URL) && strings.TrimSpace(r.Auth.KnownHosts) == "" {
		return fmt.Errorf("%s.auth.known_hosts is required for an SSH url (host keys are pinned; there is no trust-on-first-use)", label)
	}
	if key := strings.TrimSpace(r.Auth.SSHKey); key != "" && key == strings.TrimSpace(git.SigningKey) {
		return fmt.Errorf("%s.auth.ssh_key must not be the same key as git.signing_key: transport auth and commit signing are separate", label)
	}
	// trust_anchor is optional: verification defaults to the in-tree
	// .allowed_signers (rendered from signing.allowed_signers), which the sync
	// engine checks safely because a fetch never rewrites the worktree before
	// the incoming range is verified. An out-of-tree anchor is optional extra
	// hardening; it is validated where it is consumed, not here.
	return nil
}

// isSSHGitURL reports whether a git remote URL uses SSH transport (an ssh://
// URL or the scp-like [user@]host:path form), as opposed to https/git. It
// mirrors git's own rule: a URL with no scheme is scp-like when its first
// colon comes before any slash — which also catches the user-less "host:repo"
// form that must still require pinned host keys.
func isSSHGitURL(url string) bool {
	u := strings.TrimSpace(url)
	if strings.HasPrefix(u, "ssh://") {
		return true
	}
	if strings.Contains(u, "://") {
		return false
	}
	colon := strings.IndexByte(u, ':')
	if colon < 0 {
		return false // a local path, no remote host
	}
	slash := strings.IndexByte(u, '/')
	return slash < 0 || colon < slash
}

// validateSigning checks each root's declared seed signers, and that a
// root which signs its commits declares who may establish it.
func (c *Config) validateSigning() error {
	for root, policy := range c.DocRoots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		label := "roots." + root + ".seed_signers"
		if err := validateAllowedSigners(label, policy.SeedSigners); err != nil {
			return err
		}
		// A root that signs its history without saying who may establish
		// it has signed history with no admission: verification would
		// confirm a signature without anyone having decided whose.
		if policy.Git.Enabled && policy.Git.SignCommits && len(policy.SeedSigners) == 0 {
			return fmt.Errorf("roots.%s signs commits but declares no seed_signers; list the keys entitled to establish this root", root)
		}
	}
	return nil
}

// validateAllowedSigners validates a list of trusted signing keys, prefixing
// errors with label (e.g. "signing.allowed_signers" or
// "doc_roots.kb.git.allowed_signers"). It is fail-closed: a single malformed
// entry rejects the whole config rather than being silently dropped, because
// silently shrinking a trust set is worse than refusing to boot. It also
// rejects the same public key appearing twice in one list — redundant at
// best, a principal-spoofing typo at worst.
func validateAllowedSigners(label string, list []AllowedSigner) error {
	seen := make(map[string]string, len(list))
	for i, s := range list {
		entry := fmt.Sprintf("%s[%d]", label, i)
		if err := validateAllowedSigner(entry, s); err != nil {
			return err
		}
		blob, err := canonicalAllowedSignerKey(s.Key)
		if err != nil {
			return fmt.Errorf("%s.key: %w", entry, err)
		}
		if prev, ok := seen[blob]; ok {
			return fmt.Errorf("%s.key duplicates the key already declared for principal %q", entry, prev)
		}
		seen[blob] = strings.TrimSpace(s.Principal)
	}
	return nil
}

// validateAllowedSigner validates one trusted signing identity. Every field
// that renders into the space-delimited OpenSSH allowed_signers line is
// checked for the injection vectors that format is prone to: a principal
// with whitespace would corrupt the line, and a newline anywhere would
// smuggle an entire extra trusted key.
func validateAllowedSigner(label string, s AllowedSigner) error {
	if strings.TrimSpace(s.Principal) == "" {
		return fmt.Errorf("%s.principal is required", label)
	}
	// Check the raw principal, not a trimmed copy: leading or trailing
	// whitespace (a trailing newline especially) must be rejected, not
	// silently normalized away, since allowed_signers is space-delimited.
	if strings.IndexFunc(s.Principal, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%s.principal %q must not contain whitespace or control characters", label, s.Principal)
	}
	if strings.TrimSpace(s.Key) == "" {
		return fmt.Errorf("%s.key is required", label)
	}
	if _, err := canonicalAllowedSignerKey(s.Key); err != nil {
		return fmt.Errorf("%s.key: %w", label, err)
	}
	if strings.IndexFunc(s.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s.label must not contain control characters", label)
	}
	after, err := parseAllowedSignerTime(label, "valid_after", s.ValidAfter)
	if err != nil {
		return err
	}
	before, err := parseAllowedSignerTime(label, "valid_before", s.ValidBefore)
	if err != nil {
		return err
	}
	if after != nil && before != nil && !after.Before(*before) {
		return fmt.Errorf("%s.valid_after must be strictly before valid_before", label)
	}
	return nil
}

// parseAllowedSignerTime parses an optional RFC3339 validity bound. Operators
// author windows in RFC3339; conversion to the OpenSSH allowed_signers time
// format happens at render time.
func parseAllowedSignerTime(label, field, value string) (*time.Time, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, fmt.Errorf("%s.%s %q must be an RFC3339 timestamp: %w", label, field, value, err)
	}
	return &t, nil
}

// canonicalAllowedSignerKey parses an authorized_keys-form public key and
// returns its canonical "<type> <base64>" form with the comment stripped, so
// keys that differ only by comment or surrounding whitespace compare equal.
//
// It rejects any value carrying more than one key: ssh.ParseAuthorizedKey
// parses only the first line and returns the remainder in rest, so a value
// with an embedded newline and a second key would otherwise be silently
// accepted — exactly the injection the fail-closed rules exist to stop.
func canonicalAllowedSignerKey(key string) (string, error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return "", fmt.Errorf("not a valid SSH public key: %w", err)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", fmt.Errorf("value must contain exactly one SSH public key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

// validateServiceLoop checks one core service-loop config for internal
// consistency. It is a no-op when the loop is disabled. name prefixes the
// error messages (e.g. "metacognitive.min_sleep") so a failure points at
// the operator's config key.
func (c *Config) validateServiceLoop(name string, cfg ServiceLoopConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if c.Workspace.Path == "" {
		return fmt.Errorf("%s requires workspace.path (state file lives under workspace/core)", name)
	}
	minSleep, err := time.ParseDuration(cfg.MinSleep)
	if err != nil {
		return fmt.Errorf("%s.min_sleep %q: %w", name, cfg.MinSleep, err)
	}
	maxSleep, err := time.ParseDuration(cfg.MaxSleep)
	if err != nil {
		return fmt.Errorf("%s.max_sleep %q: %w", name, cfg.MaxSleep, err)
	}
	defaultSleep, err := time.ParseDuration(cfg.DefaultSleep)
	if err != nil {
		return fmt.Errorf("%s.default_sleep %q: %w", name, cfg.DefaultSleep, err)
	}
	if minSleep > maxSleep {
		return fmt.Errorf("%s.min_sleep (%s) exceeds max_sleep (%s)", name, minSleep, maxSleep)
	}
	if defaultSleep < minSleep || defaultSleep > maxSleep {
		return fmt.Errorf("%s.default_sleep (%s) must be between min_sleep (%s) and max_sleep (%s)",
			name, defaultSleep, minSleep, maxSleep)
	}
	if cfg.Jitter != nil && (*cfg.Jitter < 0 || *cfg.Jitter > 1.0) {
		return fmt.Errorf("%s.jitter %.2f must be in [0.0, 1.0]", name, *cfg.Jitter)
	}
	if cfg.SupervisorProbability != nil && (*cfg.SupervisorProbability < 0 || *cfg.SupervisorProbability > 1.0) {
		return fmt.Errorf("%s.supervisor_probability %.2f must be in [0.0, 1.0]", name, *cfg.SupervisorProbability)
	}
	return nil
}

// validateMetacognitive, validateEgo, and validateArchivist validate each
// core service loop. They delegate to [Config.validateServiceLoop]; the
// per-loop wrappers keep the call sites and tests stable.
func (c *Config) validateMetacognitive() error {
	return c.validateServiceLoop("metacognitive", c.Metacognitive)
}
func (c *Config) validateEgo() error       { return c.validateServiceLoop("ego", c.Ego) }
func (c *Config) validateArchivist() error { return c.validateServiceLoop("archivist", c.Archivist) }

// CoreRoot returns the fixed high-integrity core document root derived
// from [Workspace.Path]. When workspace.path is unset, CoreRoot returns
// the empty string.
func (c *Config) CoreRoot() string {
	if strings.TrimSpace(c.Workspace.Path) == "" {
		return ""
	}
	return filepath.Join(c.Workspace.Path, "core")
}

// IsDerivedRootName reports whether a root takes its path from the
// workspace rather than from roots:.
//
// The derived roots are load-bearing in a way ordinary declared roots are not:
// their stable refs depend on one workspace-relative location. Callers that
// need to treat "this root is missing" as a problem rather than a preference
// use this to distinguish them.
func IsDerivedRootName(name string) bool {
	return name == CoreRootName || name == SelfRootName || name == ContactsRootName || name == DossiersRootName
}

// SelfRoot returns the fixed document root holding what Thane writes
// about itself, derived from [Workspace.Path] exactly as [CoreRoot] is.
// When workspace.path is unset, SelfRoot returns the empty string.
//
// It is a sibling of core rather than a directory inside it because the
// two answer to different authorities. core is what an operator declares
// Thane to be and what constrains it — identity, config, talents, loop
// definitions — and an install should be able to hold every commit in it
// to an operator signature. self is what Thane makes of that: its
// self-concept, its running observations, its memory of its own work.
// Both want signed history; only one wants the operator as its only
// author, and a root is the unit that policy attaches to.
//
// Derived rather than declared, for the same reason core is. The core
// service loops write here on every install, so a root an operator had
// to remember to declare would leave a fresh install with nowhere for
// those outputs to land.
func (c *Config) SelfRoot() string {
	if strings.TrimSpace(c.Workspace.Path) == "" {
		return ""
	}
	return filepath.Join(c.Workspace.Path, SelfRootName)
}

// ContactsRoot returns the fixed document root holding contact dossiers,
// derived from [Workspace.Path] exactly as [CoreRoot] is. The root is opt-in:
// callers only register it when contacts is explicitly declared under roots:.
// When workspace.path is unset, ContactsRoot returns the empty string.
func (c *Config) ContactsRoot() string {
	if strings.TrimSpace(c.Workspace.Path) == "" {
		return ""
	}
	return filepath.Join(c.Workspace.Path, ContactsRootName)
}

// DossiersRoot returns the fixed document root holding dossiers for
// non-contact subjects, derived from [Workspace.Path] exactly as [CoreRoot]
// is. The root is opt-in: callers only register it when dossiers is explicitly
// declared under roots:. When workspace.path is unset, DossiersRoot returns
// the empty string.
func (c *Config) DossiersRoot() string {
	if strings.TrimSpace(c.Workspace.Path) == "" {
		return ""
	}
	return filepath.Join(c.Workspace.Path, DossiersRootName)
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

// SelfFile returns the absolute-or-relative path to a named file in the
// fixed self document root. When workspace.path is unset, SelfFile returns
// the empty string.
func (c *Config) SelfFile(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	root := c.SelfRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, name)
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

// validateSubscribe checks the Home Assistant state-watch ingestion
// configuration for consistency. The ingestion filter itself moved to a
// runtime registry (#1192); only the protective rate limit remains in
// config.
func (c *Config) validateSubscribe() error {
	if c.HomeAssistant.IngestRateLimitPerMinute < 0 {
		return fmt.Errorf("homeassistant.ingest_rate_limit_per_minute %d must be non-negative", c.HomeAssistant.IngestRateLimitPerMinute)
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
	// SignalRoutingConfig keeps QualityFloor as a string for now
	// (its own YAML cleanup is deferred). SignalRoutingConfig.LoopProfile()
	// silently drops unparseable values to 0, which Profile.Validate()
	// then accepts as "unset" — so a typo like `quality_floor: "high"`
	// would slip past Validate entirely. Parse explicitly here so the
	// operator sees a loud failure at config-load time.
	if raw := strings.TrimSpace(c.Signal.Routing.QualityFloor); raw != "" {
		if _, err := strconv.Atoi(raw); err != nil {
			return fmt.Errorf("signal.routing.quality_floor %q is not a valid integer", c.Signal.Routing.QualityFloor)
		}
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
