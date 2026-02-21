package email

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/opstate"
)

func testOpstate(t *testing.T) *opstate.Store {
	t.Helper()
	s, err := opstate.NewStore(filepath.Join(t.TempDir(), "opstate_test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFormatPollSection_Single(t *testing.T) {
	messages := []Envelope{
		{
			UID:     100,
			From:    "Jane Doe <jane@example.com>",
			Subject: "Re: Project update",
			Date:    time.Date(2026, 2, 20, 16, 30, 0, 0, time.UTC),
		},
	}

	result := formatPollSection("personal", messages)

	if !strings.Contains(result, "Account: personal (INBOX)") {
		t.Error("should contain account header")
	}
	if !strings.Contains(result, "From: Jane Doe <jane@example.com>") {
		t.Error("should contain sender")
	}
	if !strings.Contains(result, "Subject: Re: Project update") {
		t.Error("should contain subject")
	}
	if !strings.Contains(result, "Date: 2026-02-20 16:30") {
		t.Error("should contain date")
	}
}

func TestFormatPollSection_Multiple(t *testing.T) {
	messages := []Envelope{
		{
			UID:     101,
			From:    "alice@example.com",
			Subject: "Hello",
			Date:    time.Date(2026, 2, 20, 17, 0, 0, 0, time.UTC),
		},
		{
			UID:     100,
			From:    "bob@example.com",
			Subject: "Meeting",
			Date:    time.Date(2026, 2, 20, 16, 0, 0, 0, time.UTC),
		},
	}

	result := formatPollSection("work", messages)

	if !strings.Contains(result, "Account: work (INBOX)") {
		t.Error("should contain account header")
	}
	if !strings.Contains(result, "alice@example.com") {
		t.Error("should contain first sender")
	}
	if !strings.Contains(result, "bob@example.com") {
		t.Error("should contain second sender")
	}
}

func TestPollerHighWaterMark_FirstRunSeeds(t *testing.T) {
	state := testOpstate(t)

	// Verify no stored value initially.
	val, err := state.Get(pollNamespace, "test:INBOX")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty initial state, got %q", val)
	}

	// Simulate what checkAccount does on first run: seed without reporting.
	if err := state.Set(pollNamespace, "test:INBOX", "500"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err = state.Get(pollNamespace, "test:INBOX")
	if err != nil {
		t.Fatalf("Get after seed: %v", err)
	}
	if val != "500" {
		t.Errorf("stored value = %q, want %q", val, "500")
	}
}

func TestPollerHighWaterMark_UpdateOnNewMessages(t *testing.T) {
	state := testOpstate(t)

	// Seed initial high-water mark.
	if err := state.Set(pollNamespace, "test:INBOX", "100"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Simulate new messages arriving (highest UID = 105).
	if err := state.Set(pollNamespace, "test:INBOX", "105"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := state.Get(pollNamespace, "test:INBOX")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "105" {
		t.Errorf("stored value = %q, want %q", val, "105")
	}
}

func TestPollerHighWaterMark_NamespaceIsolation(t *testing.T) {
	state := testOpstate(t)

	if err := state.Set(pollNamespace, "personal:INBOX", "200"); err != nil {
		t.Fatalf("Set personal: %v", err)
	}
	if err := state.Set(pollNamespace, "work:INBOX", "300"); err != nil {
		t.Fatalf("Set work: %v", err)
	}

	personal, err := state.Get(pollNamespace, "personal:INBOX")
	if err != nil {
		t.Fatalf("Get personal: %v", err)
	}
	work, err := state.Get(pollNamespace, "work:INBOX")
	if err != nil {
		t.Fatalf("Get work: %v", err)
	}

	if personal != "200" {
		t.Errorf("personal = %q, want %q", personal, "200")
	}
	if work != "300" {
		t.Errorf("work = %q, want %q", work, "300")
	}
}

func TestNewPoller(t *testing.T) {
	state := testOpstate(t)
	// NewPoller with nil manager is valid — it just won't check anything.
	// This tests that the constructor doesn't panic.
	p := NewPoller(nil, state, nil)
	if p == nil {
		t.Error("NewPoller returned nil")
	}
}

func TestAdvanceHighWaterMark_Increases(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)

	if err := state.Set(pollNamespace, "test:INBOX", "100"); err != nil {
		t.Fatal(err)
	}

	p.advanceHighWaterMark("test", "test:INBOX", 100, []Envelope{
		{UID: 105},
		{UID: 103},
	})

	val, _ := state.Get(pollNamespace, "test:INBOX")
	if val != "105" {
		t.Errorf("high-water mark = %q, want %q", val, "105")
	}
}

func TestAdvanceHighWaterMark_NeverDecreases(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)

	if err := state.Set(pollNamespace, "test:INBOX", "391"); err != nil {
		t.Fatal(err)
	}

	// Simulate messages with lower UIDs (e.g., after moves/deletes
	// changed what's in INBOX).
	p.advanceHighWaterMark("test", "test:INBOX", 391, []Envelope{
		{UID: 286},
		{UID: 200},
	})

	val, _ := state.Get(pollNamespace, "test:INBOX")
	if val != "391" {
		t.Errorf("high-water mark should not decrease: got %q, want %q", val, "391")
	}
}

func TestAdvanceHighWaterMark_EmptyMessages(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)

	if err := state.Set(pollNamespace, "test:INBOX", "100"); err != nil {
		t.Fatal(err)
	}

	// Empty message list should not change the mark.
	p.advanceHighWaterMark("test", "test:INBOX", 100, nil)

	val, _ := state.Get(pollNamespace, "test:INBOX")
	if val != "100" {
		t.Errorf("high-water mark should not change with empty messages: got %q, want %q", val, "100")
	}
}

func TestFilterSelfSent(t *testing.T) {
	// Create a minimal manager with a configured account for testing.
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name:        "work",
				IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: "user"},
				SMTP:        SMTPConfig{Host: "smtp.test.com", Port: 587, Username: "user", Password: "pass"},
				DefaultFrom: "Thane Agent <thane@example.com>",
			},
		},
	}
	mgr := NewManager(cfg, slog.Default())

	p := NewPoller(mgr, nil, nil)

	messages := []Envelope{
		{UID: 105, From: "alice@example.com", Subject: "Hello"},
		{UID: 106, From: "Thane Agent <thane@example.com>", Subject: "Re: Hello"},
		{UID: 107, From: "bob@example.com", Subject: "Meeting"},
		{UID: 108, From: "thane@example.com", Subject: "Re: Meeting"},
	}

	filtered := p.filterSelfSent("work", messages)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 messages after filtering, got %d", len(filtered))
	}
	if filtered[0].UID != 105 {
		t.Errorf("first message UID = %d, want 105", filtered[0].UID)
	}
	if filtered[1].UID != 107 {
		t.Errorf("second message UID = %d, want 107", filtered[1].UID)
	}
}

func TestFilterSelfSent_NoDefaultFrom(t *testing.T) {
	// When DefaultFrom is empty (no SMTP configured), all messages pass through.
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name: "readonly",
				IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "user"},
				// No SMTP, no DefaultFrom.
			},
		},
	}
	mgr := NewManager(cfg, slog.Default())

	p := NewPoller(mgr, nil, nil)

	messages := []Envelope{
		{UID: 100, From: "anyone@example.com"},
	}

	filtered := p.filterSelfSent("readonly", messages)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 message (no filtering without DefaultFrom), got %d", len(filtered))
	}
}
