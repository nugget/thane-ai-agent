// Package tools defines the tools available to the agent.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/contextfmt"
	"github.com/nugget/thane-ai-agent/internal/integrations/media"
	"github.com/nugget/thane-ai-agent/internal/integrations/search"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	routepkg "github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/scheduler"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// Tool represents a callable tool.
type Tool struct {
	Name        string                                                         `json:"name"`
	Description string                                                         `json:"description"`
	Parameters  map[string]any                                                 `json:"parameters"`
	Handler     func(ctx context.Context, args map[string]any) (string, error) `json:"-"`
	// Core marks the tool as exempt from capability-tag filtering: it
	// stays in the catalog even when its tags (if any) aren't active.
	// Two distinct use cases ride this flag — meta-tools that must be
	// reachable from any scope so the tag system itself remains
	// navigable (tag_activate, tag_inspect, and friends), and
	// request-scoped tools layered in via WithRuntimeTools (where the
	// contract is "available for this run regardless of active tags").
	// See docs/understanding/tag-system.md, "Why Tool.Core exists".
	Core                 bool     `json:"-"`
	SkipContentResolve   bool     `json:"-"` // Exempt from prefix-to-content resolution.
	ContentResolveExempt []string `json:"-"` // Top-level arg keys that must remain literal during content resolution.
	CanonicalID          string   `json:"-"`
	Source               string   `json:"-"`
	Origin               string   `json:"-"`
	Tags                 []string `json:"-"`
}

// Registry holds available tools.
type Registry struct {
	tools              map[string]*Tool
	tagIndex           map[string][]string // tag → tool names
	ha                 *homeassistant.Client
	scheduler          *scheduler.Scheduler
	logger             *slog.Logger
	factTools          *knowledge.Tools
	contactTools       *contacts.Tools
	emailTools         *email.Tools
	notifier           *notifications.Sender
	notifRecords       *notifications.RecordStore
	notifRouter        *notifications.NotificationRouter
	notifDispatcher    CallbackDispatcher
	companionCaller    companionCallFunc
	forgeTools         forgeHandler
	fileTools          *FileTools
	shellExec          *ShellExec
	attachmentTools    *attachments.Tools
	tempFileStore      *TempFileStore
	usageStore         *usage.Store
	lensStore          *LensStore
	logIndexDB         *sql.DB
	workingMemoryStore *memory.WorkingMemoryStore
	archiveStore       *memory.ArchiveStore

	channelReactionHandlers map[string]ChannelReactionFunc

	modelRegistry                              *fleet.Registry
	modelRouter                                *routepkg.Router
	modelRegistrySyncRouter                    func()
	persistModelRegistryPolicy                 func(string, fleet.DeploymentPolicy) error
	deletePersistedModelRegistryPolicy         func(string) error
	persistModelRegistryResourcePolicy         func(string, fleet.ResourcePolicy) error
	deletePersistedModelRegistryResourcePolicy func(string) error
	loopDefinitionRegistry                     *looppkg.DefinitionRegistry
	loopDefinitionView                         func() *looppkg.DefinitionRegistryView
	commitLoopDefinitionSpec                   func(context.Context, looppkg.Spec, time.Time) error
	deletePersistedLoopDefinition              func(string) error
	persistLoopDefinitionPolicy                func(string, looppkg.DefinitionPolicy) error
	deletePersistedLoopDefinitionPolicy        func(string) error
	reconcileLoopDefinition                    func(context.Context, string) error
	cascadeWakeOnLoopDelete                    func(string) (removed, configRefs []string, err error)
	launchLoopDefinition                       func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error)
	liveLoopRegistry                           *looppkg.Registry
	launchLoop                                 func(context.Context, looppkg.Launch) (looppkg.LaunchResult, error)
	messageBus                                 *messages.Bus
	loopIntentDeps                             LoopIntentToolDeps

	contentResolver *ContentResolver
}

// NewEmptyRegistry creates an empty tool registry with no built-in tools.
// Use this for testing or when constructing a registry manually.
func NewEmptyRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// NewRegistry creates a tool registry with HA integration.
// NewRegistry builds the native tool registry. logger is the
// subsystem/loop logger tool handlers emit to (degraded-path warnings,
// etc.); pass nil in tests or contexts without one and it falls back to
// [slog.Default].
func NewRegistry(ha *homeassistant.Client, sched *scheduler.Scheduler, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Registry{
		tools:     make(map[string]*Tool),
		ha:        ha,
		scheduler: sched,
		logger:    logger,
	}
	r.registerBuiltins()
	r.registerFindEntity()     // Smart entity discovery
	r.registerHASearchStates() // Predicate search across live state
	r.registerHAListServices() // Service-catalog discovery (#1177)
	r.registerHAAutomationTools()
	r.registerHAAutomationTraces()     // Run-level debugging (#1178)
	r.registerHAAutomationVocabulary() // Target-scoped 2026.7 vocabulary discovery (#1176)
	return r
}

// log returns the registry's logger, defaulting to [slog.Default] when
// unset. Shallow-copy constructors (FilteredCopy, WithRuntimeTools,
// FilterByTags, NewEmptyRegistry) may leave logger nil, so handlers use
// this rather than r.logger directly — a degraded-path warning must
// never itself panic.
func (r *Registry) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// SetFactTools adds fact management tools to the registry.
func (r *Registry) SetFactTools(ft *knowledge.Tools) {
	r.factTools = ft
	r.registerFactTools()
}

// SetFileTools adds file operation tools to the registry.
func (r *Registry) SetFileTools(ft *FileTools) {
	r.fileTools = ft
	r.registerFileTools()
}

// FileTools returns the registered file tools, or nil when none are
// configured. Used by app wiring to install late-binding dependencies
// (e.g. the doc-root signature verifier) after the doc store exists.
func (r *Registry) FileTools() *FileTools {
	return r.fileTools
}

// SetShellExec adds shell execution tools to the registry.
func (r *Registry) SetShellExec(se *ShellExec) {
	r.shellExec = se
	r.registerShellExec()
}

// SetSearchManager adds the web_search tool to the registry.
func (r *Registry) SetSearchManager(mgr *search.Manager) {
	r.Register(&Tool{
		Name:        "web_search",
		Description: "Search the web for information. Returns titles, URLs, and snippets.",
		Parameters:  search.ToolDefinition(),
		Handler:     search.ToolHandler(mgr),
	})
}

// SetFetcher adds the web_fetch tool to the registry.
func (r *Registry) SetFetcher(f *search.Fetcher) {
	r.Register(&Tool{
		Name:        "web_fetch",
		Description: "Fetch a web page and extract its readable text content. Use to read articles, documentation, or any web page. Complements web_search.",
		Parameters:  search.FetchToolDefinition(),
		Handler:     search.FetchToolHandler(f),
	})
}

