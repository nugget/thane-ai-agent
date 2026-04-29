// Package tools defines the tools available to the agent.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
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
)

// Tool represents a callable tool.
type Tool struct {
	Name                 string                                                         `json:"name"`
	Description          string                                                         `json:"description"`
	Parameters           map[string]any                                                 `json:"parameters"`
	Handler              func(ctx context.Context, args map[string]any) (string, error) `json:"-"`
	AlwaysAvailable      bool                                                           `json:"-"` // Survives capability tag filtering.
	SkipContentResolve   bool                                                           `json:"-"` // Exempt from prefix-to-content resolution.
	ContentResolveExempt []string                                                       `json:"-"` // Top-level arg keys that must remain literal during content resolution.
	CanonicalID          string                                                         `json:"-"`
	Source               string                                                         `json:"-"`
	Origin               string                                                         `json:"-"`
	DefaultTags          []string                                                       `json:"-"`
}

// Registry holds available tools.
type Registry struct {
	tools           map[string]*Tool
	tagIndex        map[string][]string // tag → tool names
	ha              *homeassistant.Client
	scheduler       *scheduler.Scheduler
	factTools       *knowledge.Tools
	contactTools    *contacts.Tools
	emailTools      *email.Tools
	notifier        *notifications.Sender
	notifRecords    *notifications.RecordStore
	notifRouter     *notifications.NotificationRouter
	notifDispatcher CallbackDispatcher
	companionCaller companionCallFunc
	forgeTools      forgeHandler
	fileTools       *FileTools
	shellExec       *ShellExec
	attachmentTools *attachments.Tools
	tempFileStore   *TempFileStore
	usageStore      *usage.Store
	lensStore       *LensStore
	logIndexDB      *sql.DB

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
	persistLoopDefinition                      func(looppkg.Spec, time.Time) error
	deletePersistedLoopDefinition              func(string) error
	persistLoopDefinitionPolicy                func(string, looppkg.DefinitionPolicy) error
	deletePersistedLoopDefinitionPolicy        func(string) error
	reconcileLoopDefinition                    func(context.Context, string) error
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
func NewRegistry(ha *homeassistant.Client, sched *scheduler.Scheduler) *Registry {
	r := &Registry{
		tools:     make(map[string]*Tool),
		ha:        ha,
		scheduler: sched,
	}
	r.registerBuiltins()
	r.registerFindEntity() // Smart entity discovery
	r.registerHAAutomationTools()
	return r
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
		Name:        "remember_fact",
		Description: "Store a discrete, stable piece of information for later recall. Best for user preferences, home layout, device mappings, routines, or observed patterns. Each fact should be a single, self-contained piece of knowledge — not a project spec or design document. Do NOT store complex/evolving knowledge here — use workspace files instead. Do NOT store person-specific attributes — use save_contact instead.",
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
		Name:        "get_state",
		Description: "Get the current state of a Home Assistant entity. Use this to check if lights are on, doors are open, temperatures, etc.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The entity ID (e.g., light.living_room, sensor.temperature, binary_sensor.front_door)",
				},
			},
			"required": []string{"entity_id"},
		},
		Handler: r.handleGetState,
	})

	// List entities by domain
	r.Register(&Tool{
		Name:        "list_entities",
		Description: "List all entities in a domain (e.g., all lights, all sensors). Use this to discover what's available.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain": map[string]any{
					"type":        "string",
					"description": "The domain to list (e.g., light, switch, sensor, binary_sensor, climate, cover)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of entities to return (default 20)",
				},
			},
			"required": []string{"domain"},
		},
		Handler: r.handleListEntities,
	})

	// Control device - combined find + action (preferred tool for voice control)
	r.Register(&Tool{
		Name:        "control_device",
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

	// Call service (low-level, use control_device for voice commands)
	r.Register(&Tool{
		Name:        "call_service",
		Description: "Low-level Home Assistant service call. Only use if you already have the exact entity_id. For voice commands, use control_device instead.",
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
					"description": "The EXACT entity ID (must be verified, not guessed)",
				},
				"data": map[string]any{
					"type":        "object",
					"description": "Additional service data (e.g., brightness, temperature)",
				},
			},
			"required": []string{"domain", "service", "entity_id"},
		},
		Handler: r.handleCallService,
	})

	// Schedule task
	r.Register(&Tool{
		Name:        "schedule_task",
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
		Name:        "list_tasks",
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
		Name:        "cancel_task",
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
		t.DefaultTags = mergeUniqueStrings(spec.DefaultTags, t.DefaultTags)
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

// List returns all tools for the LLM.
func (r *Registry) List() []map[string]any {
	var result []map[string]any
	for _, t := range r.tools {
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

// AllToolNames returns the names of all registered tools.
func (r *Registry) AllToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
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
		cp.AlwaysAvailable = true
		filtered.Register(&cp)
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
		for _, tag := range t.DefaultTags {
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
// AlwaysAvailable. If tags is empty or the tag index is nil, returns a
// copy of the full registry.
func (r *Registry) FilterByTags(tags []string) *Registry {
	if len(tags) == 0 || r.tagIndex == nil {
		// No filtering — return a shallow copy with all tools.
		filtered := &Registry{
			tools:           make(map[string]*Tool, len(r.tools)),
			contentResolver: r.contentResolver,
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
	}
	for name, t := range r.tools {
		if allowed[name] || t.AlwaysAvailable {
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
		return "", err
	}

	return FormatEntityState(state), nil
}

// FormatEntityState formats a Home Assistant entity state for LLM
// consumption. Used by get_state, control_device post-action verification,
// and context injection.
func FormatEntityState(state *homeassistant.State) string {
	result := fmt.Sprintf("Entity: %s\nState: %s\n", state.EntityID, state.State)

	if name, ok := state.Attributes["friendly_name"].(string); ok {
		result += fmt.Sprintf("Name: %s\n", name)
	}
	if unit, ok := state.Attributes["unit_of_measurement"].(string); ok {
		result += fmt.Sprintf("Unit: %s\n", unit)
	}
	if brightness, ok := state.Attributes["brightness"].(float64); ok {
		result += fmt.Sprintf("Brightness: %.0f%%\n", brightness/255*100)
	}
	if temp, ok := state.Attributes["temperature"].(float64); ok {
		result += fmt.Sprintf("Temperature: %.1f\n", temp)
	}

	return result
}

func (r *Registry) handleListEntities(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	domain, _ := args["domain"].(string)
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	states, err := r.ha.GetStates(ctx)
	if err != nil {
		return "", err
	}

	var matches []string
	prefix := domain + "."
	for _, s := range states {
		if strings.HasPrefix(s.EntityID, prefix) {
			name := s.EntityID
			if friendly, ok := s.Attributes["friendly_name"].(string); ok {
				name = fmt.Sprintf("%s (%s)", s.EntityID, friendly)
			}
			matches = append(matches, fmt.Sprintf("- %s: %s", name, s.State))
			if len(matches) >= limit {
				break
			}
		}
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No entities found in domain '%s'", domain), nil
	}

	return fmt.Sprintf("Found %d %s entities:\n%s", len(matches), domain, strings.Join(matches, "\n")), nil
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

	if domain == "" || service == "" || entityID == "" {
		return "", fmt.Errorf("domain, service, and entity_id are required")
	}

	data := map[string]any{
		"entity_id": entityID,
	}

	// Merge additional data
	if extra, ok := args["data"].(map[string]any); ok {
		for k, v := range extra {
			data[k] = v
		}
	}

	if err := r.ha.CallService(ctx, domain, service, data); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully called %s.%s on %s", domain, service, entityID), nil
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

	// Find the entity
	entities, err := r.ha.GetEntities(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("failed to get entities: %w", err)
	}

	// Build search string
	searchStr := description
	if area != "" {
		searchStr = area + " " + description
	}

	// Use the fuzzy matching from find_entity
	matches := fuzzyMatchEntityInfos(searchStr, entities)
	if len(matches) == 0 {
		return fmt.Sprintf("Could not find a device matching '%s'", description), nil
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
