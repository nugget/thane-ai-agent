package anticipation

import (
	"encoding/json"
	"fmt"
	"time"
)

// Tools provides anticipation management tools for the agent.
type Tools struct {
	store *Store
}

// NewTools creates anticipation tools backed by the given store.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
}

// ToolDefinitions returns the tool schemas for LLM function calling.
func (t *Tools) ToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "create_anticipation",
				"description": "Create an anticipation — something you're expecting to happen. When you wake and conditions match, you'll receive this context to remember why you care about this moment.",
				"parameters": map[string]any{
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
						"context_entities": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Entity IDs to fetch and include as context when this anticipation fires (e.g., ['sensor.temperature', 'light.living_room']). Max 10.",
						},
						"recurring": map[string]any{
							"type":        "boolean",
							"description": "If true, the anticipation keeps firing on every match (subject to cooldown). If false (default), it is auto-resolved after the first successful wake.",
						},
						"expires_in": map[string]any{
							"type":        "string",
							"description": "Duration until expiration (e.g., '2h', '24h', '7d'). Omit for no expiration.",
						},
					},
					"required": []string{"description", "context"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_anticipations",
				"description": "List all active (non-resolved, non-expired) anticipations.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "resolve_anticipation",
				"description": "Mark an anticipation as resolved — it happened and was handled.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Anticipation ID to resolve",
						},
					},
					"required": []string{"id"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "cancel_anticipation",
				"description": "Cancel an anticipation — no longer relevant or needed.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Anticipation ID to cancel",
						},
					},
					"required": []string{"id"},
				},
			},
		},
	}
}

// Execute runs an anticipation tool and returns the result.
func (t *Tools) Execute(name string, args map[string]any) (string, error) {
	switch name {
	case "create_anticipation":
		return t.createAnticipation(args)
	case "list_anticipations":
		return t.listAnticipations()
	case "resolve_anticipation":
		return t.resolveAnticipation(args)
	case "cancel_anticipation":
		return t.cancelAnticipation(args)
	default:
		return "", fmt.Errorf("unknown anticipation tool: %s", name)
	}
}

func (t *Tools) createAnticipation(args map[string]any) (string, error) {
	desc, _ := args["description"].(string)
	ctx, _ := args["context"].(string)

	if desc == "" || ctx == "" {
		return "", fmt.Errorf("description and context are required")
	}

	a := &Anticipation{
		Description: desc,
		Context:     ctx,
	}

	// Parse trigger conditions
	if v, ok := args["after_time"].(string); ok && v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return "", fmt.Errorf("invalid after_time format: %w", err)
		}
		a.Trigger.AfterTime = &t
	}

	if v, ok := args["entity_id"].(string); ok {
		a.Trigger.EntityID = v
	}
	if v, ok := args["entity_state"].(string); ok {
		a.Trigger.EntityState = v
	}
	if v, ok := args["zone"].(string); ok {
		a.Trigger.Zone = v
	}
	if v, ok := args["zone_action"].(string); ok {
		a.Trigger.ZoneAction = v
	}
	if v, ok := args["event_type"].(string); ok {
		a.Trigger.EventType = v
	}

	if v, ok := args["recurring"].(bool); ok {
		a.Recurring = v
	}

	// Parse context entities (capped at 10).
	if v, ok := args["context_entities"].([]any); ok {
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				a.ContextEntities = append(a.ContextEntities, s)
			}
		}
		const maxContextEntities = 10
		if len(a.ContextEntities) > maxContextEntities {
			a.ContextEntities = a.ContextEntities[:maxContextEntities]
		}
	}

	// Parse expiration
	if v, ok := args["expires_in"].(string); ok && v != "" {
		dur, err := parseDuration(v)
		if err != nil {
			return "", fmt.Errorf("invalid expires_in: %w", err)
		}
		exp := time.Now().Add(dur)
		a.ExpiresAt = &exp
	}

	// Validate: must have at least one trigger
	if a.Trigger.AfterTime == nil && a.Trigger.EntityID == "" &&
		a.Trigger.Zone == "" && a.Trigger.EventType == "" {
		return "", fmt.Errorf("anticipation needs at least one trigger condition (after_time, entity_id, zone, or event_type)")
	}

	if err := t.store.Create(a); err != nil {
		return "", err
	}

	result := fmt.Sprintf("Created anticipation: %s\nID: %s\n", a.Description, a.ID)
	if a.Recurring {
		result += "Lifecycle: recurring (keeps firing on matches)\n"
	} else {
		result += "Lifecycle: one-shot (auto-resolved after first wake)\n"
	}
	if a.ExpiresAt != nil {
		result += fmt.Sprintf("Expires: %s\n", a.ExpiresAt.Format(time.RFC3339))
	}
	result += "\nTrigger conditions:\n"
	if a.Trigger.AfterTime != nil {
		result += fmt.Sprintf("  - After: %s\n", a.Trigger.AfterTime.Format(time.RFC3339))
	}
	if a.Trigger.EntityID != "" {
		result += fmt.Sprintf("  - Entity: %s", a.Trigger.EntityID)
		if a.Trigger.EntityState != "" {
			result += fmt.Sprintf(" = %s", a.Trigger.EntityState)
		}
		result += "\n"
	}
	if a.Trigger.Zone != "" {
		result += fmt.Sprintf("  - Zone: %s", a.Trigger.Zone)
		if a.Trigger.ZoneAction != "" {
			result += fmt.Sprintf(" (%s)", a.Trigger.ZoneAction)
		}
		result += "\n"
	}
	if a.Trigger.EventType != "" {
		result += fmt.Sprintf("  - Event type: %s\n", a.Trigger.EventType)
	}
	if len(a.ContextEntities) > 0 {
		result += fmt.Sprintf("Context entities: %v\n", a.ContextEntities)
	}

	return result, nil
}