// SetMediaClient adds the media_transcript tool to the registry.
func (r *Registry) SetMediaClient(c *media.Client) {
	r.Register(&Tool{
		Name:        "media_transcript",
		Description: "Retrieve the transcript of a video or podcast episode. Supports YouTube, Vimeo, and other sources via yt-dlp. Returns metadata and cleaned transcript text. Transcripts are saved to disk for future reference.",
		Parameters:  media.ToolDefinition(),
		Handler:     media.ToolHandler(c),
	})
}

// SetAttachmentTools adds attachment query and analysis tools to the
// registry.
func (r *Registry) SetAttachmentTools(at *attachments.Tools) {
	r.attachmentTools = at
	r.registerAttachmentTools()
}

// SetTempFileStore adds the create_temp_file tool to the registry and
// stores the reference for label expansion and cleanup.
func (r *Registry) SetTempFileStore(tfs *TempFileStore) {
	r.tempFileStore = tfs
	r.registerTempFileTool()
}

// TempFileStore returns the temp file store, or nil if not configured.
// Used by the delegate executor for label expansion and by the agent
// loop for cleanup.
func (r *Registry) TempFileStore() *TempFileStore {
	return r.tempFileStore
}

// SetUsageStore adds the cost_summary tool to the registry so the agent
// can query its own token usage and API costs.
func (r *Registry) SetUsageStore(store *usage.Store) {
	r.usageStore = store
	r.registerCostSummary()
}

// SetContentResolver configures universal prefix-to-content resolution
// for tool arguments. When set, string arguments matching a registered
// prefix (temp:, kb:, scratchpad:, etc.) are replaced with file content
// before the handler runs. Tools with SkipContentResolve=true are exempt.
func (r *Registry) SetContentResolver(cr *ContentResolver) {
	r.contentResolver = cr
}

// SetNotificationRecords configures the notification record store used
// by the ha_notify tool to track actionable notifications.
func (r *Registry) SetNotificationRecords(rs *notifications.RecordStore) {
	r.notifRecords = rs
}

func (r *Registry) registerTempFileTool() {
	if r.tempFileStore == nil {
		return
	}

	r.Register(&Tool{
		Name: "create_temp_file",
		Description: "Create a temporary file for passing structured content to delegates. " +
			"Returns a semantic label (not a path). Reference the label as 'temp:LABEL' in " +
			"delegate task descriptions — the system expands labels to actual paths before " +
			"the delegate runs. Use this instead of file_write when passing data to delegates " +
			"to keep conversation history clean.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{
					"type":        "string",
					"description": "Semantic label for the temp file (e.g., 'issue_body', 'review_comments'). Alphanumeric, underscore, and hyphen only.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write to the temp file. Written as-is — no encoding needed.",
				},
			},
			"required": []string{"label", "content"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			label, _ := args["label"].(string)
			if label == "" {
				return "", fmt.Errorf("label is required")
			}

			rawContent, ok := args["content"]
			if !ok {
				return "", fmt.Errorf("content is required")
			}
			content, ok := rawContent.(string)
			if !ok {
				return "", fmt.Errorf("content must be a string")
			}

			convID := ConversationIDFromContext(ctx)
			result, err := r.tempFileStore.Create(ctx, convID, label, content)
			if err != nil {
				return "", err
			}

			return fmt.Sprintf("Temp file created with label '%s' (%d bytes written). Reference it as 'temp:%s' in delegate task descriptions.", result, len(content), result), nil
		},
	})
}

func (r *Registry) registerFactTools() {
	if r.factTools == nil {
		return
	}

	r.Register(&Tool{
		Name: "remember_fact",
		Description: "Write a stable, compact truth into long-term memory so it survives this conversation. " +
			"Call this the moment the owner reveals a preference, a household layout fact, a device mapping, " +
			"a routine, or corrects something past you got wrong — store, don't just acknowledge. " +
			"Saying 'noted' or 'got it' without calling this tool is the bug. " +
			"Duplicates overwrite cleanly; missed facts disappear and you'll meet the same surprise next week. " +
			"Each fact is a single self-contained key+value. " +
			"Do NOT use for project specs or design docs (use workspace files); " +
			"do NOT use for person-specific attributes (use contact_save).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"enum":        []string{"user", "home", "device", "routine", "preference"},
					"description": "Category: user (preferences, habits), home (household, rooms, pets), device (hardware, mappings), routine (schedules, workflows), preference (interaction/communication prefs)",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Unique identifier for this fact within the category",
				},
				"value": map[string]any{
					"type":        "string",
					"description": "The information to remember",
				},
				"source": map[string]any{
					"type":        "string",
					"description": "Where this information came from",
				},
				"subjects": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Subject keys this fact relates to. Prefix with type: entity:, contact:, phone:, zone:, camera:, location:. Example: [\"entity:binary_sensor.driveway\", \"zone:driveway\"]",
				},
			},
			"required": []string{"key", "value"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.factTools.Remember(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "recall_fact",
		Description: "Retrieve information from long-term memory. Can look up specific facts, list a category, or search.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"description": "Category to filter by",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Specific key to recall",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search term to find matching facts",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.factTools.Recall(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "forget_fact",
		Description: "Remove a fact from long-term memory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"description": "Category of the fact to forget",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Key of the fact to forget",
				},
			},
			"required": []string{"category", "key"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.factTools.Forget(string(argsJSON))
		},
	})
}

