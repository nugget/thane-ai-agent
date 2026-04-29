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

func TestCanonicalToolIDPreservesDistinctRawNames(t *testing.T) {
	t.Parallel()

	idWithDash := canonicalToolID("github-tools", "foo-bar")
	idWithUnderscore := canonicalToolID("github-tools", "foo_bar")
	if idWithDash == idWithUnderscore {
		t.Fatalf("canonicalToolID collision: %q", idWithDash)
	}
	if want := "mcp:github-tools/foo-bar"; idWithDash != want {
		t.Fatalf("canonicalToolID with dash = %q, want %q", idWithDash, want)
	}
	if want := "mcp:github-tools/foo_bar"; idWithUnderscore != want {
		t.Fatalf("canonicalToolID with underscore = %q, want %q", idWithUnderscore, want)
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

	count, err := BridgeTools(context.Background(), client, "home-assistant", registry, BridgeOptions{}, logger)
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
	if tool.CanonicalID != "mcp:home-assistant/call_service" {
		t.Fatalf("CanonicalID = %q, want mcp:home-assistant/call_service", tool.CanonicalID)
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
		BridgeOptions{Include: []string{"get_entities", "get_history"}}, logger)
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
		BridgeOptions{Exclude: []string{"call_service"}}, logger)
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

	_, err := BridgeTools(context.Background(), client, "ha", registry, BridgeOptions{}, logger)
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

func TestBridgeTools_SanitizesTopLevelCompositionKeywords(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{
				Name:        "notify_state",
				Description: "Notify on state changes",
				InputSchema: map[string]any{
					"type": "object",
					"anyOf": []any{
						map[string]any{"required": []any{"entity_id"}},
						map[string]any{"required": []any{"area_id"}},
					},
					"properties": map[string]any{
						"entity_id": map[string]any{"type": "string"},
						"area_id":   map[string]any{"type": "string"},
					},
				},
			},
		},
	})

	client := NewClient("ha", mt, nil)
	registry := tools.NewEmptyRegistry()

	if _, err := BridgeTools(context.Background(), client, "ha", registry, BridgeOptions{}, slog.Default()); err != nil {
		t.Fatalf("BridgeTools: %v", err)
	}

	tool := registry.Get("mcp_ha_notify_state")
	if tool == nil {
		t.Fatal("tool not found")
	}
	if _, ok := tool.Parameters["anyOf"]; ok {
		t.Fatalf("top-level anyOf should be removed from bridged schema: %#v", tool.Parameters)
	}
	props, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", tool.Parameters["properties"])
	}
	if _, ok := props["entity_id"]; !ok {
		t.Fatalf("entity_id missing from sanitized schema: %#v", props)
	}
	if _, ok := props["area_id"]; !ok {
		t.Fatalf("area_id missing from sanitized schema: %#v", props)
	}
}

func TestBridgeTools_MetadataOverrides(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{Name: "create_pull_request", Description: "Raw MCP description", InputSchema: map[string]any{"type": "object"}},
			{Name: "delete_branch", Description: "Dangerous branch delete", InputSchema: map[string]any{"type": "object"}},
		},
	})

	client := NewClient("github", mt, nil)
	registry := tools.NewEmptyRegistry()
	logger := slog.Default()
	enabled := false

	count, err := BridgeTools(context.Background(), client, "github", registry, BridgeOptions{
		Tags: []string{"forge"},
		ToolOverrides: map[string]ToolOverride{
			"create_pull_request": {
				Description: "Open a pull request in GitHub",
				Tags:        []string{"forge", "publish"},
			},
			"delete_branch": {
				Enabled: &enabled,
			},
		},
	}, logger)
	if err != nil {
		t.Fatalf("BridgeTools: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	tool := registry.Get("mcp_github_create_pull_request")
	if tool == nil {
		t.Fatal("expected mcp_github_create_pull_request")
	}
	if tool.Description != "Open a pull request in GitHub" {
		t.Fatalf("Description = %q", tool.Description)
	}
	if tool.Source != "mcp" {
		t.Fatalf("Source = %q, want mcp", tool.Source)
	}
	if tool.CanonicalID != "mcp:github/create_pull_request" {
		t.Fatalf("CanonicalID = %q", tool.CanonicalID)
	}
	if len(tool.Tags) != 2 || tool.Tags[0] != "forge" || tool.Tags[1] != "publish" {
		t.Fatalf("Tags = %#v", tool.Tags)
	}
	if registry.Get("mcp_github_delete_branch") != nil {
		t.Fatal("delete_branch should be disabled by override")
	}
}