func (t *Tools) listAnticipations() (string, error) {
	active, err := t.store.Active()
	if err != nil {
		return "", err
	}

	if len(active) == 0 {
		return "No active anticipations.", nil
	}

	result := fmt.Sprintf("Active anticipations: %d\n\n", len(active))
	for _, a := range active {
		result += fmt.Sprintf("**%s** (ID: %s)\n", a.Description, a.ID)
		if a.Recurring {
			result += "  Lifecycle: recurring\n"
		}
		result += fmt.Sprintf("  Created: %s\n", a.CreatedAt.Format("2006-01-02 15:04"))
		if a.ExpiresAt != nil {
			result += fmt.Sprintf("  Expires: %s\n", a.ExpiresAt.Format("2006-01-02 15:04"))
		}
		if len(a.ContextEntities) > 0 {
			result += fmt.Sprintf("  Context entities: %v\n", a.ContextEntities)
		}
		result += fmt.Sprintf("  Context: %s\n\n", truncate(a.Context, 100))
	}

	return result, nil
}

func (t *Tools) resolveAnticipation(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	a, err := t.store.Get(id)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", fmt.Errorf("anticipation not found: %s", id)
	}

	if err := t.store.Resolve(id); err != nil {
		return "", err
	}

	return fmt.Sprintf("Resolved anticipation: %s", a.Description), nil
}

func (t *Tools) cancelAnticipation(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	a, err := t.store.Get(id)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", fmt.Errorf("anticipation not found: %s", id)
	}

	if err := t.store.Delete(id); err != nil {
		return "", err
	}

	return fmt.Sprintf("Cancelled anticipation: %s", a.Description), nil
}

// parseDuration handles durations like "2h", "24h", "7d"
func parseDuration(s string) (time.Duration, error) {
	// Handle days specially
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// IsAnticipationTool reports whether the named tool is an anticipation tool.
func IsAnticipationTool(name string) bool {
	switch name {
	case "create_anticipation", "list_anticipations", "resolve_anticipation", "cancel_anticipation":
		return true
	}
	return false
}

// MarshalToolCall converts a tool call to JSON for logging.
func MarshalToolCall(name string, args map[string]any) string {
	b, _ := json.Marshal(map[string]any{"tool": name, "args": args})
	return string(b)
}