func (r *Registry) registerFileTools() {
	if r.fileTools == nil || !r.fileTools.Enabled() {
		return
	}

	r.Register(&Tool{
		Name:               "file_read",
		SkipContentResolve: true,
		Description:        "Read the contents of a file from the workspace. Use for accessing configuration, memory files, documentation, or any text file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file (relative to workspace root)",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Line number to start reading from (1-indexed, optional)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to read (optional)",
				},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			offset := 0
			limit := 0
			if o, ok := args["offset"].(float64); ok {
				offset = int(o)
			}
			if l, ok := args["limit"].(float64); ok {
				limit = int(l)
			}
			return r.fileTools.Read(ctx, path, offset, limit)
		},
	})

	r.Register(&Tool{
		Name:               "file_write",
		SkipContentResolve: true,
		Description:        "Write content to a file in the workspace. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file (relative to workspace root)",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write to the file",
				},
			},
			"required": []string{"path", "content"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if err := r.fileTools.Write(ctx, path, content); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
		},
	})

	r.Register(&Tool{
		Name:               "file_edit",
		SkipContentResolve: true,
		Description:        "Edit a file by replacing exact text. The old text must match exactly (including whitespace). Use this for precise, surgical edits.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file (relative to workspace root)",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Exact text to find and replace (must match exactly)",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "New text to replace the old text with",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			oldText, _ := args["old_text"].(string)
			newText, _ := args["new_text"].(string)
			if err := r.fileTools.Edit(ctx, path, oldText, newText); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully edited %s", path), nil
		},
	})

	r.Register(&Tool{
		Name:               "file_list",
		SkipContentResolve: true,
		Description:        "List files and directories in a workspace path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the directory (relative to workspace root, use '.' for root)",
				},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				path = "."
			}
			entries, err := r.fileTools.List(ctx, path)
			if err != nil {
				return "", err
			}
			if len(entries) == 0 {
				return "Directory is empty", nil
			}
			return fmt.Sprintf("Contents of %s:\n%s", path, strings.Join(entries, "\n")), nil
		},
	})

	r.Register(&Tool{
		Name:               "file_search",
		SkipContentResolve: true,
		Description:        "Search for files by name using glob patterns. Recursively searches a directory tree and returns matching file paths. Useful for finding configuration files, specific file types, or files with certain naming patterns.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern to match file names (e.g., '*.yaml', 'config.*', 'test_*.py')",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to search in (relative to workspace root, default '.')",
				},
				"max_depth": map[string]any{
					"type":        "integer",
					"description": "Maximum directory depth to search (default 10, max 20)",
				},
			},
			"required": []string{"pattern"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			path := "."
			if p, ok := args["path"].(string); ok && p != "" {
				path = p
			}
			maxDepth := 0
			if d, ok := args["max_depth"].(float64); ok {
				maxDepth = int(d)
			}
			return r.fileTools.Search(ctx, path, pattern, maxDepth)
		},
	})

	r.Register(&Tool{
		Name:               "file_grep",
		SkipContentResolve: true,
		Description:        "Search file contents for a regular expression pattern. Recursively searches files and returns matching lines with file paths and line numbers. Skips binary files and files larger than 1MB.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression pattern to search for in file contents",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to search in (relative to workspace root, default '.')",
				},
				"max_depth": map[string]any{
					"type":        "integer",
					"description": "Maximum directory depth to search (default 10, max 20)",
				},
				"case_insensitive": map[string]any{
					"type":        "boolean",
					"description": "Whether to perform case-insensitive matching (default false)",
				},
			},
			"required": []string{"pattern"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			path := "."
			if p, ok := args["path"].(string); ok && p != "" {
				path = p
			}
			maxDepth := 0
			if d, ok := args["max_depth"].(float64); ok {
				maxDepth = int(d)
			}
			caseInsensitive := false
			if ci, ok := args["case_insensitive"].(bool); ok {
				caseInsensitive = ci
			}
			return r.fileTools.Grep(ctx, path, pattern, maxDepth, caseInsensitive)
		},
	})

	r.Register(&Tool{
		Name:               "file_stat",
		SkipContentResolve: true,
		Description:        "Get detailed information about one or more files or directories. Returns type, size, permissions, and modification time. Supports batch queries with comma-separated paths.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{
					"type":        "string",
					"description": "Comma-separated file or directory paths to inspect (relative to workspace root)",
				},
			},
			"required": []string{"paths"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			paths, _ := args["paths"].(string)
			return r.fileTools.Stat(ctx, paths)
		},
	})

	r.Register(&Tool{
		Name:               "file_tree",
		SkipContentResolve: true,
		Description:        "Display a directory tree structure with indentation. Shows the hierarchy of files and directories with a summary count. Useful for understanding project layout.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Root directory for the tree (relative to workspace root, default '.')",
				},
				"max_depth": map[string]any{
					"type":        "integer",
					"description": "Maximum depth to display (default 3, max 10)",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path := "."
			if p, ok := args["path"].(string); ok && p != "" {
				path = p
			}
			maxDepth := 0
			if d, ok := args["max_depth"].(float64); ok {
				maxDepth = int(d)
			}
			return r.fileTools.Tree(ctx, path, maxDepth)
		},
	})
}

func (r *Registry) registerShellExec() {
	if r.shellExec == nil || !r.shellExec.Enabled() {
		return
	}

	r.Register(&Tool{
		Name:        "exec",
		Description: "Execute a shell command. Use for system administration, network diagnostics (ping, curl, traceroute), building software, or any task requiring shell access.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command to execute",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Timeout in seconds (optional, default 30, max 300)",
				},
			},
			"required": []string{"command"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			command, _ := args["command"].(string)
			timeout := 0
			if t, ok := args["timeout"].(float64); ok {
				timeout = int(t)
			}

			result, err := r.shellExec.Exec(ctx, command, timeout)
			if err != nil {
				return "", err
			}

			// Format result for LLM
			var output strings.Builder
			if result.Stdout != "" {
				output.WriteString(result.Stdout)
			}
			if result.Stderr != "" {
				if output.Len() > 0 {
					output.WriteString("\n\n[stderr]\n")
				}
				output.WriteString(result.Stderr)
			}
			if result.ExitCode != 0 {
				output.WriteString(fmt.Sprintf("\n\n[exit code: %d]", result.ExitCode))
			}
			if result.TimedOut {
				output.WriteString("\n\n[command timed out]")
			}
			if result.Error != "" {
				output.WriteString(fmt.Sprintf("\n\n[error: %s]", result.Error))
			}

			if output.Len() == 0 {
				return "(no output)", nil
			}
			return output.String(), nil
		},
	})
}

