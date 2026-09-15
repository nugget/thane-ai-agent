package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/introspection"
)

// seedForkPair adds the production nickname shape: a household record,
// bound to a Home Assistant person, that goes by "Carol", and a known
// record named "Carol".
func seedForkPair(t *testing.T, store *contacts.Store) (household, known *contacts.Contact) {
	t.Helper()
	household, err := store.Upsert(&contacts.Contact{FormattedName: "Carol Household", Nickname: "Carol", TrustZone: contacts.ZoneHousehold})
	if err != nil {
		t.Fatalf("Upsert household: %v", err)
	}
	if err := store.SetHAPersonEntity(household.ID, "person.carol"); err != nil {
		t.Fatalf("SetHAPersonEntity: %v", err)
	}
	known, err = store.Upsert(&contacts.Contact{FormattedName: "Carol", TrustZone: contacts.ZoneKnown})
	if err != nil {
		t.Fatalf("Upsert known: %v", err)
	}
	return household, known
}

func directoryRowOf(snap introspection.HealthSnapshot) *introspection.HealthRow {
	for i := range snap.Annunciator {
		if snap.Annunciator[i].Name == "contact_directory" {
			return &snap.Annunciator[i]
		}
	}
	return nil
}

// TestContactDirectoryAudits_ForksIgnoreTheEmailGate pins the wiring
// #1545 asks for: fork findings reach the contact_directory row and the
// boot Warns with email polling off, while the automated-address
// findings keep their email-polling gate, and both say what to do.
func TestContactDirectoryAudits_ForksIgnoreTheEmailGate(t *testing.T) {
	emailCfg := &config.Config{Email: config.EmailConfig{Accounts: []config.EmailAccountConfig{{
		Name: "primary",
		IMAP: config.EmailIMAPConfig{Host: "imap.example.com", Username: "thane@example.com"},
	}}}}
	for _, tc := range []struct {
		name          string
		cfg           *config.Config
		wantAutomated bool
	}{
		{name: "email polling off", cfg: &config.Config{}, wantAutomated: false},
		{name: "email polling on", cfg: emailCfg, wantAutomated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newEmailIdentityStore(t)
			household, known := seedForkPair(t, store)
			seedDirectoryRecord(t, store, "Forge Notices", contacts.ZoneAdmin, "noreply@forge-notices.net")
			wantLine := fmt.Sprintf(`name "carol" likely one person: Carol Household (household, %s, HA person) and Carol (known, %s)`, household.ID, known.ID)

			capture := &auditLogCapture{}
			logContactDirectoryAudits(ctx, tc.cfg, store, slog.New(capture))
			var automated, forks int
			for _, w := range capture.warns() {
				switch {
				case strings.Contains(w.msg, "automated-looking"):
					automated++
				case w.attrs["kind"] == contacts.ForkKindName:
					forks++
					if w.attrs["key"] != "carol" || !strings.Contains(w.attrs["members"], household.ID.String()) || !strings.Contains(w.attrs["members"], known.ID.String()) {
						t.Errorf("fork Warn = %+v, want key carol naming both records", w.attrs)
					}
					for _, want := range []string{"merge the duplicate", "rename one", "CardDAV", "/v1/contacts", "by contact_id", "operator's own message"} {
						if !strings.Contains(w.attrs["remedy"], want) {
							t.Errorf("fork remedy = %q, want %q", w.attrs["remedy"], want)
						}
					}
					if strings.Contains(w.attrs["remedy"], "ask Thane") {
						t.Errorf("fork remedy = %q, must not offer Thane a fix no tool makes", w.attrs["remedy"])
					}
				default:
					t.Errorf("unexpected Warn: %s %+v", w.msg, w.attrs)
				}
			}
			if forks != 1 {
				t.Errorf("fork Warns = %d, want 1", forks)
			}
			if (automated == 1) != tc.wantAutomated {
				t.Errorf("automated Warns = %d, want present=%v", automated, tc.wantAutomated)
			}

			var src introspection.HealthSources
			wireContactDirectoryHealth(&src, tc.cfg, store)
			snap := introspection.NewInspector(src).Health(ctx)
			row := directoryRowOf(snap)
			if row == nil || row.Status != introspection.HealthDegraded {
				t.Fatalf("contact_directory row = %+v, want degraded", row)
			}
			for _, want := range []string{wantLine, "merge the duplicate into the record that should keep the name", "CardDAV", "/v1/contacts", "operator's own message"} {
				if !strings.Contains(row.Detail, want) {
					t.Errorf("detail = %q, want %q", row.Detail, want)
				}
			}
			if strings.Contains(row.Detail, "automated-looking") != tc.wantAutomated {
				t.Errorf("detail = %q, want the automated-address part present=%v", row.Detail, tc.wantAutomated)
			}
		})
	}

	t.Run("no store wires nothing", func(t *testing.T) {
		if src := contactForkFindingsSource(nil); src != nil {
			t.Error("without a contact store the fork source must stay unwired")
		}
		capture := &auditLogCapture{}
		logContactDirectoryAudits(context.Background(), emailCfg, nil, slog.New(capture))
		if warns := capture.warns(); len(warns) != 0 {
			t.Errorf("warns = %+v, want none without a store", warns)
		}
	})
}

