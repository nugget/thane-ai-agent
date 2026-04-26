package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFormatSessionsList_StableSchema(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	endedAt := now.Add(-30 * time.Minute)
	startedAt := endedAt.Add(-90 * time.Minute)

	sessions := []*Session{
		{
			ID:             "s_closed",
			ConversationID: "c_one",
			StartedAt:      startedAt,
			EndedAt:        &endedAt,
			MessageCount:   42,
			Title:          "Freezer alarm troubleshooting",
			Tags:           []string{"home-automation", "alerts"},
			Summary:        "Investigated repeat freezer-door alerts.",
		},
		{
			ID:             "s_active",
			ConversationID: "c_two",
			StartedAt:      now.Add(-10 * time.Minute),
			MessageCount:   3,
			Title:          "",
			Tags:           nil,
			Summary:        "",
		},
	}

	data := FormatSessionsList(sessions, now, false)

	var parsed struct {
		Sessions  []SessionView `json:"sessions"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Sessions) != 2 {
		t.Fatalf("sessions len = %d, want 2", len(parsed.Sessions))
	}

	closed := parsed.Sessions[0]
	if closed.Started != "-7200s" {
		t.Errorf("closed.Started = %q, want -7200s", closed.Started)
	}
	if closed.Ended != "-1800s" {
		t.Errorf("closed.Ended = %q, want -1800s", closed.Ended)
	}
	if closed.Duration != 5400 {
		t.Errorf("closed.Duration = %d, want 5400", closed.Duration)
	}
	if closed.Title != "Freezer alarm troubleshooting" {
		t.Errorf("closed.Title = %q", closed.Title)
	}

	active := parsed.Sessions[1]
	if active.Ended != "" {
		t.Errorf("active.Ended = %q, want empty string", active.Ended)
	}
	if active.Duration != 0 {
		t.Errorf("active.Duration = %d, want 0", active.Duration)
	}
	if active.Tags == nil {
		t.Error("active.Tags is nil, want empty slice — schema stability")
	}
	if len(active.Tags) != 0 {
		t.Errorf("active.Tags = %v, want empty", active.Tags)
	}

	if parsed.Truncated {
		t.Error("Truncated = true, want false")
	}
}

func TestFormatSessionsList_EmptyEmitsArray(t *testing.T) {
	data := FormatSessionsList(nil, time.Now(), false)
	if !strings.Contains(string(data), `"sessions":[]`) {
		t.Errorf("expected empty array, got %s", data)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("output contains null: %s", data)
	}
}

func TestFormatSessionsList_TruncatedFlag(t *testing.T) {
	data := FormatSessionsList(nil, time.Now(), true)
	if !strings.Contains(string(data), `"truncated":true`) {
		t.Errorf("expected truncated=true, got %s", data)
	}
}

func TestFormatRecentMessages_DeltaAndSessionID(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	messages := []Message{
		{
			ID:        "m1",
			Role:      "user",
			Content:   "hey what was that freezer alert again",
			Timestamp: now.Add(-30 * time.Minute),
			SessionID: "s_abc",
		},
		{
			ID:        "m2",
			Role:      "assistant",
			Content:   "the garage chest freezer",
			Timestamp: now.Add(-29 * time.Minute),
			SessionID: "s_abc",
		},
	}

	data := FormatRecentMessages(messages, now, false)

	var parsed struct {
		Messages  []MessageView `json:"messages"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(parsed.Messages))
	}
	if parsed.Messages[0].T != "-1800s" {
		t.Errorf("messages[0].T = %q, want -1800s", parsed.Messages[0].T)
	}
	if parsed.Messages[0].SessionID != "s_abc" {
		t.Errorf("messages[0].SessionID = %q, want s_abc", parsed.Messages[0].SessionID)
	}
	if parsed.Messages[0].Role != "user" {
		t.Errorf("messages[0].Role = %q, want user", parsed.Messages[0].Role)
	}
}

func TestFormatRecentMessages_EmptyEmitsArray(t *testing.T) {
	data := FormatRecentMessages(nil, time.Now(), false)
	if !strings.Contains(string(data), `"messages":[]`) {
		t.Errorf("expected empty array, got %s", data)
	}
}

func TestFormatSearchResults_StructurePreserved(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	results := []SearchResult{
		{
			Match: Message{
				Role:      "user",
				Content:   "freezer alert",
				Timestamp: now.Add(-2 * time.Hour),
				SessionID: "s_xyz",
			},
			SessionID: "s_xyz",
			ContextBefore: []Message{
				{Role: "assistant", Content: "earlier turn", Timestamp: now.Add(-2*time.Hour - time.Minute)},
			},
			ContextAfter: []Message{
				{Role: "assistant", Content: "later turn", Timestamp: now.Add(-2*time.Hour + time.Minute)},
			},
			Highlight: "freezer alert",
		},
	}

	data := FormatSearchResults(results, now, false)

	var parsed struct {
		Results   []SearchResultView `json:"results"`
		Truncated bool               `json:"truncated"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(parsed.Results))
	}
	r := parsed.Results[0]
	if r.Match.SessionID != "s_xyz" {
		t.Errorf("match.SessionID = %q, want s_xyz", r.Match.SessionID)
	}
	if r.Match.T != "-7200s" {
		t.Errorf("match.T = %q, want -7200s", r.Match.T)
	}
	if len(r.ContextBefore) != 1 || len(r.ContextAfter) != 1 {
		t.Errorf("context shape mismatch: before=%d after=%d", len(r.ContextBefore), len(r.ContextAfter))
	}
	if r.Highlight != "freezer alert" {
		t.Errorf("highlight = %q", r.Highlight)
	}
}
