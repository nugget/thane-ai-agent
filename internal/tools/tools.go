// Package tools defines the tools available to the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// Tool represents a callable tool.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]any         `json:"parameters"`
	Handler     func(ctx context.Context, args map[string]any) (string, error) `json:"-"`
}

// Registry holds available tools.
type Registry struct {
	tools     map[string]*Tool
	ha        *homeassistant.Client
	scheduler *scheduler.Scheduler
}

// NewRegistry creates a tool registry with HA integration.
func NewRegistry(ha *homeassistant.Client, sched *scheduler.Scheduler) *Registry {
	r := &Registry{
		tools:     make(map[string]*Tool),
		ha:        ha,
		scheduler: sched,
	}
	r.registerBuiltins()
	return r
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

	// Call service
	r.Register(&Tool{
		Name:        "call_service",
		Description: "Call a Home Assistant service to control devices. Examples: turn on lights, set thermostat temperature, lock doors.",
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
					"description": "The target entity ID",
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

	state, err := r.ha.GetState(ctx, entityID)
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

	if err := r.ha.CallService(ctx, domain, service, data); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully called %s.%s on %s", domain, service, entityID), nil
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
	tasks, _ := r.scheduler.ListTasks(false)
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
