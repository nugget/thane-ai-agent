package memory

import (
	"os"
	"testing"
	"time"
)

func TestToolCallRecording(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create a conversation first
	conv, err := store.GetOrCreateConversation("test-conv")
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	if conv.ID != "test-conv" {
		t.Errorf("expected conversation ID 'test-conv', got %q", conv.ID)
	}

	// Record a tool call with empty message_id (should work - nullable)
	err = store.RecordToolCall("test-conv", "", "call-001", "get_state", `{"entity_id":"light.test"}`)
	if err != nil {
		t.Fatalf("RecordToolCall failed: %v", err)
	}

	// Record a tool call with message_id
	err = store.AddMessage("test-conv", "user", "test message")
	if err != nil {
		t.Fatalf("AddMessage failed: %v", err)
	}
	err = store.RecordToolCall("test-conv", "", "call-002", "call_service", `{"domain":"light"}`)
	if err != nil {
		t.Fatalf("RecordToolCall with message_id failed: %v", err)
	}

	// Complete the first tool call
	err = store.CompleteToolCall("call-001", "on", "")
	if err != nil {
		t.Fatalf("CompleteToolCall failed: %v", err)
	}

	// Complete with error
	err = store.CompleteToolCall("call-002", "", "service not found")
	if err != nil {
		t.Fatalf("CompleteToolCall with error failed: %v", err)
	}

	// Retrieve tool calls
	calls := store.GetToolCalls("test-conv", 10)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}

	// Verify first call (most recent first due to ORDER BY DESC)
	if calls[0].ToolName != "call_service" {
		t.Errorf("expected tool name 'call_service', got %q", calls[0].ToolName)
	}
	if calls[0].Error != "service not found" {
		t.Errorf("expected error 'service not found', got %q", calls[0].Error)
	}

	// Verify second call
	if calls[1].ToolName != "get_state" {
		t.Errorf("expected tool name 'get_state', got %q", calls[1].ToolName)
	}
	if calls[1].Result != "on" {
		t.Errorf("expected result 'on', got %q", calls[1].Result)
	}
	// Duration may be 0 if completion is instant - just verify it's not negative
	if calls[1].DurationMs < 0 {
		t.Errorf("expected non-negative duration, got %d", calls[1].DurationMs)
	}
}

func TestToolCallsByName(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	store.GetOrCreateConversation("test-conv")

	// Record multiple tool calls
	store.RecordToolCall("test-conv", "", "call-1", "get_state", "{}")
	store.RecordToolCall("test-conv", "", "call-2", "get_state", "{}")
	store.RecordToolCall("test-conv", "", "call-3", "call_service", "{}")
	store.RecordToolCall("test-conv", "", "call-4", "get_state", "{}")

	// Filter by name
	getCalls := store.GetToolCallsByName("get_state", 10)
	if len(getCalls) != 3 {
		t.Errorf("expected 3 get_state calls, got %d", len(getCalls))
	}

	serviceCalls := store.GetToolCallsByName("call_service", 10)
	if len(serviceCalls) != 1 {
		t.Errorf("expected 1 call_service call, got %d", len(serviceCalls))
	}
}

func TestToolCallsAllRecent(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	store.GetOrCreateConversation("conv-1")
	store.GetOrCreateConversation("conv-2")

	// Record in different conversations
	store.RecordToolCall("conv-1", "", "call-1", "tool_a", "{}")
	store.RecordToolCall("conv-2", "", "call-2", "tool_b", "{}")
	store.RecordToolCall("conv-1", "", "call-3", "tool_c", "{}")

	// Get all (empty conversation filter)
	all := store.GetToolCalls("", 10)
	if len(all) != 3 {
		t.Errorf("expected 3 total calls, got %d", len(all))
	}

	// Get from specific conversation
	conv1 := store.GetToolCalls("conv-1", 10)
	if len(conv1) != 2 {
		t.Errorf("expected 2 calls in conv-1, got %d", len(conv1))
	}
}

func TestToolCallStats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	store.GetOrCreateConversation("test")

	// Record and complete some calls
	store.RecordToolCall("test", "", "c1", "get_state", "{}")
	store.RecordToolCall("test", "", "c2", "get_state", "{}")
	store.RecordToolCall("test", "", "c3", "call_service", "{}")

	time.Sleep(10 * time.Millisecond) // Ensure measurable duration

	store.CompleteToolCall("c1", "ok", "")
	store.CompleteToolCall("c2", "ok", "")
	store.CompleteToolCall("c3", "", "error!")

	stats := store.ToolCallStats()

	total, ok := stats["total_calls"].(int)
	if !ok || total != 3 {
		t.Errorf("expected total_calls=3, got %v", stats["total_calls"])
	}

	byTool, ok := stats["by_tool"].(map[string]int)
	if !ok {
		t.Fatalf("by_tool not a map")
	}
	if byTool["get_state"] != 2 {
		t.Errorf("expected get_state=2, got %d", byTool["get_state"])
	}
	if byTool["call_service"] != 1 {
		t.Errorf("expected call_service=1, got %d", byTool["call_service"])
	}

	errorRate, ok := stats["error_rate"].(float64)
	if !ok {
		t.Fatalf("error_rate not a float64")
	}
	// 1 error out of 3 = 0.333...
	if errorRate < 0.3 || errorRate > 0.4 {
		t.Errorf("expected error_rate ~0.33, got %f", errorRate)
	}
}

func TestToolCallLimitCap(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	store.GetOrCreateConversation("test")
	store.RecordToolCall("test", "", "c1", "test", "{}")

	// Request huge limit - should be capped internally
	calls := store.GetToolCalls("test", 999999)
	if len(calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(calls))
	}
	// Can't directly test the cap, but at least verify it doesn't crash
}
