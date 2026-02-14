package delegate

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/llm"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDelegationStore_RecordAndGet(t *testing.T) {
	store, err := NewDelegationStore(openTestDB(t))
	if err != nil {
		t.Fatalf("NewDelegationStore: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond)
	rec := &DelegationRecord{
		ID:             "del-001",
		ConversationID: "conv-abc",
		Task:           "check the office lights",
		Guidance:       "use get_state",
		Profile:        "ha",
		Model:          "qwen3:4b",
		Iterations:     3,
		MaxIterations:  8,
		InputTokens:    1500,
		OutputTokens:   200,
		Exhausted:      false,
		ToolsCalled:    map[string]int{"get_state": 2, "list_entities": 1},
		ResultContent:  "The office light is on.",
		StartedAt:      now,
		CompletedAt:    now.Add(5 * time.Second),
		DurationMs:     5000,
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := store.Get("del-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != rec.ID {
		t.Errorf("ID = %q, want %q", got.ID, rec.ID)
	}
	if got.ConversationID != rec.ConversationID {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, rec.ConversationID)
	}
	if got.Task != rec.Task {
		t.Errorf("Task = %q, want %q", got.Task, rec.Task)
	}
	if got.Guidance != rec.Guidance {
		t.Errorf("Guidance = %q, want %q", got.Guidance, rec.Guidance)
	}
	if got.Profile != rec.Profile {
		t.Errorf("Profile = %q, want %q", got.Profile, rec.Profile)
	}
	if got.Model != rec.Model {
		t.Errorf("Model = %q, want %q", got.Model, rec.Model)
	}
	if got.Iterations != rec.Iterations {
		t.Errorf("Iterations = %d, want %d", got.Iterations, rec.Iterations)
	}
	if got.MaxIterations != rec.MaxIterations {
		t.Errorf("MaxIterations = %d, want %d", got.MaxIterations, rec.MaxIterations)
	}
	if got.InputTokens != rec.InputTokens {
		t.Errorf("InputTokens = %d, want %d", got.InputTokens, rec.InputTokens)
	}
	if got.OutputTokens != rec.OutputTokens {
		t.Errorf("OutputTokens = %d, want %d", got.OutputTokens, rec.OutputTokens)
	}
	if got.Exhausted != rec.Exhausted {
		t.Errorf("Exhausted = %v, want %v", got.Exhausted, rec.Exhausted)
	}
	if got.ResultContent != rec.ResultContent {
		t.Errorf("ResultContent = %q, want %q", got.ResultContent, rec.ResultContent)
	}
	if got.DurationMs != rec.DurationMs {
		t.Errorf("DurationMs = %d, want %d", got.DurationMs, rec.DurationMs)
	}
}

func TestDelegationStore_List(t *testing.T) {
	store, err := NewDelegationStore(openTestDB(t))
	if err != nil {
		t.Fatalf("NewDelegationStore: %v", err)
	}

	base := time.Date(2026, 2, 14, 10, 0, 0, 0, time.UTC)
	for i, id := range []string{"del-a", "del-b", "del-c"} {
		rec := &DelegationRecord{
			ID:             id,
			ConversationID: "conv-1",
			Task:           "task " + id,
			Profile:        "general",
			Model:          "test-model",
			Iterations:     1,
			MaxIterations:  8,
			StartedAt:      base.Add(time.Duration(i) * time.Minute),
			CompletedAt:    base.Add(time.Duration(i)*time.Minute + 10*time.Second),
			DurationMs:     10000,
		}
		if err := store.Record(rec); err != nil {
			t.Fatalf("Record %q: %v", id, err)
		}
	}

	// List all — should be newest first.
	records, err := store.List(0)
	if err != nil {
		t.Fatalf("List(0): %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("List(0) count = %d, want 3", len(records))
	}
	if records[0].ID != "del-c" {
		t.Errorf("List[0].ID = %q, want del-c (newest)", records[0].ID)
	}
	if records[2].ID != "del-a" {
		t.Errorf("List[2].ID = %q, want del-a (oldest)", records[2].ID)
	}

	// List with limit.
	limited, err := store.List(2)
	if err != nil {
		t.Fatalf("List(2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("List(2) count = %d, want 2", len(limited))
	}
}

func TestDelegationStore_MessagesRoundTrip(t *testing.T) {
	store, err := NewDelegationStore(openTestDB(t))
	if err != nil {
		t.Fatalf("NewDelegationStore: %v", err)
	}

	msgs := []llm.Message{
		{Role: "system", Content: "You are a helper."},
		{Role: "user", Content: "Check the lights."},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "tc-1",
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      "get_state",
					Arguments: map[string]any{"entity_id": "light.office"},
				},
			}},
		},
		{Role: "tool", Content: `{"state":"on"}`, ToolCallID: "tc-1"},
		{Role: "assistant", Content: "The office light is on."},
	}

	now := time.Now()
	rec := &DelegationRecord{
		ID:             "del-msgs",
		ConversationID: "conv-1",
		Task:           "check lights",
		Profile:        "ha",
		Model:          "test-model",
		Iterations:     2,
		MaxIterations:  8,
		Messages:       msgs,
		ResultContent:  "The office light is on.",
		StartedAt:      now,
		CompletedAt:    now.Add(time.Second),
		DurationMs:     1000,
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := store.Get("del-msgs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(got.Messages) != len(msgs) {
		t.Fatalf("Messages count = %d, want %d", len(got.Messages), len(msgs))
	}

	// Verify tool call survived round-trip.
	if len(got.Messages[2].ToolCalls) != 1 {
		t.Fatalf("Messages[2].ToolCalls count = %d, want 1", len(got.Messages[2].ToolCalls))
	}
	tc := got.Messages[2].ToolCalls[0]
	if tc.Function.Name != "get_state" {
		t.Errorf("ToolCall name = %q, want get_state", tc.Function.Name)
	}
	entityID, ok := tc.Function.Arguments["entity_id"].(string)
	if !ok || entityID != "light.office" {
		t.Errorf("ToolCall entity_id = %v, want light.office", tc.Function.Arguments["entity_id"])
	}

	// Verify tool result message.
	if got.Messages[3].ToolCallID != "tc-1" {
		t.Errorf("Messages[3].ToolCallID = %q, want tc-1", got.Messages[3].ToolCallID)
	}
}

