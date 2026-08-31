package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

func newContactIdentityTestStore(t *testing.T) *contacts.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestApplyContactIdentityConfigVerifiedAppliesAuthority(t *testing.T) {
	store := newContactIdentityTestStore(t)
	operator, err := store.Upsert(&contacts.Contact{FormattedName: "Operator", TrustZone: contacts.ZoneAdmin})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Identity.OperatorContactID = operator.ID.String()
	cfg.Person.ContactBindings = map[string]string{operator.ID.String(): "person.operator"}

	resolved, err := applyContactIdentityConfig(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.operatorContactID != operator.ID || !resolved.configOwnsHAPersonBindings {
		t.Fatalf("resolved config = %+v", resolved)
	}
	if entity, exists, err := store.HAPersonEntity(operator.ID); err != nil || !exists || entity != "person.operator" {
		t.Fatalf("configured binding = %q, %v, %v", entity, exists, err)
	}
}

func TestApplyContactIdentityConfigUnverifiedCannotChangeAuthority(t *testing.T) {
	store := newContactIdentityTestStore(t)
	persisted, err := store.Upsert(&contacts.Contact{FormattedName: "Persisted Operator", TrustZone: contacts.ZoneAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetHAPersonEntity(persisted.ID, "person.persisted"); err != nil {
		t.Fatal(err)
	}
	selected, err := store.Upsert(&contacts.Contact{FormattedName: "Unsigned Selection"})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Identity.OperatorContactID = selected.ID.String()
	cfg.Person.ContactBindings = map[string]string{selected.ID.String(): "person.unsigned"}
	cfg.MarkUnverified()

	resolved, err := applyContactIdentityConfig(cfg, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.operatorContactID != uuid.Nil || resolved.legacyOwnerContactName != "" || resolved.configOwnsHAPersonBindings {
		t.Fatalf("unverified config resolved authority: %+v", resolved)
	}
	if entity, exists, err := store.HAPersonEntity(persisted.ID); err != nil || !exists || entity != "person.persisted" {
		t.Fatalf("persisted binding changed = %q, %v, %v", entity, exists, err)
	}
	if entity, exists, err := store.HAPersonEntity(selected.ID); err != nil || !exists || entity != "" {
		t.Fatalf("unsigned binding applied = %q, %v, %v", entity, exists, err)
	}

	legacy := config.Default()
	legacy.Identity.OwnerContactName = selected.FormattedName
	legacy.MarkUnverified()
	resolved, err = applyContactIdentityConfig(legacy, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.legacyOwnerContactName != "" {
		t.Fatalf("unverified legacy owner selector applied: %+v", resolved)
	}
}