func (r *Registry) registerBuiltins() {
	// Get entity state
	r.Register(&Tool{
		Name:        "ha_get_state",
		Description: "Get the current state of a Home Assistant entity. Use this to check if lights are on, doors are open, temperatures, etc.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The entity ID (e.g., light.living_room, sensor.temperature, binary_sensor.front_door)",
				},
				"include": EntityMetadataIncludeParameter(),
			},
			"required": []string{"entity_id"},
		},
		Handler: r.handleGetState,
	})

	// List entities by domain or entity_id glob
	r.Register(&Tool{
		Name:        "ha_list_entities",
		Description: "List Home Assistant entities by domain and/or an entity_id glob. Use domain for a whole domain (all lights); use pattern for substring/cross-domain matching (e.g. binary_sensor.*door*, *_temperature). At least one of domain or pattern is required; when both are given they combine (AND).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain": map[string]any{
					"type":        "string",
					"description": "List every entity in this exact domain (e.g., light, switch, sensor, binary_sensor, climate, cover). Optional when pattern is supplied.",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob over the full entity_id (path.Match syntax, '*' matches any run of characters): binary_sensor.*door*, *_temperature, light.office_*. Optional when domain is supplied; combined with domain as an AND.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of entities to return (default 20, max 100)",
				},
				"include_hidden": map[string]any{
					"type":        "boolean",
					"description": "By default, operator-hidden entities (registry hidden_by) are excluded and their count reported as hidden_excluded. Set true to include them, each marked hidden.",
				},
				"include": EntityMetadataIncludeParameter(),
			},
			// Encode the handler's "at least one of domain or pattern"
			// rule in the schema so the model/tooling won't generate an
			// empty {} call. The handler still enforces it at runtime.
			"anyOf": []map[string]any{
				{"required": []string{"domain"}},
				{"required": []string{"pattern"}},
			},
		},
		Handler: r.handleListEntities,
	})

	// Control device - combined find + action (preferred tool for voice control)
	r.Register(&Tool{
		Name:        "ha_control_device",
		Description: "Control a device by description. Finds the device first, then performs the action. USE THIS for voice commands like 'turn on the kitchen light'.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{
					"type":        "string",
					"description": "Device description (e.g., 'kitchen light', 'office lamp', 'bedroom fan')",
				},
				"area": map[string]any{
					"type":        "string",
					"description": "Area/room name (e.g., 'office', 'kitchen', 'bedroom')",
				},
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"turn_on", "turn_off", "toggle", "set_brightness", "set_color"},
					"description": "Action to perform",
				},
				"brightness": map[string]any{
					"type":        "integer",
					"description": "Brightness 0-100 (for set_brightness)",
				},
				"color": map[string]any{
					"type":        "string",
					"description": "Color name (for set_color, e.g., 'red', 'blue', 'purple')",
				},
			},
			"required": []string{"description", "action"},
		},
		Handler: r.handleControlDevice,
	})

	// Call service (low-level, use ha_control_device for voice commands)
	r.Register(&Tool{
		Name: "ha_call_service",
		Description: "Low-level Home Assistant service call. Address one verified entity_id, OR a target block to fan out across an area, floor, label, or device in a single call — \"turn off the office lights\" is one call with target.area_id, not N entity calls. " +
			"ha_list_services shows which services accept targets (accepts_target). HA skips hidden entities in area/floor/label targets — that is operator curation, not an error. " +
			"For voice-style commands against one device, prefer ha_control_device.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain": map[string]any{
					"type":        "string",
					"description": "The service domain (e.g., light, switch, climate, lock)",
				},
				"service": map[string]any{
					"type":        "string",
					"description": "The service to call (e.g., turn_on, turn_off, set_temperature, lock)",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The EXACT entity ID (must be verified, not guessed). Provide this or target.",
				},
				"target": map[string]any{
					"type":        "object",
					"description": "Fan-out addressing: any of entity_id, device_id, area_id, floor_id, label_id (string or array each). Areas, floors, labels, and devices accept human names (\"Office\") as well as registry IDs — names resolve case-insensitively, unknown references fail fast with the known names. Provide this or entity_id.",
					"properties": map[string]any{
						"entity_id": map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"device_id": map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"area_id":   map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"floor_id":  map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"label_id":  map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
					},
				},
				"data": map[string]any{
					"type":        "object",
					"description": "Additional service data (e.g., brightness, temperature)",
				},
			},
			"required": []string{"domain", "service"},
		},
		Handler: r.handleCallService,
	})

	// Schedule task
	r.Register(&Tool{
		Name:        "task_schedule",
		Description: "Schedule a future action. Use for reminders, delayed commands, or recurring tasks.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Human-readable name for the task",
				},
				"when": map[string]any{
					"type":        "string",
					"description": "When to run: ISO timestamp, duration (e.g., '30m', '2h'), or 'in 30 minutes'",
				},
				"action": map[string]any{
					"type":        "string",
					"description": "What to do when the task fires (message to process)",
				},
				"repeat": map[string]any{
					"type":        "string",
					"description": "Optional: repeat interval (e.g., '1h', '24h', 'daily')",
				},
			},
			"required": []string{"name", "when", "action"},
		},
		Handler: r.handleScheduleTask,
	})

	// List tasks
	r.Register(&Tool{
		Name:        "task_list",
		Description: "List scheduled tasks.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"enabled_only": map[string]any{
					"type":        "boolean",
					"description": "Only show enabled tasks (default: true)",
				},
			},
		},
		Handler: r.handleListTasks,
	})

	// Cancel task
	r.Register(&Tool{
		Name:        "task_cancel",
		Description: "Cancel a scheduled task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task ID to cancel",
				},
			},
			"required": []string{"task_id"},
		},
		Handler: r.handleCancelTask,
	})

	// Get version/build info
	r.Register(&Tool{
		Name:        "get_version",
		Description: "Get Thane's version, build info, git commit, and uptime. Use when asked about your version or to diagnose issues.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			info := buildinfo.RuntimeInfo()
			out, _ := json.MarshalIndent(info, "", "  ")
			return string(out), nil
		},
	})
}

// Register adds a tool to the registry.
func (r *Registry) Register(t *Tool) {
	if spec, ok := toolcatalog.LookupBuiltinToolSpec(t.Name); ok {
		if t.CanonicalID == "" {
			t.CanonicalID = spec.CanonicalID
		}
		if t.Source == "" {
			t.Source = string(spec.Source)
		}
		t.Tags = mergeUniqueStrings(spec.Tags, t.Tags)
	}
	if t.CanonicalID == "" {
		t.CanonicalID = t.Name
	}
	if t.Source == "" {
		t.Source = string(toolcatalog.NativeToolSource)
	}
	r.tools[t.Name] = t
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) *Tool {
	return r.tools[name]
}

// List returns all tools for the LLM, sorted by name. Deterministic
// order is required for Anthropic prompt caching: tools land first in
// the cache key, so a randomized order (Go map iteration) makes every
// turn miss the prefix even when the tool set is unchanged.
func (r *Registry) List() []map[string]any {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return result
}

// AllToolNames returns the names of all registered tools, sorted.
func (r *Registry) AllToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FilteredCopy creates a new Registry containing only the named tools.
// Tools not found in the source are silently skipped. The returned
// registry shares tool handlers with the source but has its own map.
func (r *Registry) FilteredCopy(names []string) *Registry {
	filtered := &Registry{
		tools:           make(map[string]*Tool, len(names)),
		contentResolver: r.contentResolver,
		tagIndex:        r.tagIndex,
		logger:          r.logger,
	}
	for _, name := range names {
		if t := r.tools[name]; t != nil {
			filtered.tools[name] = t
		}
	}
	return filtered
}

