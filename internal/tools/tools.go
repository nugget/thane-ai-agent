// Package tools defines the tools available to the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/anticipation"
	"github.com/nugget/thane-ai-agent/internal/facts"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// Tool represents a callable tool.
type Tool struct {
	Name        string                                                         `json:"name"`
	Description string                                                         `json:"description"`
	Parameters  map[string]any                                                 `json:"parameters"`
	Handler     func(ctx context.Context, args map[string]any) (string, error) `json:"-"`
}

// Registry holds available tools.
type Registry struct {
	tools             map[string]*Tool
	ha                *homeassistant.Client
	scheduler         *scheduler.Scheduler
	factTools         *facts.Tools
	anticipationTools *anticipation.Tools
	fileTools         *FileTools
	shellExec         *ShellExec
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
	return r
}

// SetFactTools adds fact management tools to the registry.
func (r *Registry) SetFactTools(ft *facts.Tools) {
	r.factTools = ft
	r.registerFactTools()
}

// SetAnticipationTools adds anticipation management tools to the registry.
func (r *Registry) SetAnticipationTools(at *anticipation.Tools) {
	r.anticipationTools = at
	r.registerAnticipationTools()
}

// SetFileTools adds file operation tools to the registry.
func (r *Registry) SetFileTools(ft *FileTools) {
	r.fileTools = ft
	r.registerFileTools()
}

// SetShellExec adds shell execution tools to the registry.
func (r *Registry) SetShellExec(se *ShellExec) {
	r.shellExec = se
	r.registerShellExec()
}

func (r *Registry) registerFactTools() {
	if r.factTools == nil {
		return
	}

	r.Register(&Tool{
		Name:        "remember_fact",
		Description: "Store a piece of information for later recall. Use for user preferences, home layout, device mappings, or observed patterns.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"enum":        []string{"user", "home", "device", "routine", "preference"},
					"description": "Category for organizing the fact",
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

func (r *Registry) registerAnticipationTools() {
	if r.anticipationTools == nil {
		return
	}

	r.Register(&Tool{
		Name:        "create_anticipation",
		Description: "Create an anticipation — something you're expecting to happen. When you wake and conditions match, you'll receive context to remember why you care about this moment.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{
					"type":        "string",
					"description": "Short description of what you're anticipating (e.g., 'Dan's flight arriving')",
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Instructions/reasoning to inject when this anticipation matches. What should you do or check when this happens?",
				},
				"after_time": map[string]any{
					"type":        "string",
					"description": "ISO8601 timestamp — anticipation activates after this time (e.g., '2026-02-09T14:30:00Z')",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "Entity to watch (e.g., 'person.dan', 'binary_sensor.front_door')",
				},
				"entity_state": map[string]any{
					"type":        "string",
					"description": "State to match for entity (e.g., 'home', 'on', 'open')",
				},
				"zone": map[string]any{
					"type":        "string",
					"description": "Zone name for presence matching (e.g., 'airport', 'home')",
				},
				"zone_action": map[string]any{
					"type":        "string",
					"enum":        []string{"enter", "leave"},
					"description": "Zone transition type",
				},
				"event_type": map[string]any{
					"type":        "string",
					"description": "Event type to match (e.g., 'presence_change', 'state_change')",
				},
				"expires_in": map[string]any{
					"type":        "string",
					"description": "Duration until expiration (e.g., '2h', '24h', '7d'). Omit for no expiration.",
				},
			},
			"required": []string{"description", "context"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.anticipationTools.Execute("create_anticipation", args)
		},
	})

	r.Register(&Tool{
		Name:        "list_anticipations",
		Description: "List all active (non-resolved, non-expired) anticipations.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.anticipationTools.Execute("list_anticipations", args)
		},
	})

	r.Register(&Tool{
		Name:        "resolve_anticipation",
		Description: "Mark an anticipation as resolved — it happened and was handled.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Anticipation ID to resolve",
				},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.anticipationTools.Execute("resolve_anticipation", args)
		},
	})

	r.Register(&Tool{
		Name:        "cancel_anticipation",
		Description: "Cancel an anticipation — no longer relevant or needed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Anticipation ID to cancel",
				},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.anticipationTools.Execute("cancel_anticipation", args)
		},
	})
}

func (r *Registry) registerFileTools() {
	if r.fileTools == nil || !r.fileTools.Enabled() {
		return
	}

	r.Register(&Tool{
		Name:        "file_read",
		Description: "Read the contents of a file from the workspace. Use for accessing configuration, memory files, documentation, or any text file.",
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
		Name:        "file_write",
		Description: "Write content to a file in the workspace. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
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
		Name:        "file_edit",
		Description: "Edit a file by replacing exact text. The old text must match exactly (including whitespace). Use this for precise, surgical edits.",
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
		Name:        "file_list",
		Description: "List files and directories in a workspace path.",
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
}

// Register adds a tool to the registry.
func (r *Registry) Register(t *Tool) {
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

// Execute runs a tool by name with given arguments.
func (r *Registry) Execute(ctx context.Context, name string, argsJSON string) (string, error) {
	tool := r.tools[name]
	if tool == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	var args map[string]any
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}

	return tool.Handler(ctx, args)
}

// Tool handlers

func (r *Registry) handleGetState(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("Home Assistant not configured")
	}

	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	state, err := r.ha.GetState(context.Background(), entityID)
	if err != nil {
		return "", err
	}

	// Format nicely for the LLM
	result := fmt.Sprintf("Entity: %s\nState: %s\n", state.EntityID, state.State)

	// Add key attributes
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

	return result, nil
}

func (r *Registry) handleListEntities(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("Home Assistant not configured")
	}

	domain, _ := args["domain"].(string)
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	states, err := r.ha.GetStates(context.Background())
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
		return "", fmt.Errorf("Home Assistant not configured")
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

	if err := r.ha.CallService(context.Background(), domain, service, data); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully called %s.%s on %s", domain, service, entityID), nil
}

func (r *Registry) handleControlDevice(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("Home Assistant not configured")
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
	entities, err := r.ha.GetEntities(context.Background(), domain)
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
	if err := r.ha.CallService(context.Background(), domain, service, data); err != nil {
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
	return fmt.Sprintf("Done. %s %s.", verb, foundName), nil
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

	nextRun, _ := task.NextRun(time.Now())
	return fmt.Sprintf("Task '%s' scheduled (ID: %s). Next run: %s", name, task.ID, nextRun.Format(time.RFC3339)), nil
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

	for _, t := range tasks {
		next, hasNext := t.NextRun(time.Now())
		status := "enabled"
		if !t.Enabled {
			status = "disabled"
		}

		result.WriteString(fmt.Sprintf("- %s (%s): %s", t.Name, t.ID[:8], status))
		if hasNext {
			result.WriteString(fmt.Sprintf(", next: %s", next.Format("2006-01-02 15:04")))
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
