package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
)

// mockArchiveSearcher implements ArchiveSearcher for testing.
type mockArchiveSearcher struct {
	results   []SearchResult
	err       error
	lastQuery string
	lastLimit int
	callCount int
}

func (m *mockArchiveSearcher) Search(opts SearchOptions) ([]SearchResult, error) {
	m.callCount++
	m.lastQuery = opts.Query
	m.lastLimit = opts.Limit
	return m.results, m.err
}

// archivePayload is the projection produced under the "### Past
// Experience" heading. Mirrors the shape of FormatSearchResults so the
// tests can decode without depending on json tags directly.
type archivePayload struct {
	Results   []map[string]any `json:"results"`
	Truncated bool             `json:"truncated"`
}

// parseArchiveBody extracts the JSON body that follows the
// "### Past Experience" heading and returns it for inspection.
func parseArchiveBody(t *testing.T, out string) archivePayload {
	t.Helper()
	if !strings.HasPrefix(out, archiveSectionHeading) {
		t.Fatalf("archive output missing heading prefix\nGot:\n%s", out)
	}
	body := strings.TrimPrefix(out, archiveSectionHeading)
	var payload archivePayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("archive body not valid JSON: %v\nBody: %s", err, body)
	}
	return payload
}

func TestArchiveContextProvider_GetContext(t *testing.T) {
	ts := time.Date(2026, 2, 10, 14, 30, 0, 0, time.UTC)

	makeResult := func(sessionID, role, content string) SearchResult {
		return SearchResult{
			SessionID: sessionID,
			Match: Message{
				Role:      role,
				Content:   content,
				Timestamp: ts,
			},
		}
	}

	makeResultWithContext := func(sessionID, matchRole, matchContent string, before, after []Message) SearchResult {
		return SearchResult{
			SessionID:     sessionID,
			Match:         Message{Role: matchRole, Content: matchContent, Timestamp: ts},
			ContextBefore: before,
			ContextAfter:  after,
		}
	}

	t.Run("subjects_resolve_to_results", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-abc", "assistant", "The pool heater runs from 10am to 4pm daily."),
			},
		}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:switch.pool_heater"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if !strings.HasPrefix(got, "### Past Experience") {
			t.Errorf("output should start with '### Past Experience' heading, got:\n%s", got)
		}
		payload := parseArchiveBody(t, got)
		if len(payload.Results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(payload.Results))
		}
		match := payload.Results[0]["match"].(map[string]any)
		if !strings.Contains(match["content"].(string), "pool heater") {
			t.Errorf("match content should mention pool heater, got %q", match["content"])
		}
		if got, want := match["session_id"], "sess-abc"; got != want {
			t.Errorf("session_id = %v, want %v", got, want)
		}
		if mock.lastQuery != "switch.pool_heater" {
			t.Errorf("query = %q, want %q", mock.lastQuery, "switch.pool_heater")
		}
	})

	t.Run("no_subjects_no_message", func(t *testing.T) {
		mock := &mockArchiveSearcher{}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
		if mock.callCount != 0 {
			t.Error("should not call Search when there's nothing to query")
		}
	})

	t.Run("no_subjects_short_message_fallback", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-xyz", "assistant", "Pool heater info here."),
			},
		}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "pool heater schedule"})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if got == "" {
			t.Fatal("expected output from message fallback")
		}
		if mock.lastQuery != "pool heater schedule" {
			t.Errorf("query = %q, want %q", mock.lastQuery, "pool heater schedule")
		}
	})

	t.Run("no_subjects_long_message_skipped", func(t *testing.T) {
		mock := &mockArchiveSearcher{}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		longMsg := strings.Repeat("word ", 30) // 150 chars
		got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: longMsg})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty for long message, got %q", got)
		}
		if mock.callCount != 0 {
			t.Error("should not call Search for long messages")
		}
	})

	t.Run("no_subjects_multiline_message_skipped", func(t *testing.T) {
		mock := &mockArchiveSearcher{}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "line one\nline two"})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty for multiline message, got %q", got)
		}
	})

	t.Run("subjects_with_no_results", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:sensor.fake"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty for no results, got %q", got)
		}
	})

	t.Run("search_error_soft_fails", func(t *testing.T) {
		mock := &mockArchiveSearcher{err: fmt.Errorf("database locked")}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:light.office"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("expected nil error on soft fail, got: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty on error, got %q", got)
		}
	})

	t.Run("byte_budget_trims_and_marks_truncated", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-001", "assistant", strings.Repeat("First result content. ", 30)),
				makeResult("sess-002", "assistant", strings.Repeat("Second result content. ", 30)),
				makeResult("sess-003", "assistant", strings.Repeat("Third result content. ", 30)),
			},
		}
		// Tight budget — should fit one or two results, not all three.
		p := NewArchiveContextProvider(mock, 5, 900, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:light.office"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if len(got) > 900 {
			t.Errorf("output length %d exceeds budget 900", len(got))
		}
		payload := parseArchiveBody(t, got)
		if len(payload.Results) == 0 {
			t.Fatal("expected at least one result to fit")
		}
		if len(payload.Results) == 3 {
			t.Error("expected truncation to drop at least one result")
		}
		if !payload.Truncated {
			t.Error("truncated flag should be set when results are dropped")
		}
	})

	t.Run("byte_budget_too_small_returns_empty", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-001", "assistant", strings.Repeat("very large content ", 50)),
			},
		}
		p := NewArchiveContextProvider(mock, 5, 50, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:foo"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty when nothing fits, got:\n%s", got)
		}
	})

	t.Run("subject_prefix_stripping", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{
			"entity:light.office",
			"zone:kitchen",
			"contact:dan@example.com",
		})
		_, _ = p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})

		// Verify all prefixes are stripped.
		q := mock.lastQuery
		if strings.Contains(q, "entity:") {
			t.Errorf("query %q should not contain 'entity:' prefix", q)
		}
		if strings.Contains(q, "zone:") {
			t.Errorf("query %q should not contain 'zone:' prefix", q)
		}
		if strings.Contains(q, "contact:") {
			t.Errorf("query %q should not contain 'contact:' prefix", q)
		}
		if !strings.Contains(q, "light.office") {
			t.Errorf("query %q should contain 'light.office'", q)
		}
		if !strings.Contains(q, "kitchen") {
			t.Errorf("query %q should contain 'kitchen'", q)
		}
		if !strings.Contains(q, "dan@example.com") {
			t.Errorf("query %q should contain 'dan@example.com'", q)
		}
	})

	t.Run("duplicate_subjects_deduplicated", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{
			"entity:light.office",
			"entity:light.office",
		})
		_, _ = p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})

		if mock.lastQuery != "light.office" {
			t.Errorf("query = %q, want deduplicated 'light.office'", mock.lastQuery)
		}
	})

	t.Run("max_results_passed_to_search", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 7, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:foo"})
		_, _ = p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})

		if mock.lastLimit != 7 {
			t.Errorf("limit = %d, want 7", mock.lastLimit)
		}
	})

	t.Run("context_messages_included_as_metadata", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResultWithContext("sess-ctx", "assistant", "The answer is 42.",
					[]Message{
						{Role: "user", Content: "What is the answer?", Timestamp: ts.Add(-time.Minute)},
					},
					[]Message{
						{Role: "user", Content: "Thanks!", Timestamp: ts.Add(time.Minute)},
					},
				),
			},
		}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:meaning.of.life"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		payload := parseArchiveBody(t, got)
		if len(payload.Results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(payload.Results))
		}
		hit := payload.Results[0]
		before, _ := hit["context_before"].([]any)
		after, _ := hit["context_after"].([]any)
		if len(before) != 1 || len(after) != 1 {
			t.Errorf("expected one context message on each side, got before=%d after=%d", len(before), len(after))
		}
		matchContent := hit["match"].(map[string]any)["content"].(string)
		if !strings.Contains(matchContent, "The answer is 42.") {
			t.Errorf("match.content should be the match body, got %q", matchContent)
		}
	})

	t.Run("delta_timestamps_not_absolute", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-time", "assistant", "Some past observation."),
			},
		}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := knowledge.WithSubjects(context.Background(), []string{"entity:test"})
		got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: ""})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		// Delta format is "-Ns" or "+Ns"; absolute would contain "T" between date/time.
		// Make sure no date-only or RFC3339 timestamp shows up in the payload.
		if strings.Contains(got, ts.Format(time.DateOnly)) {
			t.Errorf("output should use deltas, not absolute date %q\nGot:\n%s", ts.Format(time.DateOnly), got)
		}
		if strings.Contains(got, ts.Format(time.RFC3339)) {
			t.Errorf("output should use deltas, not RFC3339\nGot:\n%s", got)
		}
		payload := parseArchiveBody(t, got)
		match := payload.Results[0]["match"].(map[string]any)
		tField, _ := match["t"].(string)
		if !strings.HasPrefix(tField, "-") {
			t.Errorf("match.t should be a negative delta like '-Ns', got %q", tField)
		}
	})
}