// TestLogContactForkFindings pins the fork audit's boot Warns: a
// placeholder address gets its own message and remedy, and more findings
// than the bound give 20 lines and one summary.
func TestLogContactForkFindings(t *testing.T) {
	ctx := context.Background()

	t.Run("a placeholder address names the record and the fix", func(t *testing.T) {
		store := newEmailIdentityStore(t)
		mallory := seedDirectoryRecord(t, store, "Mallory Admin", contacts.ZoneAdmin, "mallory@example.com")
		capture := &auditLogCapture{}
		logContactForkFindings(ctx, store, slog.New(capture))
		warns := capture.warns()
		if len(warns) != 1 || !strings.Contains(warns[0].msg, "placeholder email address") {
			t.Fatalf("warns = %+v, want one placeholder Warn", warns)
		}
		got := warns[0].attrs
		if got["contact_id"] != mallory.ID.String() || got["address"] != "mallory@example.com" || got["trust_zone"] != contacts.ZoneAdmin ||
			!strings.Contains(got["remedy"], "replace the placeholder") || !strings.Contains(got["remedy"], "no model-facing tool removes or replaces an address") ||
			strings.Contains(got["remedy"], "ask Thane") {
			t.Errorf("placeholder Warn = %+v", got)
		}
	})

	t.Run("each kind gets its own message and remedy", func(t *testing.T) {
		store := newEmailIdentityStore(t)
		for _, seed := range []struct{ name, tel string }{
			{"Jane Rivera", "+15551110005"},
			{"Mary Smith", "+15551110006"},
		} {
			c := &contacts.Contact{FormattedName: seed.name, Nickname: "Mom", TrustZone: contacts.ZoneTrusted}
			if _, err := store.UpsertWithProperties(c, []contacts.Property{{Property: "TEL", Value: seed.tel}}); err != nil {
				t.Fatal(err)
			}
		}
		seedDirectoryRecord(t, store, "Ivan Household", contacts.ZoneHousehold, "ivan@ivanmail.net")
		seedDirectoryRecord(t, store, "Judy Known", contacts.ZoneKnown, "ivan@ivanmail.net")
		capture := &auditLogCapture{}
		logContactForkFindings(ctx, store, slog.New(capture))
		byKind := map[string]auditLogRecord{}
		for _, w := range capture.warns() {
			byKind[w.attrs["kind"]] = w
		}
		shared, email := byKind[contacts.ForkKindSharedName], byKind[contacts.ForkKindEmail]
		if !strings.Contains(shared.msg, "different contact records answer to one name") || !strings.Contains(shared.attrs["remedy"], "distinct name or nickname") {
			t.Errorf("shared_name Warn = %+v", shared)
		}
		if strings.Contains(shared.attrs["remedy"], "merge the duplicate") {
			t.Errorf("shared_name remedy = %q, must not frame different people as a duplicate", shared.attrs["remedy"])
		}
		if !strings.Contains(email.attrs["remedy"], "move the address") || strings.Contains(email.attrs["remedy"], "rename") {
			t.Errorf("email remedy = %q, want the address fix and no rename", email.attrs["remedy"])
		}
		lines, _, err := contactForkFindings(ctx, store, 5)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(lines, "\n"), `name "mom" answered to by different people: Jane Rivera (trusted,`) {
			t.Errorf("lines = %q, want the shared-name line", lines)
		}
	})

	t.Run("more findings than the bound give 20 lines and one summary", func(t *testing.T) {
		store := newEmailIdentityStore(t)
		for i := range 25 {
			if _, err := store.Upsert(&contacts.Contact{FormattedName: fmt.Sprintf("Person %02d Household", i), Nickname: fmt.Sprintf("P%02d", i), TrustZone: contacts.ZoneHousehold}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Upsert(&contacts.Contact{FormattedName: fmt.Sprintf("P%02d", i), TrustZone: contacts.ZoneKnown}); err != nil {
				t.Fatal(err)
			}
		}
		capture := &auditLogCapture{}
		logContactForkFindings(ctx, store, slog.New(capture))
		warns := capture.warns()
		if len(warns) != maxContactDirectoryWarnings+1 {
			t.Fatalf("warns = %d, want %d findings plus one summary", len(warns), maxContactDirectoryWarnings)
		}
		summary := warns[maxContactDirectoryWarnings]
		if summary.attrs["total"] != "25" || summary.attrs["logged"] != fmt.Sprint(maxContactDirectoryWarnings) {
			t.Errorf("summary = %+v, want total 25 and logged %d", summary, maxContactDirectoryWarnings)
		}
	})
}

