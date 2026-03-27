package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestContentWriter_WriteRequest(t *testing.T) {
	db := openTestDB(t)
	w, err := NewContentWriter(db, 4096, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	rc := RequestContent{
		RequestID:        "r_test123",
		SystemPrompt:     "You are a helpful assistant.",
		UserContent:      "Hello",
		Model:            "test-model",
		AssistantContent: "Hi there!",
		IterationCount:   1,
		InputTokens:      100,
		OutputTokens:     50,
		ToolsUsed:        map[string]int{"search": 2},
		Exhausted:        false,
		Messages: []llm.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	}
	w.WriteRequest(context.Background(), rc)

	// Verify system prompt was stored and deduplicated.
	expectedHash := hashPrompt("You are a helpful assistant.")
	var storedContent string
	err = db.QueryRow("SELECT content FROM log_prompts WHERE hash = ?", expectedHash).Scan(&storedContent)
	if err != nil {
		t.Fatalf("failed to read prompt: %v", err)
	}
	if storedContent != "You are a helpful assistant." {
		t.Errorf("prompt content = %q, want %q", storedContent, "You are a helpful assistant.")
	}

	// Verify request content was stored.
	var (
		reqID, promptHash, userContent, assistantContent, model string
		iterCount, inputTok, outputTok                          int
		toolsUsed                                               sql.NullString
	)
	err = db.QueryRow(`SELECT request_id, prompt_hash, user_content, assistant_content,
		model, iteration_count, input_tokens, output_tokens, tools_used
		FROM log_request_content WHERE request_id = ?`, "r_test123").Scan(
		&reqID, &promptHash, &userContent, &assistantContent,
		&model, &iterCount, &inputTok, &outputTok, &toolsUsed,
	)
	if err != nil {
		t.Fatalf("failed to read request content: %v", err)
	}
	if promptHash != expectedHash {
		t.Errorf("prompt_hash = %q, want %q", promptHash, expectedHash)
	}
	if userContent != "Hello" {
		t.Errorf("user_content = %q, want %q", userContent, "Hello")
	}
	if assistantContent != "Hi there!" {
		t.Errorf("assistant_content = %q, want %q", assistantContent, "Hi there!")
	}
	if model != "test-model" {
		t.Errorf("model = %q, want %q", model, "test-model")
	}
	if iterCount != 1 || inputTok != 100 || outputTok != 50 {
		t.Errorf("counts = (%d, %d, %d), want (1, 100, 50)", iterCount, inputTok, outputTok)
	}
	if toolsUsed.Valid {
		var tu map[string]int
		if err := json.Unmarshal([]byte(toolsUsed.String), &tu); err != nil {
			t.Fatal(err)
		}
		if tu["search"] != 2 {
			t.Errorf("tools_used[search] = %d, want 2", tu["search"])
		}
	} else {
		t.Error("tools_used should not be null")
	}

	// Write same prompt again — should not duplicate.
	w.WriteRequest(context.Background(), rc)
	var count int
	db.QueryRow("SELECT COUNT(*) FROM log_prompts WHERE hash = ?", expectedHash).Scan(&count)
	if count != 1 {
		t.Errorf("prompt count = %d, want 1 (dedup failed)", count)
	}
}

func TestContentWriter_ToolCallExtraction(t *testing.T) {
	db := openTestDB(t)
	w, err := NewContentWriter(db, 4096, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	rc := RequestContent{
		RequestID:    "r_tools",
		SystemPrompt: "system",
		UserContent:  "search for cats",
		Model:        "test-model",
		Messages: []llm.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "search for cats"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{
					ID: "tc_1",
					Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{
						Name:      "web_search",
						Arguments: map[string]any{"query": "cats"},
					},
				},
			}},
			{Role: "tool", ToolCallID: "tc_1", Content: "Found 42 results about cats."},
			{Role: "assistant", Content: "I found 42 results about cats."},
		},
		AssistantContent: "I found 42 results about cats.",
		IterationCount:   2,
	}
	w.WriteRequest(context.Background(), rc)

	// Verify tool call was stored.
	var (
		toolName, toolCallID sql.NullString
		args, result         sql.NullString
		iterIdx              int
	)
	err = db.QueryRow(`SELECT tool_name, tool_call_id, arguments, result, iteration_index
		FROM log_tool_content WHERE request_id = ?`, "r_tools").Scan(
		&toolName, &toolCallID, &args, &result, &iterIdx,
	)
	if err != nil {
		t.Fatalf("failed to read tool content: %v", err)
	}
	if toolName.String != "web_search" {
		t.Errorf("tool_name = %q, want %q", toolName.String, "web_search")
	}
	if toolCallID.String != "tc_1" {
		t.Errorf("tool_call_id = %q, want %q", toolCallID.String, "tc_1")
	}
	if result.String != "Found 42 results about cats." {
		t.Errorf("result = %q, want %q", result.String, "Found 42 results about cats.")
	}
}

func TestContentWriter_Truncation(t *testing.T) {
	db := openTestDB(t)
	w, err := NewContentWriter(db, 10, slog.Default()) // maxLen = 10
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	longContent := strings.Repeat("x", 100)
	rc := RequestContent{
		RequestID:        "r_trunc",
		SystemPrompt:     longContent, // system prompts are NOT truncated (stored as-is for dedup)
		UserContent:      longContent,
		Model:            "test-model",
		AssistantContent: longContent,
		IterationCount:   1,
		Messages: []llm.Message{
			{Role: "system", Content: longContent},
			{Role: "user", Content: longContent},
			{Role: "assistant", Content: longContent},
		},
	}
	w.WriteRequest(context.Background(), rc)

	var userContent, assistantContent string
	err = db.QueryRow(`SELECT user_content, assistant_content FROM log_request_content
		WHERE request_id = ?`, "r_trunc").Scan(&userContent, &assistantContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(userContent) != 10 {
		t.Errorf("user_content length = %d, want 10", len(userContent))
	}
	if len(assistantContent) != 10 {
		t.Errorf("assistant_content length = %d, want 10", len(assistantContent))
	}
}

func TestContentWriter_MultiByteRune(t *testing.T) {
	db := openTestDB(t)
	w, err := NewContentWriter(db, 5, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// 5 rune limit with multi-byte characters.
	rc := RequestContent{
		RequestID:        "r_rune",
		SystemPrompt:     "s",
		UserContent:      "こんにちは世界", // 7 runes
		Model:            "m",
		AssistantContent: "ok",
		IterationCount:   1,
		Messages: []llm.Message{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "こんにちは世界"},
			{Role: "assistant", Content: "ok"},
		},
	}
	w.WriteRequest(context.Background(), rc)

	var userContent string
	err = db.QueryRow(`SELECT user_content FROM log_request_content
		WHERE request_id = ?`, "r_rune").Scan(&userContent)
	if err != nil {
		t.Fatal(err)
	}
	if userContent != "こんにちは" {
		t.Errorf("user_content = %q, want %q", userContent, "こんにちは")
	}
}
