package episodic

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// mockArchive implements ArchiveReader for testing.
type mockArchive struct {
	sessions      []*memory.Session
	transcripts   map[string][]memory.ArchivedMessage
	listErr       error
	transcriptErr error
}

func (m *mockArchive) ListSessions(_ string, _ int) ([]*memory.Session, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.sessions, nil
}

func (m *mockArchive) GetSessionTranscript(sessionID string) ([]memory.ArchivedMessage, error) {
	if m.transcriptErr != nil {
		return nil, m.transcriptErr
	}
	return m.transcripts[sessionID], nil
}

// timeAt returns a time.Time at the given hour offset from a base time.
func timeAt(base time.Time, hoursAgo float64) time.Time {
	return base.Add(-time.Duration(hoursAgo * float64(time.Hour)))
}

// ptrTime returns a pointer to a time.Time.
func ptrTime(t time.Time) *time.Time {
	return &t
}

func TestGetContext_Empty(t *testing.T) {
	p := NewProvider(nil, slog.Default(), Config{
		LookbackDays:  2,
		HistoryTokens: 4000,
	})

	got, err := p.GetContext(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestGetContext_DailyFilesOnly(t *testing.T) {
	dir := t.TempDir()
	fixedNow := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	today := fixedNow.Format("2006-01-02")
	err := os.WriteFile(filepath.Join(dir, today+".md"), []byte("Worked on FTS5 today."), 0644)
	if err != nil {
		t.Fatal(err)
	}

	p := NewProvider(&mockArchive{}, slog.Default(), Config{
		DailyDir:      dir,
		LookbackDays:  2,
		HistoryTokens: 4000,
	})
	p.nowFunc = func() time.Time { return fixedNow }

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Daily Notes") {
		t.Error("expected Daily Notes section")
	}
	if !strings.Contains(got, "Worked on FTS5 today.") {
		t.Error("expected daily file content")
	}
	if !strings.Contains(got, "Today") {
		t.Error("expected Today label")
	}
	if strings.Contains(got, "Recent Conversations") {
		t.Error("should not have Recent Conversations when archive is empty")
	}
}

func TestGetContext_HistoryOnly(t *testing.T) {
	now := time.Now().UTC()
	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s1",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
				Title:     "Recent chat",
				Metadata: &memory.SessionMetadata{
					OneLiner:  "Discussed delegation",
					Paragraph: "We discussed the delegation feature in detail.",
				},
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s1": {
				{Role: "user", Content: "How does delegation work?", Timestamp: timeAt(now, 1)},
				{Role: "assistant", Content: "Delegation uses profiles to filter tools.", Timestamp: timeAt(now, 0.9)},
			},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		LookbackDays:      2,
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "Daily Notes") {
		t.Error("should not have Daily Notes without daily_dir")
	}
	if !strings.Contains(got, "Recent Conversations") {
		t.Error("expected Recent Conversations section")
	}
	if !strings.Contains(got, "ARCHIVED HISTORY") {
		t.Error("expected ARCHIVED HISTORY framing")
	}
	if !strings.Contains(got, "PAST sessions") {
		t.Error("expected temporal boundary warning")
	}
	if !strings.Contains(got, "delegation") {
		t.Errorf("expected transcript content about delegation, got: %s", got)
	}
}

func TestGetContext_Combined(t *testing.T) {
	dir := t.TempDir()
	fixedNow := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	today := fixedNow.Format("2006-01-02")
	if err := os.WriteFile(filepath.Join(dir, today+".md"), []byte("Morning standup notes."), 0644); err != nil {
		t.Fatal(err)
	}

	now := fixedNow
	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s1",
				StartedAt: timeAt(now, 2),
				EndedAt:   ptrTime(timeAt(now, 1)),
				Title:     "Earlier session",
				Metadata:  &memory.SessionMetadata{OneLiner: "Quick chat"},
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s1": {
				{Role: "user", Content: "Hello", Timestamp: timeAt(now, 2)},
				{Role: "assistant", Content: "Hi there!", Timestamp: timeAt(now, 1.9)},
			},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		DailyDir:          dir,
		LookbackDays:      1,
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})
	p.nowFunc = func() time.Time { return fixedNow }

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Daily Notes") {
		t.Error("expected Daily Notes section")
	}
	if !strings.Contains(got, "Recent Conversations") {
		t.Error("expected Recent Conversations section")
	}
}