// TestContactForkRemedy_NameWarnsOfTheOnlyExactHolder pins that the
// name remedy says what forgetting the one record that holds a name
// exactly does to it: the name falls to the one record whose given name
// or first word it is, so the record that keeps it needs the nickname.
func TestContactForkRemedy_NameWarnsOfTheOnlyExactHolder(t *testing.T) {
	remedy := contactForkRemedy(contacts.ForkKindName)
	for _, want := range []string{"Forgetting the only record whose formatted name or nickname is the name", "given name or first word", "as a nickname"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("name remedy = %q, want %q", remedy, want)
		}
	}
}

// TestContactForkFindingsClip pins that fork lines clip every free-text
// field on a rune boundary, so a line naming three records with their
// UUIDs stays valid UTF-8 and inside the health row's per-line backstop.
func TestContactForkFindingsClip(t *testing.T) {
	store := newEmailIdentityStore(t)
	huge := strings.Repeat("é€", 60)
	if _, err := store.Upsert(&contacts.Contact{FormattedName: huge + " Household", Nickname: huge, TrustZone: contacts.ZoneHousehold}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := store.Upsert(&contacts.Contact{FormattedName: fmt.Sprintf("%s %d", huge, i), Nickname: huge, TrustZone: contacts.ZoneKnown}); err != nil {
			t.Fatal(err)
		}
	}
	lines, total, err := contactForkFindings(context.Background(), store, 5)
	if err != nil {
		t.Fatalf("contactForkFindings: %v", err)
	}
	if total != 1 || len(lines) != 1 {
		t.Fatalf("lines = %q, total = %d; want one", lines, total)
	}
	line := lines[0]
	if !utf8.ValidString(line) || !strings.Contains(line, "…") || !strings.Contains(line, "and 1 more") {
		t.Errorf("line = %q, want valid UTF-8, clipped fields and the fourth record counted", line)
	}
	if len(line) > 512 {
		t.Errorf("line is %d bytes, want at most the 512-byte backstop: %q", len(line), line)
	}
}