// FilteredCopyExcluding creates a new Registry containing all tools
// except those in the exclude list.
func (r *Registry) FilteredCopyExcluding(exclude []string) *Registry {
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	filtered := &Registry{
		tools:           make(map[string]*Tool, len(r.tools)),
		contentResolver: r.contentResolver,
		tagIndex:        r.tagIndex,
		logger:          r.logger,
	}
	for name, t := range r.tools {
		if !skip[name] {
			filtered.tools[name] = t
		}
	}
	return filtered
}

// WithRuntimeTools creates a shallow registry copy with request-scoped
// runtime tools layered over the global registry. Runtime tools are
// intentionally not registered on the source registry; they belong only
// to one model run.
func (r *Registry) WithRuntimeTools(runtime []*Tool) *Registry {
	if len(runtime) == 0 {
		return r
	}
	filtered := &Registry{
		tools:           make(map[string]*Tool, len(r.tools)+len(runtime)),
		contentResolver: r.contentResolver,
		tagIndex:        r.tagIndex,
		logger:          r.logger,
	}
	for name, t := range r.tools {
		filtered.tools[name] = t
	}
	for _, t := range runtime {
		if t == nil || strings.TrimSpace(t.Name) == "" {
			continue
		}
		cp := *t
		cp.Name = strings.TrimSpace(cp.Name)
		cp.Core = true
		filtered.Register(&cp)
	}
	return filtered
}

// WithDynamicTools creates a shallow registry copy with dynamically-sourced
// tools layered over the global registry, plus tag→tool-name additions
// merged into a copied tag index so the new tools resolve under their tags
// via [Registry.FilterByTags].
//
// Unlike [Registry.WithRuntimeTools], dynamic tools are NOT marked Core:
// they stay tag-gated, so they only reach the model on a turn that has
// activated their tag. This is what localizes prompt-cache churn when a
// companion (macOS) source adds or drops tools — turns that have not
// activated the companion tag are unaffected.
//
// The shared registry and its tag index are never mutated (both are
// lock-free and assumed frozen after startup); a copy is taken only when
// there is something to add. Returns the receiver unchanged when both
// inputs are empty.
func (r *Registry) WithDynamicTools(extra []*Tool, tagAdditions map[string][]string) *Registry {
	if len(extra) == 0 && len(tagAdditions) == 0 {
		return r
	}

	filtered := &Registry{
		tools:           make(map[string]*Tool, len(r.tools)+len(extra)),
		contentResolver: r.contentResolver,
		logger:          r.logger,
	}
	for name, t := range r.tools {
		filtered.tools[name] = t
	}
	for _, t := range extra {
		if t == nil || strings.TrimSpace(t.Name) == "" {
			continue
		}
		cp := *t
		cp.Name = strings.TrimSpace(cp.Name)
		// Force non-Core regardless of what the source set: dynamic tools
		// must stay tag-gated so they never bypass FilterByTags and leak
		// into turns that haven't activated their tag. This is what keeps
		// prompt-cache churn isolated to companion-tagged turns.
		cp.Core = false
		filtered.Register(&cp)
	}

	// Carry the tag index forward. Share it when there are no additions
	// (matching the other shallow-copy helpers); otherwise merge additions
	// into a fresh map so the shared index is left untouched.
	if len(tagAdditions) == 0 {
		filtered.tagIndex = r.tagIndex
	} else {
		merged := make(map[string][]string, len(r.tagIndex)+len(tagAdditions))
		for tag, names := range r.tagIndex {
			merged[tag] = names
		}
		for tag, names := range tagAdditions {
			merged[tag] = mergeUniqueStrings(merged[tag], names)
		}
		filtered.tagIndex = merged
	}

	return filtered
}

// MetadataTagIndex builds a tag-to-tool mapping from per-tool default
// metadata. Tags with no registered tools are omitted.
func (r *Registry) MetadataTagIndex() map[string][]string {
	if len(r.tools) == 0 {
		return nil
	}
	tagIndex := make(map[string][]string)
	for name, t := range r.tools {
		for _, tag := range t.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			tagIndex[tag] = append(tagIndex[tag], name)
		}
	}
	if len(tagIndex) == 0 {
		return nil
	}
	for tag := range tagIndex {
		sort.Strings(tagIndex[tag])
	}
	return tagIndex
}

// SetTagIndex builds the tag-to-tool mapping from config. Each tag
// name maps to a list of tool names. Tools not found in the registry
// are silently skipped (they may not be registered yet or the MCP
// server may be down).
func (r *Registry) SetTagIndex(tags map[string][]string) {
	r.tagIndex = make(map[string][]string, len(tags))
	for tag, toolNames := range tags {
		r.tagIndex[tag] = toolNames
	}
}

// FilterByTags creates a new Registry containing only the tools that
// belong to at least one of the given tags, plus any tools marked as
// Core. If tags is empty or the tag index is nil, returns a copy of
// the full registry.
//
// Both paths propagate tagIndex to the returned registry, matching
// the convention of [FilteredCopy], [FilteredCopyExcluding], and
// [WithRuntimeTools]. Without the propagation, tag-aware operations
// on the result misbehave in two distinct ways: [TaggedToolNames]
// returns nil for every tag (loses the tag→tool mapping entirely),
// and a chained FilterByTags call takes the nil-index early-return
// path so it stops narrowing — returning the full set unfiltered
// instead of the intended subset. Either failure mode is a latent
// bug; carrying tagIndex forward prevents both.
func (r *Registry) FilterByTags(tags []string) *Registry {
	if len(tags) == 0 || r.tagIndex == nil {
		// No filtering — return a shallow copy with all tools.
		filtered := &Registry{
			tools:           make(map[string]*Tool, len(r.tools)),
			contentResolver: r.contentResolver,
			tagIndex:        r.tagIndex,
			logger:          r.logger,
		}
		for name, t := range r.tools {
			filtered.tools[name] = t
		}
		return filtered
	}

	allowed := make(map[string]bool)
	for _, tag := range tags {
		for _, name := range r.tagIndex[tag] {
			allowed[name] = true
		}
	}

	filtered := &Registry{
		tools:           make(map[string]*Tool, len(allowed)),
		contentResolver: r.contentResolver,
		tagIndex:        r.tagIndex,
		logger:          r.logger,
	}
	for name, t := range r.tools {
		if allowed[name] || t.Core {
			filtered.tools[name] = t
		}
	}
	return filtered
}

// TaggedToolNames returns the tool names belonging to a tag. Returns
// nil for unknown tags.
func (r *Registry) TaggedToolNames(tag string) []string {
	if r.tagIndex == nil {
		return nil
	}
	return r.tagIndex[tag]
}

