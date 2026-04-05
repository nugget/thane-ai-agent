package logging

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestLiveRequestStore_WriteAndQueryRequestDetail(t *testing.T) {
	t.Parallel()

	store := NewLiveRequestStore(8, 12)
	store.WriteRequest(context.Background(), RequestContent{
		RequestID:        "r_live",
		SystemPrompt:     "You are a helpful assistant.",
		UserContent:      "search for observability regressions",
		Model:            "test-model",
		AssistantContent: "I found two likely regressions.",
		IterationCount:   2,
		InputTokens:      321,
		OutputTokens:     123,
		ToolsUsed:        map[string]int{"web_search": 1},
		Messages: []llm.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "search for observability regressions"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{
					ID: "tc_live_1",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "web_search",
						Arguments: map[string]any{"query": "observability regressions"},
					},
				},
			}},
			{Role: "tool", ToolCallID: "tc_live_1", Content: "result payload that should be truncated"},
			{Role: "assistant", Content: "I found two likely regressions."},
		},
	})

	detail, err := store.QueryRequestDetail("r_live")
	if err != nil {
		t.Fatalf("QueryRequestDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("QueryRequestDetail returned nil detail")
	}
	if detail.RequestID != "r_live" {
		t.Fatalf("RequestID = %q, want r_live", detail.RequestID)
	}
	if detail.SystemPrompt != "You are a helpful assistant." {
		t.Fatalf("SystemPrompt = %q", detail.SystemPrompt)
	}
	if detail.UserContent != "search for o" {
		t.Fatalf("UserContent = %q, want truncated value", detail.UserContent)
	}
	if detail.AssistantContent != "I found two " {
		t.Fatalf("AssistantContent = %q, want truncated value", detail.AssistantContent)
	}
	if detail.Model != "test-model" {
		t.Fatalf("Model = %q, want test-model", detail.Model)
	}
	if detail.ToolsUsed["web_search"] != 1 {
		t.Fatalf("ToolsUsed[web_search] = %d, want 1", detail.ToolsUsed["web_search"])
	}
	if len(detail.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(detail.ToolCalls))
	}
	if detail.ToolCalls[0].ToolName != "web_search" {
		t.Fatalf("ToolName = %q, want web_search", detail.ToolCalls[0].ToolName)
	}
	if detail.ToolCalls[0].Result != "result paylo" {
		t.Fatalf("Result = %q, want truncated tool result", detail.ToolCalls[0].Result)
	}

	// Ensure callers receive a defensive copy.
	detail.ToolsUsed["web_search"] = 99
	detail.ToolCalls[0].ToolName = "mutated"
	again, err := store.QueryRequestDetail("r_live")
	if err != nil {
		t.Fatalf("QueryRequestDetail second call: %v", err)
	}
	if again.ToolsUsed["web_search"] != 1 {
		t.Fatalf("stored ToolsUsed mutated to %d, want 1", again.ToolsUsed["web_search"])
	}
	if again.ToolCalls[0].ToolName != "web_search" {
		t.Fatalf("stored ToolName mutated to %q, want web_search", again.ToolCalls[0].ToolName)
	}
}

func TestLiveRequestStore_EvictsOldest(t *testing.T) {
	t.Parallel()

	store := NewLiveRequestStore(1, 0)
	store.WriteRequest(context.Background(), RequestContent{RequestID: "r_old"})
	store.WriteRequest(context.Background(), RequestContent{RequestID: "r_new"})

	oldDetail, err := store.QueryRequestDetail("r_old")
	if err != nil {
		t.Fatalf("QueryRequestDetail old: %v", err)
	}
	if oldDetail != nil {
		t.Fatalf("old detail = %#v, want nil after eviction", oldDetail)
	}

	newDetail, err := store.QueryRequestDetail("r_new")
	if err != nil {
		t.Fatalf("QueryRequestDetail new: %v", err)
	}
	if newDetail == nil || newDetail.RequestID != "r_new" {
		t.Fatalf("new detail = %#v, want r_new", newDetail)
	}
}

func TestLiveRequestStore_UpdatesExistingRequest(t *testing.T) {
	t.Parallel()

	store := NewLiveRequestStore(4, 0)
	store.WriteRequest(context.Background(), RequestContent{
		RequestID:      "r_update",
		SystemPrompt:   "system",
		UserContent:    "user",
		Model:          "model-a",
		IterationCount: 0,
		Messages: []llm.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "user"},
		},
	})
	store.WriteRequest(context.Background(), RequestContent{
		RequestID:        "r_update",
		SystemPrompt:     "system",
		UserContent:      "user",
		Model:            "model-b",
		AssistantContent: "done",
		IterationCount:   2,
		Messages: []llm.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "user"},
			{Role: "assistant", Content: "done"},
		},
	})

	detail, err := store.QueryRequestDetail("r_update")
	if err != nil {
		t.Fatalf("QueryRequestDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("QueryRequestDetail returned nil detail")
	}
	if detail.Model != "model-b" {
		t.Fatalf("Model = %q, want model-b", detail.Model)
	}
	if detail.AssistantContent != "done" {
		t.Fatalf("AssistantContent = %q, want done", detail.AssistantContent)
	}
	if detail.IterationCount != 2 {
		t.Fatalf("IterationCount = %d, want 2", detail.IterationCount)
	}
}

func TestCombineRequestRecorders_FansOutToAllSinks(t *testing.T) {
	t.Parallel()

	left := NewLiveRequestStore(1, 0)
	right := NewLiveRequestStore(1, 0)
	recorder := CombineRequestRecorders(left.WriteRequest, right.WriteRequest)
	recorder(context.Background(), RequestContent{RequestID: "r_fanout"})

	leftDetail, err := left.QueryRequestDetail("r_fanout")
	if err != nil {
		t.Fatalf("left QueryRequestDetail: %v", err)
	}
	rightDetail, err := right.QueryRequestDetail("r_fanout")
	if err != nil {
		t.Fatalf("right QueryRequestDetail: %v", err)
	}
	if leftDetail == nil || leftDetail.RequestID != "r_fanout" {
		t.Fatalf("left detail = %#v, want r_fanout", leftDetail)
	}
	if rightDetail == nil || rightDetail.RequestID != "r_fanout" {
		t.Fatalf("right detail = %#v, want r_fanout", rightDetail)
	}
}
