package email

import (
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
