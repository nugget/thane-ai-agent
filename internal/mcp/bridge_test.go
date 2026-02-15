package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestToolName(t *testing.T) {
	tests := []struct {
		server string
		tool   string
		want   string
	}{
		{"home-assistant", "get_entities", "mcp_home_assistant_get_entities"},
		{"github", "create_issue", "mcp_github_create_issue"},
		{"My Server", "Do Thing", "mcp_my_server_do_thing"},
		{"test", "UPPERCASE", "mcp_test_uppercase"},
		{"a--b", "c--d", "mcp_a_b_c_d"},
		{"special!@#", "chars$%^", "mcp_special_chars"},
	}

	for _, tt := range tests {
		t.Run(tt.server+"/"+tt.tool, func(t *testing.T) {
			got := ToolName(tt.server, tt.tool)
			if got != tt.want {
				t.Errorf("ToolName(%q, %q) = %q, want %q", tt.server, tt.tool, got, tt.want)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"Hello-World", "hello_world"},
		{"a--b", "a_b"},
		{"_leading_", "leading"},
		{"special!chars", "special_chars"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitize(tt.input)
			if got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBridgeTools_AllTools(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{
				Name:        "get_entities",
				Description: "List all entities",
				InputSchema: map[string]any{"type": "object"},
			},
			{
				Name:        "call_service",
				Description: "Call a Home Assistant service",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"domain":  map[string]any{"type": "string"},
						"service": map[string]any{"type": "string"},
					},
				},
			},
		},
	})

	client := NewClient("ha", mt, nil)
	registry := tools.NewEmptyRegistry()
	logger := slog.Default()

	count, err := BridgeTools(context.Background(), client, "home-assistant", registry, nil, nil, logger)
	if err != nil {
		t.Fatalf("BridgeTools: %v", err)
	}

	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	// Verify tool names are namespaced.
	if registry.Get("mcp_home_assistant_get_entities") == nil {
		t.Error("expected mcp_home_assistant_get_entities in registry")
	}
	if registry.Get("mcp_home_assistant_call_service") == nil {
		t.Error("expected mcp_home_assistant_call_service in registry")
	}

	// Verify schema is passed through.
	tool := registry.Get("mcp_home_assistant_call_service")
	if tool.Parameters == nil {
		t.Fatal("Parameters is nil")
	}
	props, ok := tool.Parameters["properties"]
	if !ok {
		t.Fatal("Parameters missing 'properties'")
	}
	propsMap, ok := props.(map[string]any)
	if !ok {
		t.Fatal("properties is not a map")
	}
	if _, ok := propsMap["domain"]; !ok {
		t.Error("missing 'domain' in parameters properties")
	}
}

func TestBridgeTools_IncludeFilter(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{Name: "get_entities", Description: "List entities", InputSchema: map[string]any{"type": "object"}},
			{Name: "call_service", Description: "Call service", InputSchema: map[string]any{"type": "object"}},
			{Name: "get_history", Description: "Get history", InputSchema: map[string]any{"type": "object"}},
		},
	})

	client := NewClient("ha", mt, nil)
	registry := tools.NewEmptyRegistry()
	logger := slog.Default()

	count, err := BridgeTools(context.Background(), client, "ha", registry,
		[]string{"get_entities", "get_history"}, nil, logger)
	if err != nil {
		t.Fatalf("BridgeTools: %v", err)
	}

	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if registry.Get("mcp_ha_get_entities") == nil {
		t.Error("expected mcp_ha_get_entities")
	}
	if registry.Get("mcp_ha_get_history") == nil {
		t.Error("expected mcp_ha_get_history")
	}
	if registry.Get("mcp_ha_call_service") != nil {
		t.Error("mcp_ha_call_service should have been filtered out")
	}
}

func TestBridgeTools_ExcludeFilter(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{Name: "get_entities", Description: "List entities", InputSchema: map[string]any{"type": "object"}},
			{Name: "call_service", Description: "Call service", InputSchema: map[string]any{"type": "object"}},
			{Name: "get_history", Description: "Get history", InputSchema: map[string]any{"type": "object"}},
		},
	})

	client := NewClient("ha", mt, nil)
	registry := tools.NewEmptyRegistry()
	logger := slog.Default()

	count, err := BridgeTools(context.Background(), client, "ha", registry,
		nil, []string{"call_service"}, logger)
	if err != nil {
		t.Fatalf("BridgeTools: %v", err)
	}

	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if registry.Get("mcp_ha_call_service") != nil {
		t.Error("mcp_ha_call_service should have been excluded")
	}
}

func TestBridgeTools_HandlerProxiesCallTool(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{Name: "get_state", Description: "Get entity state", InputSchema: map[string]any{"type": "object"}},
		},
	})
	mt.addResponse("tools/call", callToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "light.kitchen is off"},
		},
	})

	client := NewClient("ha", mt, nil)
	registry := tools.NewEmptyRegistry()
	logger := slog.Default()

	_, err := BridgeTools(context.Background(), client, "ha", registry, nil, nil, logger)
	if err != nil {
		t.Fatalf("BridgeTools: %v", err)
	}

	tool := registry.Get("mcp_ha_get_state")
	if tool == nil {
		t.Fatal("tool not found")
	}

	// Call the handler and verify it proxies to the MCP client.
	result, err := tool.Handler(context.Background(), map[string]any{
		"entity_id": "light.kitchen",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if result != "light.kitchen is off" {
		t.Errorf("result = %q, want %q", result, "light.kitchen is off")
	}

	// Verify the tools/call request used the original MCP tool name.
	mt.mu.Lock()
	defer mt.mu.Unlock()
	found := false
	for _, req := range mt.sent {
		if req.Method == "tools/call" {
			paramsJSON, _ := json.Marshal(req.Params)
			if string(paramsJSON) == "" {
				continue
			}
			var params map[string]any
			json.Unmarshal(paramsJSON, &params)
			if params["name"] == "get_state" {
				found = true
			}
			break
		}
	}
	if !found {
		t.Error("tools/call request should use original MCP name 'get_state', not namespaced name")
	}
}
