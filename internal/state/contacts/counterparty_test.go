package contacts

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newCounterpartyTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestHAPersonEntityBinding(t *testing.T) {
	store := newCounterpartyTestStore(t)
	contact, err := store.Upsert(&Contact{FormattedName: "Alice Operator", TrustZone: ZoneAdmin})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Unbound reads as empty, present.
	entity, ok, err := store.HAPersonEntity(contact.ID)
	if err != nil || !ok || entity != "" {
		t.Fatalf("fresh contact binding = %q ok=%v err=%v", entity, ok, err)
	}

	if err := store.SetHAPersonEntity(contact.ID, "person.alice"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	entity, ok, err = store.HAPersonEntity(contact.ID)
	if err != nil || !ok || entity != "person.alice" {
		t.Fatalf("bound = %q ok=%v err=%v", entity, ok, err)
	}

	found, err := store.FindByHAPersonEntity("person.alice")
	if err != nil || found == nil || found.ID != contact.ID {
		t.Fatalf("reverse lookup = %+v err=%v", found, err)
	}
	if missing, err := store.FindByHAPersonEntity("person.nobody"); err != nil || missing != nil {
		t.Fatalf("unclaimed entity lookup = %+v err=%v", missing, err)
	}

	// Clearing the binding.
	if err := store.SetHAPersonEntity(contact.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	entity, _, _ = store.HAPersonEntity(contact.ID)
	if entity != "" {
		t.Errorf("binding not cleared: %q", entity)
	}
}

func TestHAPersonEntityValidation(t *testing.T) {
	store := newCounterpartyTestStore(t)
	contact, err := store.Upsert(&Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, bad := range []string{"device_tracker.phone", "person.", "person.alice.extra", "Person.Alice", "person.Alice"} {
		if err := store.SetHAPersonEntity(contact.ID, bad); err == nil {
			t.Errorf("malformed entity %q accepted", bad)
		}
	}
	if err := store.SetHAPersonEntity(uuid.New(), "person.ghost"); err == nil {
		t.Error("binding to a missing contact accepted")
	}
}

// TestHAPersonEntityUniqueness pins the storage-boundary guarantee the
// reverse lookup depends on: presence attaches to exactly one active
// counterparty, and a second claimant is refused with the current
// holder named.
func TestHAPersonEntityUniqueness(t *testing.T) {
	store := newCounterpartyTestStore(t)
	alice, err := store.Upsert(&Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Upsert(&Contact{FormattedName: "Bob Guest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetHAPersonEntity(alice.ID, "person.alice"); err != nil {
		t.Fatalf("first binding: %v", err)
	}
	err = store.SetHAPersonEntity(bob.ID, "person.alice")
	if err == nil {
		t.Fatal("duplicate claim accepted; reverse lookup is now arbitrary")
	}
	if !strings.Contains(err.Error(), "Alice Operator") {
		t.Errorf("conflict error should name the current holder: %v", err)
	}
	// Rebinding: clear, then the other contact may claim it.
	if err := store.SetHAPersonEntity(alice.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := store.SetHAPersonEntity(bob.ID, "person.alice"); err != nil {
		t.Fatalf("rebind after clear: %v", err)
	}
}
