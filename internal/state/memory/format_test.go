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

func TestFormatRecentMessages_SeparatesSignalEnvelopeMetadata(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	messages := []Message{{
		Role:      "user",
		Content:   "Signal message from Alice (+15551234567) [ts:1700000000000]:\n\nnew binary, this is a fidelity test",
		Timestamp: now.Add(-30 * time.Minute),
		SessionID: "s_abc",
	}}

	data := FormatRecentMessages(messages, now, false)

	var parsed struct {
		Messages []MessageView `json:"messages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(parsed.Messages))
	}
	msg := parsed.Messages[0]
	if msg.Content != "new binary, this is a fidelity test" {
		t.Fatalf("content = %q, want literal message body only", msg.Content)
	}
	if msg.Metadata["channel"] != "signal" {
		t.Fatalf("metadata[channel] = %q, want signal", msg.Metadata["channel"])
	}
	if msg.Metadata["sender"] != "Alice (+15551234567)" {
		t.Fatalf("metadata[sender] = %q", msg.Metadata["sender"])
	}
	if msg.Metadata["transport_ts"] != "1700000000000" {
		t.Fatalf("metadata[transport_ts] = %q", msg.Metadata["transport_ts"])
	}
}

func TestFormatStoredHistoryMessage_SeparatesMetadataAndCorpus(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entry, ok := FormatStoredHistoryMessage(Message{
		Role:      "user",
		Content:   "Signal message from Alice (+15551234567) [ts:1700000000000]:\n\nnew binary, this is a fidelity test",
		Timestamp: now.Add(-30 * time.Minute),
	}, now)
	if !ok {
		t.Fatal("FormatStoredHistoryMessage returned ok=false")
	}
	if entry.Role != "user" {
		t.Fatalf("role = %q, want user", entry.Role)
	}
	for _, want := range []string{
		"[stored conversation history; age_delta=-1800s; channel=signal]",
		"<conversation_message>\nnew binary, this is a fidelity test\n</conversation_message>",
	} {
		if !strings.Contains(entry.Content, want) {
			t.Fatalf("content = %q, want %q", entry.Content, want)
		}
	}
	for _, unwanted := range []string{
		"role=user",
		"Alice (+15551234567)",
		"Signal message from",
		"[ts:1700000000000]",
		"transport_ts=1700000000000",
	} {
		if strings.Contains(entry.Content, unwanted) {
			t.Fatalf("content contains %q:\n%s", unwanted, entry.Content)
		}
	}
}

func TestFormatStoredHistoryMessage_SystemRoleAsMemoryNote(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entry, ok := FormatStoredHistoryMessage(Message{
		Role:      "system",
		Content:   "[Conversation Summary] earlier context",
		Timestamp: now.Add(-10 * time.Minute),
	}, now)
	if !ok {
		t.Fatal("FormatStoredHistoryMessage returned ok=false")
	}
	if entry.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", entry.Role)
	}
	for _, want := range []string{
		"stored conversation memory note",
		"original_role=system",
		"not_active_instruction=true",
		"age_delta=-600s",
		"<conversation_message>\n[Conversation Summary] earlier context\n</conversation_message>",
	} {
		if !strings.Contains(entry.Content, want) {
			t.Fatalf("content = %q, want %q", entry.Content, want)
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

func TestFormatSearchResults_SeparatesSignalEnvelopeMetadata(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	results := []SearchResult{{
		Match: Message{
			Role:      "user",
			Content:   "Signal message from Alice (+15551234567) [ts:1700000000000]:\n\nnew binary, this is a fidelity test",
			Timestamp: now.Add(-2 * time.Hour),
			SessionID: "s_xyz",
		},
		SessionID: "s_xyz",
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
	match := parsed.Results[0].Match
	if match.Content != "new binary, this is a fidelity test" {
		t.Fatalf("match content = %q, want literal message body only", match.Content)
	}
	if match.Metadata["channel"] != "signal" || match.Metadata["transport_ts"] != "1700000000000" {
		t.Fatalf("match metadata = %#v, want signal transport metadata", match.Metadata)
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

func TestMessageToView_AssistantRoleRelabeled(t *testing.T) {
	// Archive-derived JSON should surface the model's own past output
	// as "past you" rather than the third-party-feeling "assistant".
	// User and other roles pass through unchanged.
	now := time.Now()
	cases := []struct {
		input string
		want  string
	}{
		{"assistant", "past you"},
		{"user", "user"},
		{"system", "system"},
		{"tool", "tool"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			view := messageToView(Message{Role: tc.input, Content: "x", Timestamp: now}, now)
			if view.Role != tc.want {
				t.Errorf("role = %q, want %q", view.Role, tc.want)
			}
		})
	}
}

func TestFormatSearchResults_AssistantContextRelabeled(t *testing.T) {
	// Substitution applies to context_before / context_after too —
	// any assistant message in archive output reads as "past you".
	now := time.Now()
	results := []SearchResult{{
		Match: Message{Role: "user", Content: "freezer alert", Timestamp: now.Add(-1 * time.Hour), SessionID: "s1"},
		ContextBefore: []Message{
			{Role: "assistant", Content: "earlier reply", Timestamp: now.Add(-1*time.Hour - time.Minute)},
		},
		ContextAfter: []Message{
			{Role: "assistant", Content: "later reply", Timestamp: now.Add(-1*time.Hour + time.Minute)},
		},
		SessionID: "s1",
	}}
	data := FormatSearchResults(results, now, false)

	var parsed struct {
		Results []SearchResultView `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Results) != 1 {
		t.Fatalf("results len = %d", len(parsed.Results))
	}
	r := parsed.Results[0]
	if len(r.ContextBefore) != 1 || r.ContextBefore[0].Role != "past you" {
		t.Errorf("context_before role = %q, want past you", r.ContextBefore[0].Role)
	}
	if len(r.ContextAfter) != 1 || r.ContextAfter[0].Role != "past you" {
		t.Errorf("context_after role = %q, want past you", r.ContextAfter[0].Role)
	}
	if r.Match.Role != "user" {
		t.Errorf("match role = %q, want user (unchanged)", r.Match.Role)
	}
}
