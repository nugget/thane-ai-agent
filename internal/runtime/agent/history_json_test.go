package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestFormatHistoryJSON_Basic(t *testing.T) {
	now := time.Date(2026, 2, 21, 15, 0, 0, 0, time.UTC)
	messages := []memory.Message{
		{Role: "user", Content: "What is the weather?", Timestamp: now.Add(-time.Hour)},
		{Role: "assistant", Content: "Let me check.", Timestamp: now.Add(-55 * time.Minute)},
	}

	result := formatHistoryJSON(messages, now)

	// Must be valid JSON.
	var entries []historyEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, result)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].Role != "user" {
		t.Errorf("entries[0].Role = %q, want %q", entries[0].Role, "user")
	}
	if entries[0].Text != "What is the weather?" {
		t.Errorf("entries[0].Text = %q, want %q", entries[0].Text, "What is the weather?")
	}
	if entries[0].AgeDelta != "-3600s" {
		t.Errorf("entries[0].AgeDelta = %q, want %q", entries[0].AgeDelta, "-3600s")
	}

	if entries[1].Role != "assistant" {
		t.Errorf("entries[1].Role = %q, want %q", entries[1].Role, "assistant")
	}
	if entries[1].AgeDelta != "-3300s" {
		t.Errorf("entries[1].AgeDelta = %q, want %q", entries[1].AgeDelta, "-3300s")
	}
}

func TestFormatHistoryJSON_EmptyMessages(t *testing.T) {
	result := formatHistoryJSON(nil, time.Now())

	if result != "[]" {
		t.Errorf("expected empty JSON array, got: %s", result)
	}

	// Must still be valid JSON.
	var entries []historyEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("invalid JSON for empty input: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestFormatHistoryJSON_CompactionSummary(t *testing.T) {
	now := time.Date(2026, 2, 21, 14, 0, 0, 0, time.UTC)
	messages := []memory.Message{
		{
			Role:      "system",
			Content:   "[Conversation Summary] The user asked about email polling.",
			Timestamp: now.Add(-4 * time.Hour),
		},
		{
			Role:      "user",
			Content:   "Any new emails?",
			Timestamp: now,
		},
	}

	result := formatHistoryJSON(messages, now)

	var entries []historyEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if entries[0].Role != "system" {
		t.Errorf("compaction summary should have role %q, got %q", "system", entries[0].Role)
	}
	if !strings.HasPrefix(entries[0].Text, "[Conversation Summary]") {
		t.Errorf("compaction summary text should start with [Conversation Summary], got: %s", entries[0].Text)
	}
}

func TestFormatHistoryJSON_AgeDeltaIsTimezoneIndependent(t *testing.T) {
	// Two equivalent moments in different zones should yield the same
	// delta — the field is a duration, not a wall-clock string.
	utc := time.Date(2026, 2, 21, 20, 0, 0, 0, time.UTC)
	cstZone := time.FixedZone("CST", -6*60*60)
	cst := utc.In(cstZone)

	messages := []memory.Message{
		{Role: "user", Content: "hello", Timestamp: utc.Add(-time.Hour)},
	}
	resultUTC := formatHistoryJSON(messages, utc)
	resultCST := formatHistoryJSON(messages, cst)

	if resultUTC != resultCST {
		t.Fatalf("delta should not depend on the now-zone:\nUTC: %s\nCST: %s", resultUTC, resultCST)
	}
}

func TestFormatHistoryJSON_FuturePinnedReturnsPositiveDelta(t *testing.T) {
	// Defensive: if a message timestamp ever ends up in the future
	// relative to now (clock skew, test fixture), the delta should
	// flip sign rather than panic or hide the anomaly.
	now := time.Date(2026, 2, 21, 15, 0, 0, 0, time.UTC)
	messages := []memory.Message{
		{Role: "user", Content: "hello", Timestamp: now.Add(60 * time.Second)},
	}

	result := formatHistoryJSON(messages, now)

	var entries []historyEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entries[0].AgeDelta != "+60s" {
		t.Errorf("future-timestamp AgeDelta = %q, want %q", entries[0].AgeDelta, "+60s")
	}
}

func TestBuildSystemPrompt_ConversationHistoryJSON(t *testing.T) {
	l := newMinimalLoop()

	history := []memory.Message{
		{Role: "user", Content: "Turn on the lights", Timestamp: time.Now().Add(-time.Hour)},
		{Role: "assistant", Content: "Done, lights are on.", Timestamp: time.Now().Add(-59 * time.Minute)},
	}

	prompt := l.buildSystemPrompt(context.Background(), "what is the temperature?", history)

	if !strings.Contains(prompt, "## Conversation History") {
		t.Error("system prompt should contain conversation history section heading")
	}
	if !strings.Contains(prompt, "Turn on the lights") {
		t.Error("system prompt should contain user message text from history")
	}
	if !strings.Contains(prompt, `"role":"user"`) {
		t.Error("system prompt should contain JSON-formatted role field")
	}
	if !strings.Contains(prompt, `"role":"assistant"`) {
		t.Error("system prompt should contain assistant role in JSON")
	}
	if !strings.Contains(prompt, `"age_delta":`) {
		t.Error("system prompt should expose age_delta on history entries (model-facing recency convention)")
	}

	// Verify fenced code block and untrusted data instruction.
	if !strings.Contains(prompt, "```json") {
		t.Error("system prompt should contain fenced JSON code block")
	}
	if !strings.Contains(prompt, "untrusted data") {
		t.Error("system prompt should instruct model to treat history as untrusted data")
	}

	// Verify it's valid JSON by extracting the fenced block.
	fenceStart := strings.Index(prompt, "```json\n")
	fenceEnd := strings.Index(prompt[fenceStart+8:], "\n```")
	if fenceStart == -1 || fenceEnd == -1 {
		t.Fatal("could not find fenced JSON block boundaries in prompt")
	}
	jsonBlock := prompt[fenceStart+8 : fenceStart+8+fenceEnd]

	var entries []historyEntry
	if err := json.Unmarshal([]byte(jsonBlock), &entries); err != nil {
		t.Fatalf("embedded JSON is invalid: %v\nblock: %s", err, jsonBlock)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries in embedded JSON, got %d", len(entries))
	}
}

func TestBuildSystemPrompt_EmptyHistory(t *testing.T) {
	l := newMinimalLoop()

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "Conversation History") {
		t.Error("system prompt should not contain conversation history section with nil history")
	}
}

func TestBuildSystemPrompt_EmptyHistorySlice(t *testing.T) {
	l := newMinimalLoop()

	prompt := l.buildSystemPrompt(context.Background(), "hello", []memory.Message{})

	if strings.Contains(prompt, "Conversation History") {
		t.Error("system prompt should not contain conversation history section with empty history")
	}
}
