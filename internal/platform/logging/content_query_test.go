package logging

import (
	"context"
	"log/slog"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestQueryRequestDetail_Found(t *testing.T) {
	db := openTestDB(t)
	w, err := NewContentWriter(db, 4096, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Write a request with tool calls.
	rc := RequestContent{
		RequestID:        "r_query_test",
		SystemPrompt:     "You are a helpful assistant.",
		UserContent:      "Hello",
		Model:            "test-model",
		AssistantContent: "Hi there!",
		IterationCount:   2,
		InputTokens:      100,
		OutputTokens:     50,
		ToolsUsed:        map[string]int{"search": 2},
		Exhausted:        true,
		ExhaustReason:    "max_iterations",
		Messages: []llm.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{
					ID: "tc_q1",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "search",
						Arguments: map[string]any{"query": "hello"},
					},
				},
			}},
			{Role: "tool", ToolCallID: "tc_q1", Content: "Found results."},
			{Role: "assistant", Content: "Hi there!"},
		},
	}
	w.WriteRequest(context.Background(), rc)

	// Query it back.
	detail, err := QueryRequestDetail(db, "r_query_test")
	if err != nil {
		t.Fatalf("QueryRequestDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("expected non-nil detail")
	}

	if detail.RequestID != "r_query_test" {
		t.Errorf("request_id = %q, want r_query_test", detail.RequestID)
	}
	if detail.Model != "test-model" {
		t.Errorf("model = %q, want test-model", detail.Model)
	}
	if detail.UserContent != "Hello" {
		t.Errorf("user_content = %q, want Hello", detail.UserContent)
	}
	if detail.AssistantContent != "Hi there!" {
		t.Errorf("assistant_content = %q, want Hi there!", detail.AssistantContent)
	}
	if detail.IterationCount != 2 {
		t.Errorf("iteration_count = %d, want 2", detail.IterationCount)
	}
	if detail.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", detail.InputTokens)
	}
	if detail.OutputTokens != 50 {
		t.Errorf("output_tokens = %d, want 50", detail.OutputTokens)
	}
	if !detail.Exhausted {
		t.Error("expected exhausted = true")
	}
	if detail.ExhaustReason != "max_iterations" {
		t.Errorf("exhaust_reason = %q, want max_iterations", detail.ExhaustReason)
	}
	if detail.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("system_prompt = %q, want full prompt", detail.SystemPrompt)
	}
	if detail.ToolsUsed["search"] != 2 {
		t.Errorf("tools_used[search] = %d, want 2", detail.ToolsUsed["search"])
	}
	if len(detail.Messages) != 5 {
		t.Fatalf("messages count = %d, want 5", len(detail.Messages))
	}
	if detail.Messages[0].Role != "system" || detail.Messages[0].Content != "You are a helpful assistant." {
		t.Fatalf("messages[0] = %#v, want retained system prompt", detail.Messages[0])
	}
	if detail.Messages[2].Role != "assistant" || len(detail.Messages[2].ToolCalls) != 1 {
		t.Fatalf("messages[2] = %#v, want assistant tool-call message", detail.Messages[2])
	}
	if detail.Messages[2].ToolCalls[0].Name != "search" || detail.Messages[2].ToolCalls[0].Arguments != `{"query":"hello"}` {
		t.Fatalf("messages[2].ToolCalls[0] = %#v, want search query args", detail.Messages[2].ToolCalls[0])
	}

	// Verify tool calls.
	if len(detail.ToolCalls) != 1 {
		t.Fatalf("tool_calls count = %d, want 1", len(detail.ToolCalls))
	}
	tc := detail.ToolCalls[0]
	if tc.ToolName != "search" {
		t.Errorf("tool_name = %q, want search", tc.ToolName)
	}
	if tc.ToolCallID != "tc_q1" {
		t.Errorf("tool_call_id = %q, want tc_q1", tc.ToolCallID)
	}
	if tc.Result != "Found results." {
		t.Errorf("result = %q, want Found results.", tc.Result)
	}
}

func TestQueryRequestDetail_NotFound(t *testing.T) {
	db := openTestDB(t)
	detail, err := QueryRequestDetail(db, "r_nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail != nil {
		t.Errorf("expected nil for missing request, got %+v", detail)
	}
}

func TestQueryRequestDetail_NoToolCalls(t *testing.T) {
	db := openTestDB(t)
	w, err := NewContentWriter(db, 4096, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	rc := RequestContent{
		RequestID:        "r_notools",
		SystemPrompt:     "system",
		UserContent:      "hi",
		Model:            "m",
		AssistantContent: "hello",
		IterationCount:   1,
		Messages: []llm.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	w.WriteRequest(context.Background(), rc)

	detail, err := QueryRequestDetail(db, "r_notools")
	if err != nil {
		t.Fatalf("QueryRequestDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("expected non-nil detail")
	}
	if len(detail.ToolCalls) != 0 {
		t.Errorf("tool_calls count = %d, want 0", len(detail.ToolCalls))
	}
	// Ensure ToolCalls is empty slice, not nil (for clean JSON serialization).
	if detail.ToolCalls == nil {
		t.Error("ToolCalls should be empty slice, not nil")
	}
}