func TestDailyMemory_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	fixedNow := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	// Only create yesterday's file, not today's.
	yesterday := fixedNow.AddDate(0, 0, -1).Format("2006-01-02")
	if err := os.WriteFile(filepath.Join(dir, yesterday+".md"), []byte("Yesterday's notes."), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(nil, slog.Default(), Config{
		DailyDir:     dir,
		LookbackDays: 2,
	})
	p.nowFunc = func() time.Time { return fixedNow }

	got := p.getDailyMemory()
	if !strings.Contains(got, "Yesterday") {
		t.Error("expected Yesterday label")
	}
	if !strings.Contains(got, "Yesterday's notes.") {
		t.Error("expected yesterday's content")
	}
	if strings.Contains(got, "Today") {
		t.Error("should not have Today when file is missing")
	}
}

func TestDailyMemory_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	fixedNow := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	today := fixedNow.Format("2006-01-02")
	if err := os.WriteFile(filepath.Join(dir, today+".md"), []byte("  \n  "), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(nil, slog.Default(), Config{
		DailyDir:     dir,
		LookbackDays: 1,
	})
	p.nowFunc = func() time.Time { return fixedNow }

	got := p.getDailyMemory()
	if got != "" {
		t.Errorf("expected empty string for whitespace-only file, got %q", got)
	}
}

func TestRecencyGradient(t *testing.T) {
	now := time.Now().UTC()

	sessions := make([]*memory.Session, 6)
	transcripts := make(map[string][]memory.ArchivedMessage)

	for i := range 6 {
		id := fmt.Sprintf("s%d", i)
		ended := timeAt(now, float64(i)+0.5)
		sessions[i] = &memory.Session{
			ID:        id,
			StartedAt: timeAt(now, float64(i)+1),
			EndedAt:   &ended,
			Title:     fmt.Sprintf("Session %d", i),
			Metadata: &memory.SessionMetadata{
				OneLiner:  fmt.Sprintf("One-liner for session %d", i),
				Paragraph: fmt.Sprintf("Paragraph summary for session %d with more detail.", i),
			},
		}
		transcripts[id] = []memory.ArchivedMessage{
			{Role: "user", Content: fmt.Sprintf("User message in session %d", i), Timestamp: timeAt(now, float64(i)+1)},
			{Role: "assistant", Content: fmt.Sprintf("Assistant reply in session %d", i), Timestamp: timeAt(now, float64(i)+0.9)},
		}
	}

	archive := &mockArchive{sessions: sessions, transcripts: transcripts}

	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     8000, // Large budget to include all sessions.
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()

	// Session 0 should have transcript (user/assistant messages).
	if !strings.Contains(got, "User message in session 0") {
		t.Error("most recent session should have transcript excerpt")
	}

	// Sessions 1-3 should have paragraph summaries.
	for i := 1; i <= 3; i++ {
		expected := fmt.Sprintf("Paragraph summary for session %d", i)
		if !strings.Contains(got, expected) {
			t.Errorf("session %d should have paragraph summary", i)
		}
	}

	// Sessions 4-5 should have one-liners.
	for i := 4; i <= 5; i++ {
		expected := fmt.Sprintf("One-liner for session %d", i)
		if !strings.Contains(got, expected) {
			t.Errorf("session %d should have one-liner", i)
		}
	}
}

func TestTokenBudget(t *testing.T) {
	now := time.Now().UTC()

	sessions := make([]*memory.Session, 10)
	for i := range 10 {
		ended := timeAt(now, float64(i)+0.5)
		sessions[i] = &memory.Session{
			ID:        fmt.Sprintf("s%d", i),
			StartedAt: timeAt(now, float64(i)+1),
			EndedAt:   &ended,
			Title:     fmt.Sprintf("Session %d", i),
			Metadata: &memory.SessionMetadata{
				OneLiner:  fmt.Sprintf("One-liner for session %d", i),
				Paragraph: fmt.Sprintf("Paragraph summary for session %d with more detail.", i),
			},
		}
	}

	archive := &mockArchive{
		sessions: sessions,
		transcripts: map[string][]memory.ArchivedMessage{
			"s0": {
				{Role: "user", Content: "Hello", Timestamp: timeAt(now, 1)},
				{Role: "assistant", Content: "Hi!", Timestamp: timeAt(now, 0.9)},
			},
		},
	}

	// Very small budget should limit output.
	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     100, // ~400 chars
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()
	if got == "" {
		t.Fatal("expected some output even with small budget")
	}

	// Should not contain all 10 sessions.
	if strings.Contains(got, "session 9") {
		t.Error("budget should have prevented including all sessions")
	}
}

