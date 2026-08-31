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

func TestReplaceHAPersonBindingsExact(t *testing.T) {
	store := newCounterpartyTestStore(t)
	alice, err := store.Upsert(&Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Upsert(&Contact{FormattedName: "Bob Guest"})
	if err != nil {
		t.Fatal(err)
	}
	carol, err := store.Upsert(&Contact{FormattedName: "Carol Guest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetHAPersonEntity(alice.ID, "person.legacy_alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHAPersonEntity(bob.ID, "person.legacy_bob"); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceHAPersonBindings(map[uuid.UUID]string{
		alice.ID: "person.alice",
		carol.ID: "person.carol",
	}); err != nil {
		t.Fatalf("ReplaceHAPersonBindings: %v", err)
	}

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		want string
	}{
		{name: "replaced", id: alice.ID, want: "person.alice"},
		{name: "removed", id: bob.ID, want: ""},
		{name: "added", id: carol.ID, want: "person.carol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := store.HAPersonEntity(tc.id)
			if err != nil || !ok || got != tc.want {
				t.Fatalf("HAPersonEntity(%s) = %q, %v, %v; want %q, true, nil", tc.id, got, ok, err, tc.want)
			}
		})
	}

	if err := store.ReplaceHAPersonBindings(map[uuid.UUID]string{
		alice.ID: "person.carol",
		carol.ID: "person.alice",
	}); err != nil {
		t.Fatalf("swap existing unique claims: %v", err)
	}
	if got, _, _ := store.HAPersonEntity(alice.ID); got != "person.carol" {
		t.Fatalf("alice binding after swap = %q, want person.carol", got)
	}
	if got, _, _ := store.HAPersonEntity(carol.ID); got != "person.alice" {
		t.Fatalf("carol binding after swap = %q, want person.alice", got)
	}

	if err := store.ReplaceHAPersonBindings(map[uuid.UUID]string{}); err != nil {
		t.Fatalf("clear all bindings: %v", err)
	}
	for _, id := range []uuid.UUID{alice.ID, bob.ID, carol.ID} {
		if got, _, err := store.HAPersonEntity(id); err != nil || got != "" {
			t.Fatalf("binding after exact empty replacement for %s = %q, %v", id, got, err)
		}
	}
}

func TestReplaceHAPersonBindingsRollback(t *testing.T) {
	store := newCounterpartyTestStore(t)
	alice, err := store.Upsert(&Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetHAPersonEntity(alice.ID, "person.alice"); err != nil {
		t.Fatal(err)
	}

	err = store.ReplaceHAPersonBindings(map[uuid.UUID]string{
		alice.ID:   "person.changed",
		uuid.New(): "person.missing",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing contact error = %v, want not found", err)
	}
	got, ok, err := store.HAPersonEntity(alice.ID)
	if err != nil || !ok || got != "person.alice" {
		t.Fatalf("binding after rollback = %q, %v, %v; want person.alice, true, nil", got, ok, err)
	}

	err = store.ReplaceHAPersonBindings(map[uuid.UUID]string{
		alice.ID:   "person.same",
		uuid.New(): "person.same",
	})
	if err == nil || !strings.Contains(err.Error(), "assigned to both") {
		t.Fatalf("duplicate person error = %v, want duplicate assignment", err)
	}
	got, _, _ = store.HAPersonEntity(alice.ID)
	if got != "person.alice" {
		t.Fatalf("binding after duplicate rollback = %q, want person.alice", got)
	}
}
