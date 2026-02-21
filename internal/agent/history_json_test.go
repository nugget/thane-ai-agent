package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

func TestFormatHistoryJSON_Basic(t *testing.T) {
	now := time.Date(2026, 2, 21, 15, 0, 0, 0, time.UTC)
	messages := []memory.Message{
		{Role: "user", Content: "What is the weather?", Timestamp: now},
		{Role: "assistant", Content: "Let me check.", Timestamp: now.Add(5 * time.Second)},
	}

	result := formatHistoryJSON(messages, "UTC")

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
	if !strings.Contains(entries[0].Timestamp, "2026-02-21T15:00:00") {
		t.Errorf("entries[0].Timestamp = %q, want RFC3339 with 2026-02-21T15:00:00", entries[0].Timestamp)
	}

	if entries[1].Role != "assistant" {
		t.Errorf("entries[1].Role = %q, want %q", entries[1].Role, "assistant")
	}
}

func TestFormatHistoryJSON_EmptyMessages(t *testing.T) {
	result := formatHistoryJSON(nil, "UTC")

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
	messages := []memory.Message{
		{
			Role:      "system",
			Content:   "[Conversation Summary] The user asked about email polling.",
			Timestamp: time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC),
		},
		{
			Role:      "user",
			Content:   "Any new emails?",
			Timestamp: time.Date(2026, 2, 21, 14, 0, 0, 0, time.UTC),
		},
	}

	result := formatHistoryJSON(messages, "UTC")

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

func TestFormatHistoryJSON_TimezoneConversion(t *testing.T) {
	utcTime := time.Date(2026, 2, 21, 20, 0, 0, 0, time.UTC)
	messages := []memory.Message{
		{Role: "user", Content: "hello", Timestamp: utcTime},
	}

	result := formatHistoryJSON(messages, "America/Chicago")

	var entries []historyEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// UTC 20:00 → CST 14:00 (UTC-6)
	if !strings.Contains(entries[0].Timestamp, "14:00:00") {
		t.Errorf("expected CST time with 14:00:00, got: %s", entries[0].Timestamp)
	}
}

func TestFormatHistoryJSON_InvalidTimezone(t *testing.T) {
	messages := []memory.Message{
		{Role: "user", Content: "hello", Timestamp: time.Now()},
	}

	// Should not panic with invalid timezone — falls back to local.
	result := formatHistoryJSON(messages, "Not/A/Timezone")

	var entries []historyEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("invalid JSON with bad timezone: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
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

	// Verify it's valid JSON by extracting the JSON portion.
	jsonStart := strings.Index(prompt, "[{")
	jsonEnd := strings.LastIndex(prompt, "}]") + 2
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatal("could not find JSON array boundaries in prompt")
	}
	jsonBlock := prompt[jsonStart:jsonEnd]

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
