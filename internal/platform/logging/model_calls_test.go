package logging

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestContentWriterRetainsReplayableModelCalls(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	writer, err := NewContentWriter(db, 4096, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	const systemPrompt = "persona\n\nruntime contract\n\ntool contract"
	messages := []llm.Message{
		{
			Role:    "system",
			Content: systemPrompt,
			Sections: []llm.PromptSection{
				{Name: "PERSONA", Content: "persona"},
				{Name: "RUNTIME CONTRACT", Content: "runtime contract"},
				{Name: "TOOL CALLING CONTRACT", Content: "tool contract"},
			},
		},
		{Role: "user", Content: "turn on the office light"},
	}
	call := NewModelCallContent(0, "reference-model", messages, []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "ha_control_device",
			"parameters": map[string]any{
				"type": "object",
			},
		},
	}})
	response := &llm.ChatResponse{Model: "reference-model", StopReason: "tool_calls", InputTokens: 42, OutputTokens: 7}
	response.Message.Role = "assistant"
	response.Message.ToolCalls = []llm.ToolCall{{
		ID: "call_ref",
		Function: struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}{Name: "ha_control_device", Arguments: map[string]any{"entity_id": "light.office", "action": "turn_on"}},
	}}
	call.Complete(response)

	writer.WriteRequest(context.Background(), RequestContent{
		RequestID:    "r_eval",
		SystemPrompt: systemPrompt,
		Model:        "request-summary-model",
		Messages:     messages,
		ModelCalls:   []ModelCallContent{call},
	})

	var stored string
	if err := db.QueryRow(`SELECT model_calls_json FROM log_request_content WHERE request_id = ?`, "r_eval").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, systemPrompt) {
		t.Fatal("model_calls_json duplicated the content-addressed system prompt")
	}

	detail, err := QueryRequestDetail(db, "r_eval")
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || len(detail.ModelCalls) != 1 {
		t.Fatalf("ModelCalls = %#v, want one call", detail)
	}
	got := detail.ModelCalls[0]
	if got.Messages[0].Content != systemPrompt {
		t.Fatalf("resolved system prompt = %q", got.Messages[0].Content)
	}
	if len(got.PromptSections) != 3 {
		t.Fatalf("PromptSections = %#v", got.PromptSections)
	}
	contract := got.PromptSections[2]
	if got.Messages[0].Content[contract.Start:contract.End] != "tool contract" {
		t.Fatalf("tool contract range = %q", got.Messages[0].Content[contract.Start:contract.End])
	}
	if got.Response.ToolCalls[0].Name != "ha_control_device" {
		t.Fatalf("response tool = %#v", got.Response.ToolCalls)
	}
	if got.Tools[0]["type"] != "function" {
		t.Fatalf("tools = %#v", got.Tools)
	}

	ids, err := QueryRecentModelCallRequestIDs(context.Background(), db, time.Now().Add(-time.Hour), "reference-model", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "r_eval" {
		t.Fatalf("request IDs = %#v, want r_eval", ids)
	}
}

func TestRetainedToolCallMarksTruncatedArguments(t *testing.T) {
	t.Parallel()

	call := llm.ToolCall{ID: "call_1"}
	call.Function.Name = "search"
	call.Function.Arguments = map[string]any{"query": "long search phrase"}
	details := retainedToolCalls([]llm.ToolCall{call}, 8)
	if len(details) != 1 || !details[0].ArgumentsTruncated {
		t.Fatalf("retained tool call = %#v, want arguments_truncated", details)
	}
}

func TestLiveRequestStoreModelCallsAreDefensiveCopies(t *testing.T) {
	t.Parallel()

	store := NewLiveRequestStore(2, 4096)
	call := NewModelCallContent(0, "m", []llm.Message{{Role: "system", Content: "system"}}, []map[string]any{{
		"type":     "function",
		"function": map[string]any{"name": "tool_a"},
	}})
	call.Complete(&llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "done"}})
	store.WriteRequest(context.Background(), RequestContent{RequestID: "r", ModelCalls: []ModelCallContent{call}})

	first, err := store.QueryRequestDetail("r")
	if err != nil {
		t.Fatal(err)
	}
	first.ModelCalls[0].Messages[0].Content = "mutated"
	first.ModelCalls[0].Tools[0]["type"] = "mutated"
	second, err := store.QueryRequestDetail("r")
	if err != nil {
		t.Fatal(err)
	}
	if second.ModelCalls[0].Messages[0].Content != "system" || second.ModelCalls[0].Tools[0]["type"] != "function" {
		t.Fatalf("stored model call was aliased: %#v", second.ModelCalls[0])
	}
}

func TestRetainedModelCallsExcludeIncompleteAndDiscardedCalls(t *testing.T) {
	t.Parallel()

	incomplete := NewModelCallContent(0, "model", []llm.Message{{Role: "user", Content: "hello"}}, nil)
	discarded := NewModelCallContent(1, "model", []llm.Message{{Role: "user", Content: "hello"}}, nil)
	discarded.Complete(&llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "synthetic"}})
	discarded.Discard()
	complete := NewModelCallContent(2, "model", []llm.Message{{Role: "user", Content: "hello"}}, nil)
	complete.UseAttempt("recovery-model", []llm.Message{{Role: "user", Content: "recovery input"}}, nil)
	complete.Complete(&llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "provider response"}})

	details, _ := retainedModelCalls([]ModelCallContent{incomplete, discarded, complete}, 4096, true)
	if len(details) != 1 {
		t.Fatalf("retained calls = %#v, want only completed provider call", details)
	}
	if details[0].Model != "recovery-model" || details[0].Messages[0].Content != "recovery input" {
		t.Fatalf("retained recovery attempt = %#v", details[0])
	}
}
