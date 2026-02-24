package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/facts"
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

func TestArchiveContextProvider_GetContext(t *testing.T) {
	ts := time.Date(2026, 2, 10, 14, 30, 0, 0, time.UTC)

	makeResult := func(sessionID, role, content string) SearchResult {
		return SearchResult{
			SessionID: sessionID,
			Match: ArchivedMessage{
				Role:      role,
				Content:   content,
				Timestamp: ts,
			},
		}
	}

	makeResultWithContext := func(sessionID, matchRole, matchContent string, before, after []ArchivedMessage) SearchResult {
		return SearchResult{
			SessionID:     sessionID,
			Match:         ArchivedMessage{Role: matchRole, Content: matchContent, Timestamp: ts},
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

		ctx := facts.WithSubjects(context.Background(), []string{"entity:switch.pool_heater"})
		got, err := p.GetContext(ctx, "")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
		}
		if !strings.Contains(got, "Past Experience") {
			t.Error("output should contain 'Past Experience' heading")
		}
		if !strings.Contains(got, "pool heater") {
			t.Error("output should contain matched message content")
		}
		if !strings.Contains(got, "sess-abc") {
			t.Error("output should contain session ID prefix")
		}
		if mock.lastQuery != "switch.pool_heater" {
			t.Errorf("query = %q, want %q", mock.lastQuery, "switch.pool_heater")
		}
	})

	t.Run("no_subjects_no_message", func(t *testing.T) {
		mock := &mockArchiveSearcher{}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		got, err := p.GetContext(context.Background(), "")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
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

		got, err := p.GetContext(context.Background(), "pool heater schedule")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
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
		got, err := p.GetContext(context.Background(), longMsg)
		if err != nil {
			t.Fatalf("GetContext: %v", err)
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

		got, err := p.GetContext(context.Background(), "line one\nline two")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty for multiline message, got %q", got)
		}
	})

	t.Run("subjects_with_no_results", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := facts.WithSubjects(context.Background(), []string{"entity:sensor.fake"})
		got, err := p.GetContext(ctx, "")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty for no results, got %q", got)
		}
	})

	t.Run("search_error_soft_fails", func(t *testing.T) {
		mock := &mockArchiveSearcher{err: fmt.Errorf("database locked")}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := facts.WithSubjects(context.Background(), []string{"entity:light.office"})
		got, err := p.GetContext(ctx, "")
		if err != nil {
			t.Fatalf("expected nil error on soft fail, got: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty on error, got %q", got)
		}
	})

	t.Run("max_bytes_cap_respected", func(t *testing.T) {
		// Create results with enough content to exceed a small byte budget.
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-001", "assistant", "First result with some content that takes up space."),
				makeResult("sess-002", "assistant", "Second result with more content."),
				makeResult("sess-003", "assistant", "Third result that should be omitted."),
			},
		}
		// Very tight budget: only room for heading + ~1 result.
		p := NewArchiveContextProvider(mock, 5, 200, nil)

		ctx := facts.WithSubjects(context.Background(), []string{"entity:light.office"})
		got, err := p.GetContext(ctx, "")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
		}
		if len(got) > 300 { // allow some slack for truncation notice
			t.Errorf("output length %d exceeds expected cap", len(got))
		}
		if !strings.Contains(got, "omitted") {
			t.Error("expected truncation notice when results are capped")
		}
	})

	t.Run("subject_prefix_stripping", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := facts.WithSubjects(context.Background(), []string{
			"entity:light.office",
			"zone:kitchen",
			"contact:dan@example.com",
		})
		_, _ = p.GetContext(ctx, "")

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

		ctx := facts.WithSubjects(context.Background(), []string{
			"entity:light.office",
			"entity:light.office",
		})
		_, _ = p.GetContext(ctx, "")

		if mock.lastQuery != "light.office" {
			t.Errorf("query = %q, want deduplicated 'light.office'", mock.lastQuery)
		}
	})

	t.Run("max_results_passed_to_search", func(t *testing.T) {
		mock := &mockArchiveSearcher{results: nil}
		p := NewArchiveContextProvider(mock, 7, 4000, nil)

		ctx := facts.WithSubjects(context.Background(), []string{"entity:foo"})
		_, _ = p.GetContext(ctx, "")

		if mock.lastLimit != 7 {
			t.Errorf("limit = %d, want 7", mock.lastLimit)
		}
	})

	t.Run("context_messages_included", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResultWithContext("sess-ctx", "assistant", "The answer is 42.",
					[]ArchivedMessage{
						{Role: "user", Content: "What is the answer?", Timestamp: ts.Add(-time.Minute)},
					},
					[]ArchivedMessage{
						{Role: "user", Content: "Thanks!", Timestamp: ts.Add(time.Minute)},
					},
				),
			},
		}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := facts.WithSubjects(context.Background(), []string{"entity:meaning.of.life"})
		got, err := p.GetContext(ctx, "")
		if err != nil {
			t.Fatalf("GetContext: %v", err)
		}
		if !strings.Contains(got, "What is the answer?") {
			t.Error("output should contain context before")
		}
		if !strings.Contains(got, "The answer is 42.") {
			t.Error("output should contain matched message")
		}
		if !strings.Contains(got, "Thanks!") {
			t.Error("output should contain context after")
		}
	})

	t.Run("match_is_bolded", func(t *testing.T) {
		mock := &mockArchiveSearcher{
			results: []SearchResult{
				makeResult("sess-bold", "assistant", "Important finding."),
			},
		}
		p := NewArchiveContextProvider(mock, 3, 4000, nil)

		ctx := facts.WithSubjects(context.Background(), []string{"entity:test"})
		got, _ := p.GetContext(ctx, "")
		if !strings.Contains(got, "**[assistant] Important finding.**") {
			t.Errorf("matched message should be bolded, got:\n%s", got)
		}
	})
}

func TestBuildQuery(t *testing.T) {
	p := NewArchiveContextProvider(nil, 3, 4000, nil)

	tests := []struct {
		name     string
		subjects []string
		message  string
		want     string
	}{
		{
			name:     "subjects_only",
			subjects: []string{"entity:light.office", "zone:kitchen"},
			want:     "light.office kitchen",
		},
		{
			name:    "message_fallback",
			message: "check the heater",
			want:    "check the heater",
		},
		{
			name:     "subjects_take_priority",
			subjects: []string{"entity:heater"},
			message:  "check the heater",
			want:     "heater",
		},
		{
			name: "empty_everything",
			want: "",
		},
		{
			name:     "no_prefix_subject",
			subjects: []string{"bare_subject"},
			want:     "bare_subject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.buildQuery(tt.subjects, tt.message)
			if got != tt.want {
				t.Errorf("buildQuery() = %q, want %q", got, tt.want)
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

func TestTruncateContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short", "hello", "hello"},
		{"empty", "", "(empty)"},
		{"whitespace_only", "   ", "(empty)"},
		{"multiline", "first line\nsecond line", "first line..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateContent(tt.input)
			if got != tt.want {
				t.Errorf("truncateContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}

	t.Run("long_content_truncated", func(t *testing.T) {
		long := strings.Repeat("word ", 60) // 300 chars
		got := truncateContent(long)
		if len(got) > maxMessageChars+10 { // allow for "..."
			t.Errorf("truncated length %d exceeds limit", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Error("truncated content should end with '...'")
		}
	})
}
