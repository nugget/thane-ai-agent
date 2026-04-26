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
	if closed.DurationSeconds != 5400 {
		t.Errorf("closed.DurationSeconds = %d, want 5400", closed.DurationSeconds)
	}
	if closed.Title != "Freezer alarm troubleshooting" {
		t.Errorf("closed.Title = %q", closed.Title)
	}

	active := parsed.Sessions[1]
	if active.Ended != "" {
		t.Errorf("active.Ended = %q, want empty string", active.Ended)
	}
	if active.DurationSeconds != 0 {
		t.Errorf("active.DurationSeconds = %d, want 0", active.DurationSeconds)
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

func TestFormatRecentMessages_ContentTruncation(t *testing.T) {
	now := time.Now()
	huge := strings.Repeat("x", maxMessageContentBytes+500)
	messages := []Message{
		{ID: "m1", Role: "user", Content: huge, Timestamp: now.Add(-time.Minute), SessionID: "s1"},
		{ID: "m2", Role: "assistant", Content: "short reply", Timestamp: now, SessionID: "s1"},
	}

	data := FormatRecentMessages(messages, now, false)
	var parsed struct {
		Messages []MessageView `json:"messages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("len = %d, want 2", len(parsed.Messages))
	}
	if !parsed.Messages[0].ContentTruncated {
		t.Error("first message should be flagged content_truncated")
	}
	if len(parsed.Messages[0].Content) > maxMessageContentBytes {
		t.Errorf("truncated content len = %d, want <= %d", len(parsed.Messages[0].Content), maxMessageContentBytes)
	}
	if parsed.Messages[1].ContentTruncated {
		t.Error("short message should not be flagged content_truncated")
	}

	// Schema stability: session_id is always *present* in the JSON,
	// even when its value is empty. Inspect the raw object map so the
	// assertion catches an `omitempty` regression — checking the
	// SessionID field on a typed struct can't distinguish "key absent"
	// from "key present with empty string."
	var raw struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for i, m := range raw.Messages {
		if _, ok := m["session_id"]; !ok {
			t.Errorf("messages[%d] missing session_id key — schema invariant violated", i)
		}
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

func TestFormatSearchResults_BoundsContextPerSide(t *testing.T) {
	// Production hotfix: per-message content was already capped at
	// maxMessageContentBytes, but the per-side context list was not.
	// With the archive's default 50-message-per-direction context
	// expansion, a single search result's serialized form could blow
	// well past the tool's overall byte cap, causing FitPrefix to
	// clip to zero and emit an empty results array. Cap context
	// list length per side so each rendered result is bounded.
	now := time.Now()
	makeMsgs := func(prefix string, n int) []Message {
		out := make([]Message, n)
		for i := range out {
			out[i] = Message{
				Role:      "user",
				Content:   prefix,
				Timestamp: now.Add(-time.Duration(n-i) * time.Minute),
				SessionID: "s1",
			}
		}
		return out
	}
	results := []SearchResult{{
		Match:         Message{Role: "user", Content: "match", Timestamp: now, SessionID: "s1"},
		SessionID:     "s1",
		ContextBefore: makeMsgs("before", 50),
		ContextAfter:  makeMsgs("after", 50),
	}}

	data := FormatSearchResults(results, now, false)
	var parsed struct {
		Results []SearchResultView `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(parsed.Results))
	}
	r := parsed.Results[0]
	if len(r.ContextBefore) != maxSearchContextPerSide {
		t.Errorf("context_before len = %d, want %d", len(r.ContextBefore), maxSearchContextPerSide)
	}
	if len(r.ContextAfter) != maxSearchContextPerSide {
		t.Errorf("context_after len = %d, want %d", len(r.ContextAfter), maxSearchContextPerSide)
	}
}
