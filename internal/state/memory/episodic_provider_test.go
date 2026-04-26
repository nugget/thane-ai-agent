package memory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockArchive implements ArchiveReader for testing.
type mockArchive struct {
	sessions []*Session
	listErr  error
}

func (m *mockArchive) ListSessions(_ string, _ int) ([]*Session, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.sessions, nil
}

func timeAt(base time.Time, hoursAgo float64) time.Time {
	return base.Add(-time.Duration(hoursAgo * float64(time.Hour)))
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestEpisodicGetContext_Empty(t *testing.T) {
	p := NewEpisodicProvider(nil, slog.Default(), EpisodicConfig{
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

func TestEpisodicGetContext_DailyFilesOnly(t *testing.T) {
	dir := t.TempDir()
	fixedNow := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	today := fixedNow.Format("2006-01-02")
	if err := os.WriteFile(filepath.Join(dir, today+".md"), []byte("Worked on FTS5 today."), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewEpisodicProvider(&mockArchive{}, slog.Default(), EpisodicConfig{
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
	if strings.Contains(got, "Recent Sessions") {
		t.Error("should not have Recent Sessions when archive is empty")
	}
}

func TestEpisodicGetContext_RecentSessionsJSON(t *testing.T) {
	now := time.Now().UTC()
	archive := &mockArchive{
		sessions: []*Session{
			{
				ID:        "s1",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
				Title:     "Recent chat",
				Tags:      []string{"home-automation"},
				Summary:   "Discussed delegation in detail.",
			},
			{
				ID:        "s2",
				StartedAt: timeAt(now, 24),
				EndedAt:   ptrTime(timeAt(now, 23)),
				Title:     "Yesterday",
				Summary:   "Reviewed PRs.",
			},
		},
	}

	p := NewEpisodicProvider(archive, slog.Default(), EpisodicConfig{
		LookbackDays:  2,
		HistoryTokens: 4000,
	})

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if !strings.Contains(got, "### Recent Sessions") {
		t.Errorf("expected Recent Sessions header:\n%s", got)
	}
	if !strings.Contains(got, "archive_session_transcript") {
		t.Error("framing should mention archive_session_transcript as enticement")
	}

	// Pull the fenced JSON block and assert schema/contents.
	jsonBlock := extractFirstFencedJSON(got)
	if jsonBlock == "" {
		t.Fatalf("missing fenced JSON block:\n%s", got)
	}
	var parsed struct {
		Sessions  []SessionView `json:"sessions"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(jsonBlock), &parsed); err != nil {
		t.Fatalf("unmarshal sessions JSON: %v\nblock: %s", err, jsonBlock)
	}
	if len(parsed.Sessions) != 2 {
		t.Fatalf("sessions len = %d, want 2", len(parsed.Sessions))
	}
	if parsed.Sessions[0].ID != "s1" {
		t.Errorf("sessions[0].id = %q, want s1 (newest first)", parsed.Sessions[0].ID)
	}
	if parsed.Sessions[0].Started == "" || !strings.HasPrefix(parsed.Sessions[0].Started, "-") {
		t.Errorf("sessions[0].started = %q, want negative delta", parsed.Sessions[0].Started)
	}
}

func TestEpisodicGetContext_ActiveSessionsExcluded(t *testing.T) {
	now := time.Now().UTC()
	archive := &mockArchive{
		sessions: []*Session{
			{
				ID:        "active",
				StartedAt: timeAt(now, 0.1),
				EndedAt:   nil, // active — should be excluded
				Title:     "Live conversation",
			},
			{
				ID:        "closed",
				StartedAt: timeAt(now, 24),
				EndedAt:   ptrTime(timeAt(now, 23)),
				Title:     "Yesterday",
			},
		},
	}
	p := NewEpisodicProvider(archive, slog.Default(), EpisodicConfig{HistoryTokens: 4000})

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if strings.Contains(got, `"id":"active"`) {
		t.Errorf("active session leaked into Recent Sessions:\n%s", got)
	}
	if !strings.Contains(got, `"id":"closed"`) {
		t.Errorf("closed session missing:\n%s", got)
	}
}

func TestEpisodicGetContext_EmptySessionsSkipped(t *testing.T) {
	now := time.Now().UTC()
	archive := &mockArchive{
		sessions: []*Session{
			// Delegate session with no metadata or title — should be filtered.
			{
				ID:        "delegate",
				StartedAt: timeAt(now, 1),
				EndedAt:   ptrTime(timeAt(now, 0.5)),
			},
			{
				ID:        "real",
				StartedAt: timeAt(now, 2),
				EndedAt:   ptrTime(timeAt(now, 1.5)),
				Title:     "Real conversation",
			},
		},
	}
	p := NewEpisodicProvider(archive, slog.Default(), EpisodicConfig{HistoryTokens: 4000})

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if strings.Contains(got, `"id":"delegate"`) {
		t.Errorf("empty delegate session leaked:\n%s", got)
	}
	if !strings.Contains(got, `"id":"real"`) {
		t.Errorf("real session missing:\n%s", got)
	}
}

func TestEpisodicGetContext_ListSessionsErrorIsSilent(t *testing.T) {
	archive := &mockArchive{listErr: errors.New("boom")}
	p := NewEpisodicProvider(archive, slog.Default(), EpisodicConfig{HistoryTokens: 4000})

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext should not propagate archive error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty output on archive error, got %q", got)
	}
}

func TestEpisodicGetContext_ByteCapTruncates(t *testing.T) {
	now := time.Now().UTC()
	// Synthesize many large sessions so the byte cap clamps the output.
	huge := strings.Repeat("x", 1000)
	archive := &mockArchive{}
	for i := range 30 {
		archive.sessions = append(archive.sessions, &Session{
			ID:        sessionID(i),
			StartedAt: timeAt(now, float64(i+1)),
			EndedAt:   ptrTime(timeAt(now, float64(i))),
			Title:     "session " + sessionID(i),
			Summary:   huge,
		})
	}
	// Tight token budget → tight byte cap (×4) → truncated catalog.
	p := NewEpisodicProvider(archive, slog.Default(), EpisodicConfig{HistoryTokens: 100})

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	jsonBlock := extractFirstFencedJSON(got)
	var parsed struct {
		Sessions  []SessionView `json:"sessions"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(jsonBlock), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !parsed.Truncated {
		t.Error("expected truncated=true under tight byte cap")
	}
	if len(parsed.Sessions) >= 30 {
		t.Errorf("expected catalog clipped, got %d entries (no clip)", len(parsed.Sessions))
	}
}

func TestSessionHasContent(t *testing.T) {
	tests := []struct {
		name    string
		session *Session
		want    bool
	}{
		{name: "title only", session: &Session{Title: "x"}, want: true},
		{name: "summary only", session: &Session{Summary: "x"}, want: true},
		{name: "metadata one_liner", session: &Session{Metadata: &SessionMetadata{OneLiner: "x"}}, want: true},
		{name: "metadata paragraph", session: &Session{Metadata: &SessionMetadata{Paragraph: "x"}}, want: true},
		{name: "metadata detailed", session: &Session{Metadata: &SessionMetadata{Detailed: "x"}}, want: true},
		{name: "explicit empty type", session: &Session{Title: "x", Metadata: &SessionMetadata{SessionType: "empty"}}, want: false},
		{name: "no content", session: &Session{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionHasContent(tt.session); got != tt.want {
				t.Errorf("sessionHasContent = %v, want %v", got, tt.want)
			}
		})
	}
}

// extractFirstFencedJSON returns the contents of the first ```json
// block in s, or empty string if none.
func extractFirstFencedJSON(s string) string {
	start := strings.Index(s, "```json\n")
	if start < 0 {
		return ""
	}
	s = s[start+len("```json\n"):]
	end := strings.Index(s, "\n```")
	if end < 0 {
		return ""
	}
	return s[:end]
}

// sessionID returns "s0", "s1", ... for test fixture IDs.
func sessionID(i int) string {
	return "s" + itoaInt(i)
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
