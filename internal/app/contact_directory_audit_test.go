package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// auditLogRecord is one captured log record, attributes flattened.
type auditLogRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// auditLogCapture is a slog.Handler that keeps every record it sees.
type auditLogCapture struct {
	mu      sync.Mutex
	records []auditLogRecord
}

func (h *auditLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *auditLogCapture) Handle(_ context.Context, r slog.Record) error {
	rec := auditLogRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec)
	return nil
}

func (h *auditLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *auditLogCapture) WithGroup(string) slog.Handler      { return h }

func (h *auditLogCapture) warns() []auditLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []auditLogRecord
	for _, r := range h.records {
		if r.level == slog.LevelWarn {
			out = append(out, r)
		}
	}
	return out
}

// seedDirectoryRecord adds one record at zone holding the given email.
func seedDirectoryRecord(t *testing.T, store *contacts.Store, name, zone, address string) *contacts.Contact {
	t.Helper()
	c, err := store.Upsert(&contacts.Contact{FormattedName: name, TrustZone: zone})
	if err != nil {
		t.Fatalf("Upsert %s: %v", name, err)
	}
	if err := store.AddProperty(c.ID, &contacts.Property{Property: "EMAIL", Value: address}); err != nil {
		t.Fatalf("AddProperty %s: %v", name, err)
	}
	return c
}

func TestLogContactDirectoryFindings(t *testing.T) {
	ctx := context.Background()

	t.Run("one misfiled record gives exactly one Warn", func(t *testing.T) {
		store := newEmailIdentityStore(t)
		forge := seedDirectoryRecord(t, store, "Forge Notices", contacts.ZoneAdmin, "noreply@forge.example")
		seedDirectoryRecord(t, store, "Carrier", contacts.ZoneKnown, "no-reply@carrier.example")
		seedDirectoryRecord(t, store, "Alice", contacts.ZoneHousehold, "alice@example.com")
		capture := &auditLogCapture{}
		logContactDirectoryFindings(ctx, store, slog.New(capture))

		warns := capture.warns()
		if len(warns) != 1 {
			t.Fatalf("warns = %+v, want exactly one", warns)
		}
		got := warns[0].attrs
		want := map[string]string{
			"contact_id":   forge.ID.String(),
			"contact_name": "Forge Notices",
			"trust_zone":   contacts.ZoneAdmin,
			"address":      "noreply@forge.example",
			"pattern":      contacts.AutomatedNoReply,
			"read_as":      contacts.ZoneKnown,
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("attr %s = %q, want %q", k, got[k], v)
			}
		}
		if !strings.Contains(got["remedy"], "X-THANE-TRUST-ZONE") || !strings.Contains(got["remedy"], "/v1/contacts/{id}") {
			t.Errorf("remedy = %q, want both custody paths", got["remedy"])
		}
	})

	t.Run("a clean directory gives no Warn", func(t *testing.T) {
		store := newEmailIdentityStore(t)
		seedDirectoryRecord(t, store, "Carrier", contacts.ZoneKnown, "no-reply@carrier.example")
		seedDirectoryRecord(t, store, "Alice", contacts.ZoneAdmin, "alice@example.com")
		capture := &auditLogCapture{}
		logContactDirectoryFindings(ctx, store, slog.New(capture))
		if warns := capture.warns(); len(warns) != 0 {
			t.Errorf("warns = %+v, want none", warns)
		}
	})

	t.Run("more findings than the bound give 20 lines and one summary", func(t *testing.T) {
		store := newEmailIdentityStore(t)
		for i := range 25 {
			seedDirectoryRecord(t, store, fmt.Sprintf("Notices %02d", i), contacts.ZoneTrusted, fmt.Sprintf("noreply@n%02d.example", i))
		}
		capture := &auditLogCapture{}
		logContactDirectoryFindings(ctx, store, slog.New(capture))
		warns := capture.warns()
		if len(warns) != maxContactDirectoryWarnings+1 {
			t.Fatalf("warns = %d, want %d findings plus one summary", len(warns), maxContactDirectoryWarnings)
		}
		for _, w := range warns[:maxContactDirectoryWarnings] {
			if w.attrs["contact_id"] == "" {
				t.Errorf("a finding line lacks contact_id: %+v", w)
			}
		}
		summary := warns[maxContactDirectoryWarnings]
		if summary.attrs["total"] != "25" || summary.attrs["logged"] != fmt.Sprint(maxContactDirectoryWarnings) || summary.attrs["contact_id"] != "" {
			t.Errorf("summary = %+v, want total 25 and logged %d", summary, maxContactDirectoryWarnings)
		}
	})

	t.Run("a store failure logs one Warn and returns", func(t *testing.T) {
		db, err := database.Open(t.TempDir() + "/contacts.db")
		if err != nil {
			t.Fatalf("database.Open: %v", err)
		}
		store, err := contacts.NewStore(db, slog.Default())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		capture := &auditLogCapture{}
		logContactDirectoryFindings(ctx, store, slog.New(capture))
		warns := capture.warns()
		if len(warns) != 1 || !strings.Contains(warns[0].msg, "audit failed") || warns[0].attrs["error"] == "" {
			t.Errorf("warns = %+v, want one audit-failed Warn carrying the error", warns)
		}
	})
}

// TestContactDirectoryFindingsSource pins the health wiring: the row
// exists only with a contact store and email polling on (it follows the
// poller, though the send gate caps whenever email is configured), and
// when wired it formats one line per finding.
func TestContactDirectoryFindingsSource(t *testing.T) {
	emailCfg := &config.Config{Email: config.EmailConfig{Accounts: []config.EmailAccountConfig{{
		Name: "primary",
		IMAP: config.EmailIMAPConfig{Host: "imap.example.com", Username: "thane@example.com"},
	}}}}
	if !emailServicesEnabled(emailCfg) {
		t.Fatal("test config must enable email services")
	}
	store := newEmailIdentityStore(t)
	seedDirectoryRecord(t, store, "Forge Notices", contacts.ZoneAdmin, "noreply@forge.example")

	if src := contactDirectoryFindingsSource(&config.Config{}, store); src != nil {
		t.Error("with email disabled the contact_directory source must stay unwired")
	}
	if src := contactDirectoryFindingsSource(emailCfg, nil); src != nil {
		t.Error("without a contact store the contact_directory source must stay unwired")
	}
	src := contactDirectoryFindingsSource(emailCfg, store)
	if src == nil {
		t.Fatal("with email and a store the source must be wired")
	}
	lines, err := src(context.Background())
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if len(lines) != 1 || lines[0] != "Forge Notices (admin, noreply@forge.example: no-reply)" {
		t.Errorf("lines = %q", lines)
	}
}
