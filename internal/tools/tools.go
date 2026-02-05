// Package tools defines the tools available to the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
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
	tools map[string]*Tool
	ha    *homeassistant.Client
}

// NewRegistry creates a tool registry with HA integration.
func NewRegistry(ha *homeassistant.Client) *Registry {
	r := &Registry{
		tools: make(map[string]*Tool),
		ha:    ha,
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