func TestBuildQuery(t *testing.T) {
	p := NewArchiveContextProvider(nil, 3, 4000, nil)

	tests := []struct {
		name       string
		subjects   []string
		message    string
		wantQuery  string
		wantSource string
	}{
		{
			name:       "subjects_only",
			subjects:   []string{"entity:light.office", "zone:kitchen"},
			wantQuery:  "light.office kitchen",
			wantSource: "subjects",
		},
		{
			name:       "message_fallback",
			message:    "check the heater",
			wantQuery:  "check the heater",
			wantSource: "message_fallback",
		},
		{
			name:       "subjects_take_priority",
			subjects:   []string{"entity:heater"},
			message:    "check the heater",
			wantQuery:  "heater",
			wantSource: "subjects",
		},
		{
			name:       "empty_everything",
			wantQuery:  "",
			wantSource: "",
		},
		{
			name:       "no_prefix_subject",
			subjects:   []string{"bare_subject"},
			wantQuery:  "bare_subject",
			wantSource: "subjects",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQuery, gotSource := p.buildQuery(tt.subjects, tt.message)
			if gotQuery != tt.wantQuery {
				t.Errorf("buildQuery() query = %q, want %q", gotQuery, tt.wantQuery)
			}
			if gotSource != tt.wantSource {
				t.Errorf("buildQuery() source = %q, want %q", gotSource, tt.wantSource)
			}
		})
	}
}

func TestStripSubjectPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"entity:light.office", "light.office"},
		{"zone:kitchen", "kitchen"},
		{"contact:dan@example.com", "dan@example.com"},
		{"bare_value", "bare_value"},
		{"entity:", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripSubjectPrefix(tt.input)
			if got != tt.want {
				t.Errorf("stripSubjectPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPreviewContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short", "hello", "hello"},
		{"empty", "", "(empty)"},
		{"whitespace_only", "   ", "(empty)"},
		{"multiline", "first line\nsecond line", "first line"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := previewContent(tt.input)
			if got != tt.want {
				t.Errorf("previewContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}

	t.Run("long_content_truncated", func(t *testing.T) {
		long := strings.Repeat("word ", 60)
		got := previewContent(long)
		if !strings.HasSuffix(got, "...") {
			t.Errorf("long preview should end with '...', got %q", got)
		}
	})
}