func TestGapDetection(t *testing.T) {
	now := time.Now().UTC()

	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s0",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
				Title:     "Recent",
				Metadata:  &memory.SessionMetadata{OneLiner: "Recent session"},
			},
			{
				// 5 hours earlier — should trigger gap.
				ID:        "s1",
				StartedAt: timeAt(now, 6),
				EndedAt:   ptrTime(timeAt(now, 5)),
				Title:     "Earlier",
				Metadata:  &memory.SessionMetadata{OneLiner: "Earlier session"},
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s0": {{Role: "user", Content: "Hey", Timestamp: timeAt(now, 1)}},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()
	if !strings.Contains(got, "gap") {
		t.Errorf("expected gap annotation, got: %s", got)
	}
	if !strings.Contains(got, "4h") {
		t.Errorf("expected ~4h gap annotation, got: %s", got)
	}
}

func TestNoMetadata(t *testing.T) {
	now := time.Now().UTC()

	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s0",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
				// No Title, no Metadata, no Summary.
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s0": {
				{Role: "user", Content: "Test message", Timestamp: timeAt(now, 1)},
			},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()
	if got != "" {
		t.Errorf("expected empty output for session without metadata, got:\n%s", got)
	}
}

func TestEmptySessionsSkipped(t *testing.T) {
	now := time.Now().UTC()

	archive := &mockArchive{
		sessions: []*memory.Session{
			// Most recent: has title → should be included.
			{
				ID:        "s1",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
				Title:     "Useful session",
			},
			// Second: empty delegate → should be skipped.
			{
				ID:        "s2",
				StartedAt: timeAt(now, 2),
				EndedAt:   ptrTime(timeAt(now, 1.5)),
			},
			// Third: has summary → should be included.
			{
				ID:        "s3",
				StartedAt: timeAt(now, 3),
				EndedAt:   ptrTime(timeAt(now, 2.5)),
				Summary:   "Earlier session with content.",
			},
			// Fourth: another empty delegate → should be skipped.
			{
				ID:        "s4",
				StartedAt: timeAt(now, 4),
				EndedAt:   ptrTime(timeAt(now, 3.5)),
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s1": {{Role: "user", Content: "Hello", Timestamp: timeAt(now, 1)}},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()

	if !strings.Contains(got, "Useful session") {
		t.Error("expected session with title to appear")
	}
	if !strings.Contains(got, "Earlier session with content.") {
		t.Error("expected session with summary to appear")
	}
	if strings.Contains(got, "(no summary)") {
		t.Error("empty sessions should not produce '(no summary)' entries")
	}
}

func TestSessionHasContent(t *testing.T) {
	tests := []struct {
		name    string
		session *memory.Session
		want    bool
	}{
		{"title only", &memory.Session{Title: "A title"}, true},
		{"summary only", &memory.Session{Summary: "A summary"}, true},
		{"metadata oneliner", &memory.Session{Metadata: &memory.SessionMetadata{OneLiner: "short"}}, true},
		{"metadata paragraph", &memory.Session{Metadata: &memory.SessionMetadata{Paragraph: "long"}}, true},
		{"empty metadata", &memory.Session{Metadata: &memory.SessionMetadata{}}, false},
		{"nil metadata", &memory.Session{}, false},
		{"completely empty", &memory.Session{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionHasContent(tc.session)
			if got != tc.want {
				t.Errorf("sessionHasContent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionFallbackChain(t *testing.T) {
	tests := []struct {
		name     string
		session  *memory.Session
		wantPara string
		wantOne  string
	}{
		{
			name: "full metadata",
			session: &memory.Session{
				Metadata: &memory.SessionMetadata{
					Paragraph: "Full paragraph.",
					OneLiner:  "Short version.",
				},
				Summary: "Summary text.",
				Title:   "The Title",
			},
			wantPara: "Full paragraph.",
			wantOne:  "Short version.",
		},
		{
			name: "summary only",
			session: &memory.Session{
				Summary: "Just a summary. With extra detail.",
			},
			wantPara: "Just a summary. With extra detail.",
			wantOne:  "Just a summary.",
		},
		{
			name: "title only",
			session: &memory.Session{
				Title: "Just a title",
			},
			wantPara: "Just a title",
			wantOne:  "Just a title",
		},
		{
			name:     "nothing",
			session:  &memory.Session{},
			wantPara: "(no summary available)",
			wantOne:  "(no summary)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPara := sessionParagraph(tc.session)
			if gotPara != tc.wantPara {
				t.Errorf("sessionParagraph: got %q, want %q", gotPara, tc.wantPara)
			}
			gotOne := sessionOneLiner(tc.session)
			if gotOne != tc.wantOne {
				t.Errorf("sessionOneLiner: got %q, want %q", gotOne, tc.wantOne)
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	t.Run("estimateTokens", func(t *testing.T) {
		if got := estimateTokens("1234"); got != 1 {
			t.Errorf("got %d, want 1", got)
		}
		if got := estimateTokens("12345678"); got != 2 {
			t.Errorf("got %d, want 2", got)
		}
		if got := estimateTokens(""); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("truncateContent", func(t *testing.T) {
		short := "hello"
		if got := truncateContent(short, 10); got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
		long := strings.Repeat("x", 300)
		got := truncateContent(long, 200)
		if got != strings.Repeat("x", 200)+"..." {
			t.Errorf("truncated content mismatch")
		}
		// Newlines should be replaced with spaces.
		multiline := "line1\nline2\nline3"
		if got := truncateContent(multiline, 100); strings.Contains(got, "\n") {
			t.Errorf("expected newlines replaced, got %q", got)
		}
		// UTF-8 safety: multi-byte runes should not be split.
		utf8Str := strings.Repeat("\u00e9", 10) // 10 × é (2 bytes each)
		got = truncateContent(utf8Str, 5)
		if !strings.HasSuffix(got, "...") {
			t.Errorf("expected ... suffix, got %q", got)
		}
		// Should have exactly 5 runes + "..."
		if got != strings.Repeat("\u00e9", 5)+"..." {
			t.Errorf("UTF-8 truncation failed: got %q", got)
		}
	})

	t.Run("firstSentence", func(t *testing.T) {
		if got := firstSentence("Hello world. More text."); got != "Hello world." {
			t.Errorf("got %q", got)
		}
		if got := firstSentence("No period here"); got != "No period here" {
			t.Errorf("got %q", got)
		}
		long := strings.Repeat("x", 100)
		got := firstSentence(long)
		if got != strings.Repeat("x", 80)+"..." {
			t.Errorf("long truncation failed: got %q", got)
		}
		// UTF-8 safety: truncation should respect rune boundaries.
		utf8Long := strings.Repeat("\u00e9", 100)
		got = firstSentence(utf8Long)
		if got != strings.Repeat("\u00e9", 80)+"..." {
			t.Errorf("UTF-8 firstSentence failed: got %q", got)
		}
	})

	t.Run("formatGap", func(t *testing.T) {
		tests := []struct {
			d    time.Duration
			want string
		}{
			{25 * time.Minute, "25m"},
			{2 * time.Hour, "2h"},
			{24 * time.Hour, "1 day"},
			{72 * time.Hour, "3 days"},
		}
		for _, tc := range tests {
			if got := formatGap(tc.d); got != tc.want {
				t.Errorf("formatGap(%v): got %q, want %q", tc.d, got, tc.want)
			}
		}
	})

	t.Run("dayLabel", func(t *testing.T) {
		now := time.Now()
		if got := dayLabel(0, now); got != "Today" {
			t.Errorf("got %q, want Today", got)
		}
		if got := dayLabel(1, now); got != "Yesterday" {
			t.Errorf("got %q, want Yesterday", got)
		}
		twoDaysAgo := now.AddDate(0, 0, -2)
		got := dayLabel(2, twoDaysAgo)
		if got == "Today" || got == "Yesterday" {
			t.Errorf("got %q, expected day name", got)
		}
	})

	t.Run("expandTilde", func(t *testing.T) {
		home, _ := os.UserHomeDir()
		if got := expandTilde("~/foo"); got != filepath.Join(home, "foo") {
			t.Errorf("got %q", got)
		}
		if got := expandTilde("/absolute/path"); got != "/absolute/path" {
			t.Errorf("got %q", got)
		}
		if got := expandTilde("~"); got != home {
			t.Errorf("got %q, want %q", got, home)
		}
		if got := expandTilde("relative"); got != "relative" {
			t.Errorf("got %q", got)
		}
	})
}

func TestMessageTimestamps(t *testing.T) {
	// Fixed time so we can assert exact RFC3339 output.
	base := time.Date(2026, 2, 15, 21, 0, 0, 0, time.FixedZone("CST", -6*3600))
	msgTime := base.Add(-10 * time.Minute) // 20:50

	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s1",
				StartedAt: base.Add(-1 * time.Hour),
				EndedAt:   ptrTime(base.Add(-30 * time.Minute)),
				Title:     "Timestamp test",
				Metadata:  &memory.SessionMetadata{OneLiner: "Testing timestamps"},
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s1": {
				{Role: "user", Content: "what time is it", Timestamp: msgTime},
				{Role: "assistant", Content: "It's 8:50 PM", Timestamp: msgTime.Add(5 * time.Second)},
			},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		Timezone:          "America/Chicago",
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()

	// Each transcript message should have an RFC3339 timestamp prefix.
	if !strings.Contains(got, "[2026-02-15T20:50:00-06:00] **user:**") {
		t.Errorf("expected RFC3339 timestamp on user message, got:\n%s", got)
	}
	if !strings.Contains(got, "[2026-02-15T20:50:05-06:00] **assistant:**") {
		t.Errorf("expected RFC3339 timestamp on assistant message, got:\n%s", got)
	}
}

func TestSessionHeaderRFC3339(t *testing.T) {
	base := time.Date(2026, 2, 15, 21, 4, 0, 0, time.FixedZone("CST", -6*3600))

	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s1",
				StartedAt: base,
				EndedAt:   ptrTime(base.Add(30 * time.Minute)),
				Title:     "RFC3339 header test",
				Metadata:  &memory.SessionMetadata{OneLiner: "Testing header format"},
			},
		},
		transcripts: map[string][]memory.ArchivedMessage{
			"s1": {
				{Role: "user", Content: "Hello", Timestamp: base},
			},
		},
	}

	p := NewProvider(archive, slog.Default(), Config{
		Timezone:          "America/Chicago",
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()

	// Match the full header pattern so message timestamps alone can't
	// satisfy this assertion.
	if !strings.Contains(got, "**[2026-02-15T21:04:00-06:00 — RFC3339 header test]**") {
		t.Errorf("expected RFC3339-formatted session header, got:\n%s", got)
	}
	// Should NOT contain old-style "Feb 15 21:04".
	if strings.Contains(got, "Feb 15 21:04") {
		t.Errorf("session header should not use old format, got:\n%s", got)
	}
}

func TestArchiveError(t *testing.T) {
	archive := &mockArchive{listErr: fmt.Errorf("database locked")}

	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()
	if got != "" {
		t.Errorf("expected empty string on archive error, got %q", got)
	}
}

func TestTranscriptError(t *testing.T) {
	now := time.Now().UTC()

	archive := &mockArchive{
		sessions: []*memory.Session{
			{
				ID:        "s0",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
				Title:     "Test",
				Metadata:  &memory.SessionMetadata{Paragraph: "Fallback paragraph."},
			},
		},
		transcriptErr: fmt.Errorf("IO error"),
	}

	p := NewProvider(archive, slog.Default(), Config{
		HistoryTokens:     4000,
		SessionGapMinutes: 30,
	})

	got := p.getRecentHistory()
	// Should fall back to paragraph when transcript fails.
	if !strings.Contains(got, "Fallback paragraph.") {
		t.Errorf("expected paragraph fallback on transcript error, got: %s", got)
	}
}