// Execute runs a tool by name with given arguments.
func (r *Registry) Execute(ctx context.Context, name string, argsJSON string) (string, error) {
	tool := r.tools[name]
	if tool == nil {
		return "", &ErrToolUnavailable{ToolName: name}
	}

	var args map[string]any
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}

	// Universal prefix-to-content resolution. Bare prefix references
	// (temp:LABEL, kb:file.md, etc.) in argument values are recursively
	// resolved to file content before the handler runs. temp: references
	// always error on failure (missing label or unconfigured store);
	// path prefix failures pass through silently.
	if !tool.SkipContentResolve && r.contentResolver != nil && args != nil {
		resolveArgs := args
		filtered := false
		if len(tool.ContentResolveExempt) > 0 {
			exempt := make(map[string]struct{}, len(tool.ContentResolveExempt))
			for _, key := range tool.ContentResolveExempt {
				key = strings.TrimSpace(key)
				if key != "" {
					exempt[key] = struct{}{}
				}
			}
			if len(exempt) > 0 {
				filteredArgs := make(map[string]any, len(args))
				for key, value := range args {
					if _, skip := exempt[key]; skip {
						continue
					}
					filteredArgs[key] = value
				}
				resolveArgs = filteredArgs
				filtered = true
			}
		}
		if err := r.contentResolver.ResolveArgs(ctx, resolveArgs); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if filtered {
			for key, value := range resolveArgs {
				args[key] = value
			}
		}
	}

	return tool.Handler(ctx, args)
}

func mergeUniqueStrings(parts ...[]string) []string {
	seen := make(map[string]bool)
	var merged []string
	for _, part := range parts {
		for _, item := range part {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			merged = append(merged, item)
		}
	}
	return merged
}

// Tool handlers

func (r *Registry) handleGetState(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	state, err := r.ha.GetState(ctx, entityID)
	if err != nil {
		if IsHAEntityNotFound(err) {
			return SuggestEntityNotFound(ctx, r.ha, entityID), nil
		}
		return "", err
	}

	include, err := ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return "", err
	}
	var metadata *homeassistant.EntityMetadata
	if include.Any() {
		bundle, err := fetchHAEntityMetadataBundleForEntityIDs(ctx, r.ha, include, []string{entityID})
		if err != nil {
			return "", err
		}
		metadata = bundle.metadata(entityID, state)
	}

	return FormatEntityStateWithMetadata(state, metadata), nil
}

// FormatEntityState formats a Home Assistant entity state for LLM
// consumption. Used by ha_get_state, ha_control_device post-action verification,
// and context injection.
func FormatEntityState(state *homeassistant.State) string {
	return contextfmt.Format(state, time.Now())
}

// FormatEntityStateWithMetadata formats a Home Assistant entity state
// through the same context renderer used by watched-entity injection,
// attaching resolved HA registry metadata when present.
func FormatEntityStateWithMetadata(state *homeassistant.State, metadata *homeassistant.EntityMetadata) string {
	return contextfmt.FormatWithMetadata(state, time.Now(), metadata)
}

func (r *Registry) handleListEntities(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	domain := strings.TrimSpace(stringArgValue(args, "domain"))
	pattern := strings.TrimSpace(stringArgValue(args, "pattern"))
	if domain == "" && pattern == "" {
		return "", fmt.Errorf("at least one of domain or pattern is required")
	}
	// Validate the glob up-front so a malformed pattern is a clear error
	// rather than a silent no-match. Once this passes the per-entity
	// matches below cannot error.
	if pattern != "" {
		if err := homeassistant.ValidateEntityGlob(pattern); err != nil {
			return "", fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
	}

	limit, err := boundedIntArg(args, "limit", 20, maxHAListEntitiesLimit)
	if err != nil {
		return "", err
	}
	include, err := ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return "", err
	}
	includeHidden, _ := args["include_hidden"].(bool)

	states, err := r.ha.GetStates(ctx)
	if err != nil {
		return "", err
	}

	// Visibility needs the registry snapshot before the limit so hidden
	// entities are dropped (or marked) up front rather than padding the
	// page. Registry is TTL-cached (#1185). Fail open on a registry
	// error: keep enumeration usable and show everything (nil map =
	// "no visibility info, no filtering") rather than erroring the tool.
	visEntries, regErr := entityRegistryByID(ctx, r.ha)
	if regErr != nil {
		r.log().Warn("ha_list_entities: visibility filter degraded; entity registry unavailable", "error", regErr)
		visEntries = nil
	}

	var matches []haListEntityItem
	var matchEntityIDs []string
	var matchStates []homeassistant.State
	total := 0
	hiddenExcluded := 0
	now := time.Now()
	prefix := ""
	if domain != "" {
		prefix = domain + "."
	}
	for _, s := range states {
		if prefix != "" && !strings.HasPrefix(s.EntityID, prefix) {
			continue
		}
		if pattern != "" {
			if ok, _ := homeassistant.MatchEntityGlob(pattern, s.EntityID); !ok {
				continue
			}
		}
		if !includeHidden && isEntityHidden(visEntries[s.EntityID]) {
			hiddenExcluded++
			continue
		}
		total++
		if len(matches) >= limit {
			continue
		}
		item := haListEntityItem{
			EntityID: s.EntityID,
			State:    haSemanticState(s),
		}
		item.Since, item.Updated = haRecencyDelta(s, now)
		if friendly, ok := s.Attributes["friendly_name"].(string); ok {
			item.FriendlyName = friendly
		}
		if includeHidden {
			item.Hidden = isEntityHidden(visEntries[s.EntityID])
		}
		matches = append(matches, item)
		matchEntityIDs = append(matchEntityIDs, s.EntityID)
		matchStates = append(matchStates, s)
	}
	if include.Any() && len(matches) > 0 {
		bundle, err := fetchHAEntityMetadataBundleForEntityIDs(ctx, r.ha, include, matchEntityIDs)
		if err != nil {
			return "", err
		}
		for i := range matches {
			matches[i].Metadata = bundle.metadata(matches[i].EntityID, &matchStates[i])
		}
	}

	result := haListEntitiesResult{
		Domain:         domain,
		Pattern:        pattern,
		Count:          len(matches),
		Total:          total,
		Truncated:      total > len(matches),
		HiddenExcluded: hiddenExcluded,
		Items:          matches,
	}
	return toIndentedJSONWithTruncationNote(result, haListEntitiesTruncationNote), nil
}

