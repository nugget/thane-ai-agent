package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

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
// when wired it formats one line per finding up to the limit the row
// asks for while counting every finding.
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
	lines, total, err := src(context.Background(), 5)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if total != 1 || len(lines) != 1 || lines[0] != "Forge Notices (admin, noreply@forge.example: no-reply)" {
		t.Errorf("lines = %q, total = %d", lines, total)
	}

	for i := range 6 {
		seedDirectoryRecord(t, store, fmt.Sprintf("Notices %02d", i), contacts.ZoneTrusted, fmt.Sprintf("noreply@n%02d.example", i))
	}
	lines, total, err = src(context.Background(), 2)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if total != 7 || len(lines) != 2 || lines[0] != "Forge Notices (admin, noreply@forge.example: no-reply)" {
		t.Errorf("limit 2: lines = %q, total = %d; want two lines and a total of 7", lines, total)
	}
}

// TestContactDirectoryFindingsClipPerField pins that an oversized name
// or address is clipped field by field, so each line still carries the
// zone, the pattern, and (for a long name) the whole address, which is
// what the operator's remedy needs to pick out the automated EMAIL
// value on a record holding several.
func TestContactDirectoryFindingsClipPerField(t *testing.T) {
	// 300 bytes of a two- and a three-byte rune, so a byte cut that
	// ignored rune boundaries would leave invalid UTF-8.
	hugeName := strings.Repeat("é€", 60)
	hugeAddress := "noreply+" + strings.Repeat("x", 300) + "@forge.example"
	tests := []struct {
		name, record, address string
		want                  []string
	}{
		{
			name:    "long name keeps zone, address and pattern",
			record:  hugeName,
			address: "noreply@forge.example",
			want:    []string{"(admin, noreply@forge.example: no-reply)", "…"},
		},
		{
			name:    "long address keeps name, zone and pattern",
			record:  "Forge Notices",
			address: hugeAddress,
			want:    []string{"Forge Notices (admin, noreply+x", "…: no-reply)"},
		},
		{
			name:    "short fields are not clipped",
			record:  "Forge Notices",
			address: "noreply@forge.example",
			want:    []string{"Forge Notices (admin, noreply@forge.example: no-reply)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newEmailIdentityStore(t)
			seedDirectoryRecord(t, store, tt.record, contacts.ZoneAdmin, tt.address)
			lines, total, err := contactDirectoryFindings(context.Background(), store, 5)
			if err != nil {
				t.Fatalf("contactDirectoryFindings: %v", err)
			}
			if total != 1 || len(lines) != 1 {
				t.Fatalf("lines = %q, total = %d; want one", lines, total)
			}
			line := lines[0]
			for _, w := range tt.want {
				if !strings.Contains(line, w) {
					t.Errorf("line = %q, want it to contain %q", line, w)
				}
			}
			if !utf8.ValidString(line) {
				t.Errorf("line = %q is not valid UTF-8", line)
			}
			// Both clipped fields plus the longest zone and pattern
			// stay under the health row's 256-byte backstop.
			if len(line) > 2*maxDirectoryFieldBytes+len(" (household, : notifications)") {
				t.Errorf("line is %d bytes: %q", len(line), line)
			}
		})
	}
}

// TestLogContactDirectoryFindingsClipsFields pins that the boot Warn
// clips the record name and the address the way the health row does, so
// one oversized directory value cannot inflate the startup log.
func TestLogContactDirectoryFindingsClipsFields(t *testing.T) {
	store := newEmailIdentityStore(t)
	// Two- and three-byte runes, so a cut that ignored rune boundaries
	// would leave invalid UTF-8.
	hugeName := strings.Repeat("é€", 60)
	hugeAddress := "noreply+" + strings.Repeat("x", 300) + "@forge.example"
	seedDirectoryRecord(t, store, hugeName, contacts.ZoneAdmin, hugeAddress)

	capture := &auditLogCapture{}
	logContactDirectoryFindings(context.Background(), store, slog.New(capture))
	warns := capture.warns()
	if len(warns) != 1 {
		t.Fatalf("warns = %+v, want exactly one", warns)
	}
	got := warns[0].attrs
	for _, key := range []string{"contact_name", "address"} {
		v := got[key]
		if len(v) > maxDirectoryFieldBytes || !utf8.ValidString(v) || !strings.HasSuffix(v, "…") {
			t.Errorf("%s = %d bytes, valid UTF-8 %v; want a clipped value of at most %d bytes ending in …", key, len(v), utf8.ValidString(v), maxDirectoryFieldBytes)
		}
	}
	if got["pattern"] != contacts.AutomatedNoReply || got["trust_zone"] != contacts.ZoneAdmin {
		t.Errorf("clipping must leave the pattern and zone intact, got %+v", got)
	}
}
