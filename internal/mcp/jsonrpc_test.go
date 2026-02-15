package mcp

import (
	"encoding/json"
	"testing"
)

func TestNewRequest(t *testing.T) {
	req := NewRequest(42, "tools/list", map[string]any{"cursor": "abc"})

	if req.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", req.JSONRPC, "2.0")
	}
	if req.ID != 42 {
		t.Errorf("ID = %d, want 42", req.ID)
	}
	if req.Method != "tools/list" {
		t.Errorf("Method = %q, want %q", req.Method, "tools/list")
	}
}

func TestRequestMarshalRoundtrip(t *testing.T) {
	req := NewRequest(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
	})

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.JSONRPC != req.JSONRPC {
		t.Errorf("JSONRPC = %q, want %q", decoded.JSONRPC, req.JSONRPC)
	}
	if decoded.ID != req.ID {
		t.Errorf("ID = %d, want %d", decoded.ID, req.ID)
	}
	if decoded.Method != req.Method {
		t.Errorf("Method = %q, want %q", decoded.Method, req.Method)
	}
}

func TestResponseUnmarshal(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`
	var resp Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.ID != 1 {
		t.Errorf("ID = %d, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("Error = %v, want nil", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("Result is nil, want non-nil")
	}
}

func TestResponseUnmarshalError(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`
	var resp Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Error == nil {
		t.Fatal("Error is nil, want non-nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("Error.Code = %d, want -32601", resp.Error.Code)
	}
	if resp.Error.Message != "Method not found" {
		t.Errorf("Error.Message = %q, want %q", resp.Error.Message, "Method not found")
	}
}

func TestRPCErrorString(t *testing.T) {
	e := &RPCError{Code: -32600, Message: "Invalid Request"}
	got := e.Error()
	want := "jsonrpc error -32600: Invalid Request"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestNewNotification(t *testing.T) {
	notif := NewNotification("notifications/initialized", nil)

	if notif.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", notif.JSONRPC, "2.0")
	}
	if notif.Method != "notifications/initialized" {
		t.Errorf("Method = %q, want %q", notif.Method, "notifications/initialized")
	}
	if notif.Params != nil {
		t.Errorf("Params = %v, want nil", notif.Params)
	}
}

func TestNotificationOmitsNilParams(t *testing.T) {
	notif := NewNotification("test", nil)
	data, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := m["params"]; ok {
		t.Error("params should be omitted when nil")
	}
}

func TestRequestOmitsNilParams(t *testing.T) {
	req := NewRequest(1, "ping", nil)
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if _, ok := m["params"]; ok {
		t.Error("params should be omitted when nil")
	}

	// Verify we can still round-trip.
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != 1 {
		t.Errorf("ID = %d, want 1", decoded.ID)
	}
}
