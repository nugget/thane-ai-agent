package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func newTestHistoryProvider(t *testing.T) (*HistoryProvider, *RecordStore) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}

	p := NewHistoryProvider(HistoryProviderConfig{
		Records: store,
		Window:  6 * time.Hour,
		Limit:   30,
		Logger:  slog.Default(),
	})
	return p, store
}

func TestHistoryProvider_Empty(t *testing.T) {
	p, _ := newTestHistoryProvider(t)

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for no notifications, got %q", got)
	}
}

func TestHistoryProvider_FireAndForget(t *testing.T) {
	p, store := newTestHistoryProvider(t)

	now := time.Now().UTC().Truncate(time.Second)
	p.nowFunc = func() time.Time { return now }

	if err := store.Log(&Record{
		RequestID: "ff-test-001",
		Recipient: "nugget",
		Channel:   "ha_push",
		Source:    "metacognitive",
		Title:     "Lock battery",
		Message:   "Front-door lock battery warning is active",
		CreatedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	if !strings.HasPrefix(got, "### Recent Notifications\n\n") {
		t.Errorf("expected header prefix, got %q", got[:min(len(got), 40)])
	}

	// Parse the JSON payload.
	jsonStr := strings.TrimPrefix(got, "### Recent Notifications\n\n")
	jsonStr = strings.TrimSuffix(jsonStr, "\n")

	var summaries []historySummary
	if err := json.Unmarshal([]byte(jsonStr), &summaries); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, jsonStr)
	}

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if s.Channel != "ha_push" {
		t.Errorf("Channel = %q, want %q", s.Channel, "ha_push")
	}
	if s.Recipient != "nugget" {
		t.Errorf("Recipient = %q, want %q", s.Recipient, "nugget")
	}
	if s.Source != "metacognitive" {
		t.Errorf("Source = %q, want %q", s.Source, "metacognitive")
	}
	if s.Kind != KindFireAndForget {
		t.Errorf("Kind = %q, want %q", s.Kind, KindFireAndForget)
	}
	if s.Sent != "-1800s" {
		t.Errorf("Sent = %q, want %q", s.Sent, "-1800s")
	}
	if s.Title != "Lock battery" {
		t.Errorf("Title = %q, want %q", s.Title, "Lock battery")
	}
	if s.Status != "" {
		t.Errorf("Status should be empty for fire-and-forget, got %q", s.Status)
	}
}

func TestHistoryProvider_ActionableWithHITL(t *testing.T) {
	p, store := newTestHistoryProvider(t)

	now := time.Now().UTC().Truncate(time.Second)
	p.nowFunc = func() time.Time { return now }

	r := &Record{
		RequestID: "act-test-001",
		Recipient: "nugget",
		Actions:   []Action{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
		CreatedAt: now.Add(-1 * time.Hour),
		ExpiresAt: now.Add(time.Hour),
		Channel:   "ha_push",
		Source:    "signal/+15125551234",
		Kind:      KindActionable,
		Title:     "Turn off lights?",
		Message:   "Living room lights have been on for 4 hours",
	}
	if err := store.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Respond("act-test-001", "yes"); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	jsonStr := strings.TrimPrefix(got, "### Recent Notifications\n\n")
	jsonStr = strings.TrimSuffix(jsonStr, "\n")

	var summaries []historySummary
	if err := json.Unmarshal([]byte(jsonStr), &summaries); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, jsonStr)
	}

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if s.Kind != KindActionable {
		t.Errorf("Kind = %q, want %q", s.Kind, KindActionable)
	}
	if s.Status != StatusResponded {
		t.Errorf("Status = %q, want %q", s.Status, StatusResponded)
	}
	if s.Response != "yes" {
		t.Errorf("Response = %q, want %q", s.Response, "yes")
	}
	if s.Source != "signal/+15125551234" {
		t.Errorf("Source = %q, want %q", s.Source, "signal/+15125551234")
	}
}

func TestHistoryProvider_WindowFilter(t *testing.T) {
	p, store := newTestHistoryProvider(t)

	now := time.Now().UTC().Truncate(time.Second)
	p.nowFunc = func() time.Time { return now }
	p.window = 1 * time.Hour

	// Notification from 2 hours ago — outside window.
	if err := store.Log(&Record{
		RequestID: "ff-old",
		Recipient: "nugget",
		Channel:   "ha_push",
		Source:    "agent",
		CreatedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for out-of-window notification, got %q", got)
	}
}

func TestHistoryProvider_NilRecords(t *testing.T) {
	p := NewHistoryProvider(HistoryProviderConfig{
		Records: nil,
	})
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for nil records, got %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello\u2026"},
		{"unicode", "héllo wörld", 5, "héllo\u2026"},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.input, tt.n)
			if got != tt.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
			}
		})
	}
}
