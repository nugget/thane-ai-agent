package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/introspection"
)

// newContactsDocumentStore opens a document store with a plain contacts
// root, the root dossiers live in, and returns its directory.
func newContactsDocumentStore(t *testing.T) (*documents.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), contacts.DossierRootName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := documents.NewStore(db, map[string]string{contacts.DossierRootName: dir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	return store, dir
}

func writeDossierFile(t *testing.T, dir string, id uuid.UUID) {
	t.Helper()
	body := "---\ntitle: Dossier\n---\n\n# Dossier\n\nExisting synthesis.\n"
	if err := os.WriteFile(filepath.Join(dir, id.String()+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write dossier: %v", err)
	}
}

// TestDocumentPresence pins the adapter the second-dossier refusal reads
// through: an absent document is (false, nil), a written one is (true,
// nil), a failure that is not "not found" is an error rather than a
// guess, and no store means no check.
func TestDocumentPresence(t *testing.T) {
	ctx := context.Background()
	store, dir := newContactsDocumentStore(t)
	presence := documentPresence(store)
	id := uuid.Must(uuid.NewV7())
	ref := contacts.DossierRef(id)

	if exists, err := presence(ctx, ref); err != nil || exists {
		t.Fatalf("absent dossier = %v, %v; want false, nil", exists, err)
	}
	writeDossierFile(t, dir, id)
	if exists, err := presence(ctx, ref); err != nil || !exists {
		t.Fatalf("written dossier = %v, %v; want true, nil", exists, err)
	}
	if exists, err := presence(ctx, "no_such_root:"+id.String()+".md"); err == nil || exists {
		t.Errorf("unknown root = %v, %v; want an error", exists, err)
	}
	if documentPresence(nil) != nil {
		t.Error("without a document store the presence check must stay unwired")
	}
}

// TestConfigureContactDossierDocuments_WiresTheRefusal pins the
// production wiring end to end: contact tools configured the way
// initChannels configures them refuse a second dossier against a real
// document store, and without document tools nothing is configured.
func TestConfigureContactDossierDocuments_WiresTheRefusal(t *testing.T) {
	ctx := context.Background()
	contactStore := newEmailIdentityStore(t)
	household, known := seedForkPair(t, contactStore)
	docStore, dir := newContactsDocumentStore(t)
	writeDossierFile(t, dir, household.ID)

	tools := contacts.NewTools(contactStore, nil)
	tools.ConfigureDossierRoot(true, true)
	configureContactDossierDocuments(tools, documents.NewTools(docStore), docStore)
	_, err := tools.WriteDossier(ctx, contacts.DossierWriteArgs{ContactID: known.ID.String()})
	if err == nil || !strings.Contains(err.Error(), "refused to start a second dossier") || !strings.Contains(err.Error(), household.ID.String()) {
		t.Fatalf("WriteDossier() = %v, want the second-dossier refusal naming %s", err, household.ID)
	}

	unwired := contacts.NewTools(contactStore, nil)
	unwired.ConfigureDossierRoot(true, true)
	configureContactDossierDocuments(unwired, nil, docStore)
	_, err = unwired.WriteDossier(ctx, contacts.DossierWriteArgs{ContactID: known.ID.String()})
	if err == nil || !strings.Contains(err.Error(), "document tools are not configured") {
		t.Errorf("without document tools WriteDossier() = %v, want the unconfigured error", err)
	}
}

// TestWireContactDirectoryHealth pins initInspector's wiring of the
// contact_directory row: the fork source whenever there is a store, the
// automated-address source only with email polling on, and nothing
// without a store.
func TestWireContactDirectoryHealth(t *testing.T) {
	store := newEmailIdentityStore(t)
	emailCfg := &config.Config{Email: config.EmailConfig{Accounts: []config.EmailAccountConfig{{
		Name: "primary",
		IMAP: config.EmailIMAPConfig{Host: "imap.example.com", Username: "thane@example.com"},
	}}}}
	for _, tc := range []struct {
		name                      string
		cfg                       *config.Config
		store                     *contacts.Store
		wantForks, wantAutomation bool
	}{
		{name: "email polling off", cfg: &config.Config{}, store: store, wantForks: true},
		{name: "email polling on", cfg: emailCfg, store: store, wantForks: true, wantAutomation: true},
		{name: "no store", cfg: emailCfg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var src introspection.HealthSources
			wireContactDirectoryHealth(&src, tc.cfg, tc.store)
			if (src.DirectoryForks != nil) != tc.wantForks || (src.DirectoryFindings != nil) != tc.wantAutomation {
				t.Errorf("forks wired = %v, automated wired = %v; want %v, %v",
					src.DirectoryForks != nil, src.DirectoryFindings != nil, tc.wantForks, tc.wantAutomation)
			}
		})
	}
}

// TestLogLegacyOperatorResolution pins the boot line that says which
// record the legacy owner contact name chose: authority first before any
// pin, so a household record that goes by the name as a nickname wins
// over a known record with that formatted name, and the line says so.
func TestLogLegacyOperatorResolution(t *testing.T) {
	store := newEmailIdentityStore(t)
	household, known := seedForkPair(t, store)
	_ = known

	t.Run("a nickname match above known is named", func(t *testing.T) {
		identity := contactIdentityConfig{legacyOwnerContactName: "Carol"}
		resolver := &contactChannelBindingResolver{store: store, legacyOwnerContactName: "Carol"}
		capture := &auditLogCapture{}
		operatorID, resolveErr := resolver.resolvedOperator()
		logLegacyOperatorResolution(slog.New(capture), store, identity, operatorID, resolveErr)
		var info *auditLogRecord
		for i := range capture.records {
			if capture.records[i].level == slog.LevelInfo {
				info = &capture.records[i]
			}
		}
		if info == nil {
			t.Fatalf("records = %+v, want one Info line", capture.records)
		}
		if info.attrs["contact_id"] != household.ID.String() || info.attrs["matched_by"] != "nickname" ||
			info.attrs["trust_zone"] != contacts.ZoneHousehold || !strings.Contains(info.attrs["remedy"], "identity.operator_contact_id") {
			t.Errorf("Info attrs = %+v, want the household record matched by nickname", info.attrs)
		}
	})

	t.Run("a name that matches nothing warns", func(t *testing.T) {
		capture := &auditLogCapture{}
		resolver := &contactChannelBindingResolver{store: store, legacyOwnerContactName: "Nobody"}
		operatorID, resolveErr := resolver.resolvedOperator()
		logLegacyOperatorResolution(slog.New(capture), store, contactIdentityConfig{legacyOwnerContactName: "Nobody"}, operatorID, resolveErr)
		if warns := capture.warns(); len(warns) != 1 || !strings.Contains(warns[0].msg, "matches no active contact") || warns[0].attrs["error"] != "" {
			t.Errorf("warns = %+v, want one no-match Warn with no resolver error", warns)
		}
	})

	t.Run("a first name several records share warns with the candidates", func(t *testing.T) {
		shared := newEmailIdentityStore(t)
		var ids []string
		for _, name := range []string{"Eve Alpha", "Eve Beta"} {
			c, err := shared.Upsert(&contacts.Contact{FormattedName: name, TrustZone: contacts.ZoneKnown})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, c.ID.String())
		}
		resolver := &contactChannelBindingResolver{store: shared, legacyOwnerContactName: "Eve"}
		capture := &auditLogCapture{}
		operatorID, resolveErr := resolver.resolvedOperator()
		logLegacyOperatorResolution(slog.New(capture), shared, contactIdentityConfig{legacyOwnerContactName: "Eve"}, operatorID, resolveErr)
		warns := capture.warns()
		if len(warns) != 1 || !strings.Contains(warns[0].msg, "identity custody refuses") || !strings.Contains(warns[0].attrs["error"], `ambiguous contact "Eve"`) ||
			!strings.Contains(warns[0].attrs["error"], ids[0]) || !strings.Contains(warns[0].attrs["error"], ids[1]) {
			t.Errorf("warns = %+v, want one Warn carrying the ambiguity and both ids", warns)
		}
	})

	t.Run("an exact tie at the same standing pins no operator and warns with both", func(t *testing.T) {
		tied := newEmailIdentityStore(t)
		var ids []string
		for _, name := range []string{"Alice Adams", "Alice Baker"} {
			c, err := tied.Upsert(&contacts.Contact{FormattedName: name, Nickname: "Boss", TrustZone: contacts.ZoneHousehold})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, c.ID.String())
		}
		resolver := &contactChannelBindingResolver{store: tied, legacyOwnerContactName: "Boss"}
		operatorID, resolveErr := resolver.resolvedOperator()
		if operatorID != uuid.Nil {
			t.Fatalf("resolvedOperator = %s, want none: a tie must not pick an operator", operatorID)
		}
		capture := &auditLogCapture{}
		logLegacyOperatorResolution(slog.New(capture), tied, contactIdentityConfig{legacyOwnerContactName: "Boss"}, operatorID, resolveErr)
		warns := capture.warns()
		if len(warns) != 1 || !strings.Contains(warns[0].msg, "identity custody refuses") || !strings.Contains(warns[0].attrs["error"], "at the same standing (above known) hold it exactly") ||
			!strings.Contains(warns[0].attrs["error"], ids[0]) || !strings.Contains(warns[0].attrs["error"], ids[1]) {
			t.Errorf("warns = %+v, want one Warn carrying the exact tie and both ids", warns)
		}
	})

	t.Run("a first word names the field it matched", func(t *testing.T) {
		single := newEmailIdentityStore(t)
		bob, err := single.Upsert(&contacts.Contact{FormattedName: "Bob Stone", TrustZone: contacts.ZoneKnown})
		if err != nil {
			t.Fatal(err)
		}
		resolver := &contactChannelBindingResolver{store: single, legacyOwnerContactName: "Bob"}
		capture := &auditLogCapture{}
		operatorID, resolveErr := resolver.resolvedOperator()
		logLegacyOperatorResolution(slog.New(capture), single, contactIdentityConfig{legacyOwnerContactName: "Bob"}, operatorID, resolveErr)
		var info *auditLogRecord
		for i := range capture.records {
			if capture.records[i].level == slog.LevelInfo {
				info = &capture.records[i]
			}
		}
		if info == nil || info.attrs["contact_id"] != bob.ID.String() || info.attrs["matched_by"] != contacts.NameFieldFormattedFirstWord {
			t.Errorf("Info = %+v, want Bob Stone matched by %s", info, contacts.NameFieldFormattedFirstWord)
		}
	})

	t.Run("a configured operator_contact_id logs nothing", func(t *testing.T) {
		capture := &auditLogCapture{}
		identity := contactIdentityConfig{operatorContactID: household.ID, legacyOwnerContactName: "Carol"}
		logLegacyOperatorResolution(slog.New(capture), store, identity, household.ID, nil)
		if len(capture.records) != 0 {
			t.Errorf("records = %+v, want none", capture.records)
		}
	})
}

// TestSharedNameWarningSaysATieReachesNone pins the shared_name boot
// Warn to resolution's band rule: a lookup reaches the member with the
// most standing, and none when two share it, as two household records
// nicknamed "Mom" do, so the Warn never tells the operator one of them
// still gets the notifications.
func TestSharedNameWarningSaysATieReachesNone(t *testing.T) {
	warn := contactForkWarningByKind[contacts.ForkKindSharedName]
	if !strings.Contains(warn, "reaches only the one with the most standing, or none of them when two or more share it") ||
		strings.Contains(warn, "reaches only one of them") {
		t.Errorf("shared_name Warn = %q, want the band rule", warn)
	}
}