func (r *Registry) handleCallService(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	domain, _ := args["domain"].(string)
	service, _ := args["service"].(string)
	entityID, _ := args["entity_id"].(string)

	// A present-but-malformed target must say so, not fall through to
	// the generic "provide entity_id or target" error.
	var targetRaw map[string]any
	hasTarget := false
	if rawTarget, present := args["target"]; present {
		obj, ok := rawTarget.(map[string]any)
		if !ok {
			return "", fmt.Errorf("target must be an object like {\"area_id\": \"office\"}, got %T", rawTarget)
		}
		targetRaw, hasTarget = obj, true
	}

	if domain == "" || service == "" {
		return "", fmt.Errorf("domain and service are required")
	}
	if entityID == "" && !hasTarget {
		return "", fmt.Errorf("provide entity_id (one verified entity) or target (fan out by area/floor/label/device)")
	}
	if entityID != "" && hasTarget {
		return "", fmt.Errorf("provide entity_id or target, not both; put the entity in target.entity_id to combine it with other selectors")
	}

	data := map[string]any{}

	// Merge service data FIRST, and refuse addressing keys inside it:
	// data.entity_id would silently override the verified/resolved
	// addressing below, reintroducing the phantom-success no-op and
	// making the reported result disagree with what went to HA.
	if extra, ok := args["data"].(map[string]any); ok {
		for k, v := range extra {
			if slicesContains(haTargetKeys, k) {
				return "", fmt.Errorf("data.%s is addressing, not service data — use entity_id or target for addressing", k)
			}
			data[k] = v
		}
	}

	var resolvedTarget map[string]any

	if entityID != "" {
		// HA accepts a service call for an unknown entity_id and silently
		// no-ops, so a typo'd or stale id otherwise vanishes without feedback.
		// Probe the entity first (a 404 here is authoritative) and return a
		// recoverable "did you mean?" suggestion instead of a phantom success.
		if _, err := r.ha.GetState(ctx, entityID); err != nil {
			if IsHAEntityNotFound(err) {
				return SuggestEntityNotFound(ctx, r.ha, entityID), nil
			}
			return "", fmt.Errorf("verify entity_id %q before calling %s.%s: %w", entityID, domain, service, err)
		}
		data["entity_id"] = entityID
	} else {
		resolution, err := r.resolveServiceTarget(ctx, targetRaw)
		if err != nil {
			return "", err
		}
		if resolution.Suggestion != "" {
			return resolution.Suggestion, nil
		}
		resolvedTarget = resolution.Resolved
		for k, v := range resolvedTarget {
			data[k] = v
		}
	}

	changed, err := r.ha.CallServiceWithResponse(ctx, domain, service, data)
	if err != nil {
		return "", err
	}

	return haCallServiceResponse(domain, service, entityID, resolvedTarget, changed), nil
}

func (r *Registry) handleControlDevice(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	description, _ := args["description"].(string)
	area, _ := args["area"].(string)
	action, _ := args["action"].(string)

	if description == "" || action == "" {
		return "", fmt.Errorf("description and action are required")
	}

	// Infer domain from action
	domain := ""
	switch action {
	case "turn_on", "turn_off", "toggle", "set_brightness", "set_color":
		domain = "light" // Default to light for these actions
	}

	// Also check description for domain hints
	descLower := strings.ToLower(description)
	if strings.Contains(descLower, "fan") {
		domain = "fan"
	} else if strings.Contains(descLower, "switch") || strings.Contains(descLower, "outlet") {
		domain = "switch"
	} else if strings.Contains(descLower, "lock") {
		domain = "lock"
	}

	// Fetch the full entity set once. GetEntities pulls the entire state
	// machine regardless of any domain filter (it filters in-process), so
	// fetching all and deriving the domain slice locally keeps this to a
	// single bulk GetStates — the inferred-domain match and the broaden-on-
	// miss suggestion both read from the same payload.
	allEntities, err := r.ha.GetEntities(ctx, "")
	if err != nil {
		return "", fmt.Errorf("failed to get entities: %w", err)
	}
	entities := allEntities
	if domain != "" {
		entities = entities[:0:0]
		for _, e := range allEntities {
			if e.Domain == domain {
				entities = append(entities, e)
			}
		}
	}

	// Build search string
	searchStr := description
	if area != "" {
		searchStr = area + " " + description
	}

	// Use the fuzzy matching from ha_find_entity
	matches := fuzzyMatchEntityInfos(searchStr, entities)
	if len(matches) == 0 {
		// The inferred domain may be wrong, so broaden to all domains to
		// suggest candidates — but do not act on a low-confidence
		// cross-domain guess. Return them for the model to confirm. Reuse
		// the already-fetched full set rather than fetching again.
		var candidates []EntitySuggestion
		for i, m := range fuzzyMatchEntityInfos(searchStr, allEntities) {
			if i >= maxEntitySuggestions {
				break
			}
			candidates = append(candidates, EntitySuggestion{
				EntityID:     m.EntityID,
				FriendlyName: m.FriendlyName,
				Score:        m.Score,
			})
		}
		note := "No device matched and nothing was changed. Confirm one of the candidates, or use ha_find_entity to locate the entity_id, then retry."
		if len(candidates) == 0 {
			note = "No device matched and nothing was changed. Use ha_find_entity or ha_list_entities to discover the entity_id; nothing similar was found."
		}
		return toJSON(ControlDeviceNoMatchResult{
			Acted:                false,
			Reason:               "no_match",
			RequestedDescription: description,
			Candidates:           candidates,
			Note:                 note,
		}), nil
	}

	best := matches[0]
	entityID := best.EntityID
	foundName := best.FriendlyName
	if foundName == "" {
		foundName = entityID
	}

	// Build service call
	service := action
	if action == "set_brightness" || action == "set_color" {
		service = "turn_on" // These are turn_on with extra data
	}

	data := map[string]any{
		"entity_id": entityID,
	}

	// Add brightness/color data (works with turn_on too)
	if brightness, ok := args["brightness"].(float64); ok {
		data["brightness_pct"] = int(brightness)
	}
	if color, ok := args["color"].(string); ok && color != "" {
		data["color_name"] = color
	}

	// Extract domain from entity_id
	parts := strings.SplitN(entityID, ".", 2)
	if len(parts) == 2 {
		domain = parts[0]
	}

	// Execute the service call
	if err := r.ha.CallService(ctx, domain, service, data); err != nil {
		return "", fmt.Errorf("failed to control %s: %w", foundName, err)
	}

	// Build friendly response
	actionPast := map[string]string{
		"turn_on":        "turned on",
		"turn_off":       "turned off",
		"toggle":         "toggled",
		"set_brightness": "adjusted brightness of",
		"set_color":      "changed color of",
	}
	verb := actionPast[action]
	if verb == "" {
		verb = action
	}

	// Capitalize first letter of verb
	if len(verb) > 0 {
		verb = strings.ToUpper(verb[:1]) + verb[1:]
	}

	result := fmt.Sprintf("Done. %s %s.\n", verb, foundName)

	// Auto-verify: fetch post-action state so the caller can confirm
	// the action took effect without a second tool call. Use a
	// context-aware delay so cancellation is respected promptly.
	timer := time.NewTimer(500 * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
		return result, nil
	case <-timer.C:
	}

	state, err := r.ha.GetState(ctx, entityID)
	if err != nil {
		// State fetch is best-effort; the action itself succeeded.
		result += fmt.Sprintf("\n(Could not verify state: %v)", err)
		return result, nil
	}
	result += "\nPost-action state:\n" + FormatEntityState(state)

	return result, nil
}