func TestDelegationStore_ToolsCalledRoundTrip(t *testing.T) {
	store, err := NewDelegationStore(openTestDB(t))
	if err != nil {
		t.Fatalf("NewDelegationStore: %v", err)
	}

	tools := map[string]int{"get_state": 3, "list_entities": 1, "call_service": 2}
	now := time.Now()
	rec := &DelegationRecord{
		ID:             "del-tools",
		ConversationID: "conv-1",
		Task:           "test",
		Profile:        "general",
		Model:          "test-model",
		Iterations:     1,
		MaxIterations:  8,
		ToolsCalled:    tools,
		StartedAt:      now,
		CompletedAt:    now,
		DurationMs:     100,
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := store.Get("del-tools")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(got.ToolsCalled) != len(tools) {
		t.Fatalf("ToolsCalled count = %d, want %d", len(got.ToolsCalled), len(tools))
	}
	for name, want := range tools {
		if got.ToolsCalled[name] != want {
			t.Errorf("ToolsCalled[%q] = %d, want %d", name, got.ToolsCalled[name], want)
		}
	}
}

func TestExtractToolsCalled(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		want     map[string]int
	}{
		{
			name:     "empty messages",
			messages: nil,
			want:     nil,
		},
		{
			name: "no tool calls",
			messages: []llm.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			want: nil,
		},
		{
			name: "single tool call",
			messages: []llm.Message{
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "get_state"},
					}},
				},
			},
			want: map[string]int{"get_state": 1},
		},
		{
			name: "repeated tool calls",
			messages: []llm.Message{
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "get_state"}},
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "list_entities"}},
					},
				},
				{Role: "tool", Content: "result1"},
				{Role: "tool", Content: "result2"},
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "get_state"}},
					},
				},
			},
			want: map[string]int{"get_state": 2, "list_entities": 1},
		},
		{
			name: "mixed messages with text-only assistant",
			messages: []llm.Message{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "do something"},
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{Name: "call_service"}},
					},
				},
				{Role: "tool", Content: "ok"},
				{Role: "assistant", Content: "Done."},
			},
			want: map[string]int{"call_service": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolsCalled(tt.messages)
			if tt.want == nil {
				if got != nil {
					t.Errorf("ExtractToolsCalled() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractToolsCalled() count = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for name, want := range tt.want {
				if got[name] != want {
					t.Errorf("ExtractToolsCalled()[%q] = %d, want %d", name, got[name], want)
				}
			}
		})
	}
}

func TestDelegationStore_GetNotFound(t *testing.T) {
	store, err := NewDelegationStore(openTestDB(t))
	if err != nil {
		t.Fatalf("NewDelegationStore: %v", err)
	}

	_, err = store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
}
