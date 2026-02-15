package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// mockTransport is a test double for the Transport interface.
type mockTransport struct {
	mu        sync.Mutex
	responses map[string]*Response // method -> canned response
	sent      []Request            // captured requests
	notifs    []Notification       // captured notifications
	closed    bool
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		responses: make(map[string]*Response),
	}
}

func (m *mockTransport) addResponse(method string, result any) {
	data, _ := json.Marshal(result)
	m.responses[method] = &Response{
		JSONRPC: jsonrpcVersion,
		Result:  json.RawMessage(data),
	}
}

func (m *mockTransport) addError(method string, code int, msg string) {
	m.responses[method] = &Response{
		JSONRPC: jsonrpcVersion,
		Error:   &RPCError{Code: code, Message: msg},
	}
}

func (m *mockTransport) Send(_ context.Context, req *Request) (*Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, *req)
	resp, ok := m.responses[req.Method]
	if !ok {
		return nil, fmt.Errorf("unexpected method: %s", req.Method)
	}
	// Copy response and set matching ID.
	out := *resp
	out.ID = req.ID
	return &out, nil
}

func (m *mockTransport) Notify(_ context.Context, notif *Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifs = append(m.notifs, *notif)
	return nil
}

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

func TestClient_Initialize(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo{Name: "test-server", Version: "1.0.0"},
		Capabilities:    serverCapabilities{},
	})

	client := NewClient("test", mt, nil)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Verify the initialize request was sent.
	if len(mt.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(mt.sent))
	}
	if mt.sent[0].Method != "initialize" {
		t.Errorf("method = %q, want %q", mt.sent[0].Method, "initialize")
	}

	// Verify the initialized notification was sent.
	if len(mt.notifs) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(mt.notifs))
	}
	if mt.notifs[0].Method != "notifications/initialized" {
		t.Errorf("notification method = %q, want %q", mt.notifs[0].Method, "notifications/initialized")
	}

	// Verify server info was captured.
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.serverName != "test-server" {
		t.Errorf("serverName = %q, want %q", client.serverName, "test-server")
	}
}

func TestClient_ListTools(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo{Name: "test-server", Version: "1.0.0"},
	})
	mt.addResponse("tools/list", toolsListResult{
		Tools: []ToolDefinition{
			{
				Name:        "get_entities",
				Description: "Get all entities",
				InputSchema: map[string]any{"type": "object"},
			},
			{
				Name:        "call_service",
				Description: "Call a service",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"domain": map[string]any{"type": "string"},
					},
				},
			},
		},
	})

	client := NewClient("test", mt, nil)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "get_entities" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "get_entities")
	}
	if tools[1].Name != "call_service" {
		t.Errorf("tools[1].Name = %q, want %q", tools[1].Name, "call_service")
	}

	// Second call should return cached results without another request.
	tools2, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools (cached): %v", err)
	}
	if len(tools2) != 2 {
		t.Fatalf("cached: got %d tools, want 2", len(tools2))
	}
	// Should have sent only 2 requests total (initialize + first tools/list).
	if len(mt.sent) != 2 {
		t.Errorf("sent %d requests, want 2 (init + one tools/list)", len(mt.sent))
	}
}

func TestClient_CallTool_TextResult(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo{Name: "test-server", Version: "1.0.0"},
	})
	mt.addResponse("tools/call", callToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "light.living_room is on"},
		},
	})

	client := NewClient("test", mt, nil)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	result, err := client.CallTool(context.Background(), "get_state", map[string]any{
		"entity_id": "light.living_room",
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if result != "light.living_room is on" {
		t.Errorf("result = %q, want %q", result, "light.living_room is on")
	}
}

func TestClient_CallTool_MultipleContentBlocks(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo{Name: "test-server", Version: "1.0.0"},
	})
	mt.addResponse("tools/call", callToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "Result line 1"},
			{Type: "image"},
			{Type: "text", Text: "Result line 2"},
		},
	})

	client := NewClient("test", mt, nil)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	result, err := client.CallTool(context.Background(), "mixed_tool", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	want := "Result line 1\n[image]\nResult line 2"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestClient_CallTool_ErrorResult(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo{Name: "test-server", Version: "1.0.0"},
	})
	mt.addResponse("tools/call", callToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "entity not found"},
		},
		IsError: true,
	})

	client := NewClient("test", mt, nil)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err := client.CallTool(context.Background(), "get_state", map[string]any{
		"entity_id": "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "MCP tool get_state returned error: entity not found" {
		t.Errorf("error = %q", got)
	}
}

func TestClient_CallTool_RPCError(t *testing.T) {
	mt := newMockTransport()
	mt.addResponse("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      serverInfo{Name: "test-server", Version: "1.0.0"},
	})
	mt.addError("tools/call", -32601, "Method not found")

	client := NewClient("test", mt, nil)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err := client.CallTool(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestClient_Close(t *testing.T) {
	mt := newMockTransport()
	client := NewClient("test", mt, nil)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !mt.closed {
		t.Error("transport was not closed")
	}
}

func TestClient_Name(t *testing.T) {
	mt := newMockTransport()
	client := NewClient("my-server", mt, nil)
	if got := client.Name(); got != "my-server" {
		t.Errorf("Name() = %q, want %q", got, "my-server")
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ContentBlock
		want   string
	}{
		{
			name:   "single text block",
			blocks: []ContentBlock{{Type: "text", Text: "hello"}},
			want:   "hello",
		},
		{
			name:   "multiple text blocks",
			blocks: []ContentBlock{{Type: "text", Text: "a"}, {Type: "text", Text: "b"}},
			want:   "a\nb",
		},
		{
			name:   "image placeholder",
			blocks: []ContentBlock{{Type: "image"}},
			want:   "[image]",
		},
		{
			name:   "unknown type",
			blocks: []ContentBlock{{Type: "audio"}},
			want:   "[audio]",
		},
		{
			name:   "empty",
			blocks: nil,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.blocks)
			if got != tt.want {
				t.Errorf("extractText() = %q, want %q", got, tt.want)
			}
		})
	}
}