func (r *Registry) handleScheduleTask(ctx context.Context, args map[string]any) (string, error) {
	if r.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	name, _ := args["name"].(string)
	when, _ := args["when"].(string)
	action, _ := args["action"].(string)
	repeat, _ := args["repeat"].(string)

	if name == "" || when == "" || action == "" {
		return "", fmt.Errorf("name, when, and action are required")
	}

	// Parse the "when" parameter
	schedule, err := parseWhen(when, repeat)
	if err != nil {
		return "", fmt.Errorf("invalid schedule: %w", err)
	}

	task := &scheduler.Task{
		Name:     name,
		Schedule: schedule,
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": action},
		},
		Enabled:   true,
		CreatedBy: "agent",
	}

	if err := r.scheduler.CreateTask(task); err != nil {
		return "", err
	}

	now := time.Now()
	nextRun, hasNext := task.NextRun(now)
	next := "(none)"
	if hasNext {
		next = promptfmt.FormatDelta(nextRun, now)
	}
	return fmt.Sprintf("Task '%s' scheduled (ID: %s). Next run: %s", name, task.ID, next), nil
}

func (r *Registry) handleListTasks(ctx context.Context, args map[string]any) (string, error) {
	if r.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	enabledOnly := true
	if e, ok := args["enabled_only"].(bool); ok {
		enabledOnly = e
	}

	tasks, err := r.scheduler.ListTasks(enabledOnly)
	if err != nil {
		return "", err
	}

	if len(tasks) == 0 {
		return "No scheduled tasks.", nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Found %d task(s):\n", len(tasks)))

	now := time.Now()
	for _, t := range tasks {
		next, hasNext := t.NextRun(now)
		status := "enabled"
		if !t.Enabled {
			status = "disabled"
		}

		result.WriteString(fmt.Sprintf("- %s (%s): %s", t.Name, promptfmt.ShortIDPrefix(t.ID), status))
		if hasNext {
			result.WriteString(fmt.Sprintf(", next: %s", promptfmt.FormatDelta(next, now)))
		}
		result.WriteString("\n")
	}

	return result.String(), nil
}

func (r *Registry) handleCancelTask(ctx context.Context, args map[string]any) (string, error) {
	if r.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id is required")
	}

	// Try to find task by full ID or prefix
	tasks, err := r.scheduler.ListTasks(false)
	if err != nil {
		return "", fmt.Errorf("failed to list tasks: %w", err)
	}
	var found *scheduler.Task
	for _, t := range tasks {
		if t.ID == taskID || strings.HasPrefix(t.ID, taskID) {
			found = t
			break
		}
	}

	if found == nil {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	if err := r.scheduler.DeleteTask(found.ID); err != nil {
		return "", err
	}

	return fmt.Sprintf("Task '%s' cancelled.", found.Name), nil
}

// parseWhen converts a human-friendly time specification to a Schedule.
func parseWhen(when, repeat string) (scheduler.Schedule, error) {
	now := time.Now()

	// Try parsing as duration first (e.g., "30m", "2h")
	if dur, err := time.ParseDuration(when); err == nil {
		if repeat != "" {
			// Repeating interval
			repeatDur, err := parseDuration(repeat)
			if err != nil {
				return scheduler.Schedule{}, fmt.Errorf("invalid repeat: %w", err)
			}
			return scheduler.Schedule{
				Kind:  scheduler.ScheduleEvery,
				Every: &scheduler.Duration{Duration: repeatDur},
			}, nil
		}
		// One-shot after duration
		at := now.Add(dur)
		return scheduler.Schedule{
			Kind: scheduler.ScheduleAt,
			At:   &at,
		}, nil
	}

	// Try parsing "in X minutes/hours" format
	if strings.HasPrefix(strings.ToLower(when), "in ") {
		durStr := strings.TrimPrefix(strings.ToLower(when), "in ")
		dur, err := parseHumanDuration(durStr)
		if err == nil {
			at := now.Add(dur)
			return scheduler.Schedule{
				Kind: scheduler.ScheduleAt,
				At:   &at,
			}, nil
		}
	}

	// Try parsing as RFC3339 timestamp
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		return scheduler.Schedule{
			Kind: scheduler.ScheduleAt,
			At:   &t,
		}, nil
	}

	// Try common date formats
	formats := []string{
		"2006-01-02 15:04",
		"2006-01-02T15:04",
		"15:04",
		"3:04pm",
		"3:04 pm",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, when); err == nil {
			// For time-only formats, use today's date
			if format == "15:04" || format == "3:04pm" || format == "3:04 pm" {
				t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
				// If time has passed today, schedule for tomorrow
				if t.Before(now) {
					t = t.Add(24 * time.Hour)
				}
			}
			return scheduler.Schedule{
				Kind: scheduler.ScheduleAt,
				At:   &t,
			}, nil
		}
	}

	return scheduler.Schedule{}, fmt.Errorf("could not parse time: %s", when)
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))

	// Handle "daily", "hourly" etc
	switch s {
	case "daily":
		return 24 * time.Hour, nil
	case "hourly":
		return time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	}

	return time.ParseDuration(s)
}

func parseHumanDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)

	if len(parts) < 2 {
		return 0, fmt.Errorf("expected '<number> <unit>'")
	}

	var num int
	_, err := fmt.Sscanf(parts[0], "%d", &num)
	if err != nil {
		return 0, err
	}

	unit := strings.ToLower(parts[1])
	switch {
	case strings.HasPrefix(unit, "second"):
		return time.Duration(num) * time.Second, nil
	case strings.HasPrefix(unit, "minute"):
		return time.Duration(num) * time.Minute, nil
	case strings.HasPrefix(unit, "hour"):
		return time.Duration(num) * time.Hour, nil
	case strings.HasPrefix(unit, "day"):
		return time.Duration(num) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}
}
