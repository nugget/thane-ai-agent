package logging

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDatasetRecordFromRequestContent_PopulatesEnvelope(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 26, 14, 23, 1, 0, time.UTC)
	rc := RequestContent{
		RequestID:        "r_abc",
		SystemPrompt:     "you are an agent",
		UserContent:      "hello",
		Model:            "claude-sonnet",
		AssistantContent: "hi back",
		IterationCount:   3,
		InputTokens:      120,
		OutputTokens:     45,
		ToolsUsed:        map[string]int{"shell": 2, "search": 1},
		Exhausted:        false,
	}

	record := DatasetRecordFromRequestContent(rc, now)

	if record.Dataset != DatasetConversations {
		t.Errorf("dataset = %q, want %q", record.Dataset, DatasetConversations)
	}
	if record.Kind != "request_complete" {
		t.Errorf("kind = %q, want %q", record.Kind, "request_complete")
	}
	if record.RequestID != "r_abc" {
		t.Errorf("request_id = %q, want %q", record.RequestID, "r_abc")
	}
	if record.Source != "agent" {
		t.Errorf("source = %q, want %q", record.Source, "agent")
	}
	if !record.Timestamp.Equal(now.UTC()) {
		t.Errorf("ts = %v, want %v", record.Timestamp, now.UTC())
	}

	if record.Payload["model"] != "claude-sonnet" {
		t.Errorf("payload.model = %v, want claude-sonnet", record.Payload["model"])
	}
	if record.Payload["input_tokens"].(int) != 120 {
		t.Errorf("payload.input_tokens = %v, want 120", record.Payload["input_tokens"])
	}
	if record.Payload["iteration_count"].(int) != 3 {
		t.Errorf("payload.iteration_count = %v, want 3", record.Payload["iteration_count"])
	}
	tools, ok := record.Payload["tools_used"].(map[string]int)
	if !ok || tools["shell"] != 2 {
		t.Errorf("payload.tools_used = %v, want shell:2 search:1", record.Payload["tools_used"])
	}
}

func TestDatasetRecordFromRequestContent_OmitsEmptyExhaustReason(t *testing.T) {
	t.Parallel()

	rc := RequestContent{RequestID: "r1", Model: "m"}
	record := DatasetRecordFromRequestContent(rc, time.Now())
	if _, present := record.Payload["exhaust_reason"]; present {
		t.Error("exhaust_reason should be omitted when empty")
	}
	if _, present := record.Payload["tools_used"]; present {
		t.Error("tools_used should be omitted when empty")
	}
}

func TestNewConversationsRecorder_WritesToDataset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writer, err := OpenDatasetWriter(dir)
	if err != nil {
		t.Fatalf("OpenDatasetWriter() error = %v", err)
	}
	defer writer.Close()

	recorder := NewConversationsRecorder(writer, nil)
	if recorder == nil {
		t.Fatal("NewConversationsRecorder() = nil, want non-nil")
	}

	recorder(context.Background(), RequestContent{
		RequestID:        "r_test",
		Model:            "test-model",
		UserContent:      "ping",
		AssistantContent: "pong",
	})

	lines := readDatasetLines(t, dir, DatasetConversations)
	if len(lines) != 1 {
		t.Fatalf("conversations line count = %d, want 1", len(lines))
	}
	var got DatasetRecord
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.RequestID != "r_test" {
		t.Errorf("request_id = %q, want r_test", got.RequestID)
	}
	if got.Dataset != DatasetConversations {
		t.Errorf("dataset = %q, want %q", got.Dataset, DatasetConversations)
	}
}

func TestNewConversationsRecorder_NilWriterReturnsNil(t *testing.T) {
	t.Parallel()

	if got := NewConversationsRecorder(nil, nil); got != nil {
		t.Error("NewConversationsRecorder(nil, ...) should return nil so the caller can compose without a nil-check")
	}
}
