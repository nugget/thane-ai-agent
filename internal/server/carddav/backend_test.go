package carddav

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

func newTestBackend(t *testing.T) *Backend {
	return newTestBackendWithConfigBindings(t, false)
}

func newTestBackendWithConfigBindings(t *testing.T, configOwnsHAPersonBindings bool) *Backend {
	t.Helper()
	tmp, err := os.CreateTemp("", "thane-carddav-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	db, err := database.Open(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := contacts.NewStore(db, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	return NewBackend(store, configOwnsHAPersonBindings, slog.Default())
}

func TestBackend_ListAddressBooks(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	books, err := b.ListAddressBooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("expected 1 address book, got %d", len(books))
	}
	if books[0].Path != addressBookPath {
		t.Errorf("Path = %q, want %q", books[0].Path, addressBookPath)
	}
	if books[0].Name != "Thane Contacts" {
		t.Errorf("Name = %q, want %q", books[0].Name, "Thane Contacts")
	}
}

func TestBackend_PutAndGetAddressObject(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Create a contact via the store to get a valid ID, then PUT via
	// CardDAV.
	c := &contacts.Contact{
		FormattedName: "Put Test",
		Kind:          "individual",
	}
	created, err := b.store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, created.ID.String())
	card.SetValue(vcard.FieldFormattedName, "Updated Name")
	card.SetKind(vcard.KindIndividual)
	card.SetName(&vcard.Name{
		FamilyName: "Name",
		GivenName:  "Updated",
	})
	card.Add(vcard.FieldEmail, &vcard.Field{
		Value: "test@example.com",
		Params: vcard.Params{
			vcard.ParamType: {"work"},
		},
	})

	path := objectPath(created.ID)
	obj, err := b.PutAddressObject(ctx, path, card, nil)
	if err != nil {
		t.Fatal(err)
	}
	if obj.ETag == "" {
		t.Error("expected non-empty ETag")
	}

	// GET the same object.
	got, err := b.GetAddressObject(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fn := got.Card.Value(vcard.FieldFormattedName); fn != "Updated Name" {
		t.Errorf("FN = %q, want %q", fn, "Updated Name")
	}

	// Verify the email property came through.
	emails := got.Card[vcard.FieldEmail]
	if len(emails) != 1 {
		t.Fatalf("expected 1 email, got %d", len(emails))
	}
	if emails[0].Value != "test@example.com" {
		t.Errorf("email = %q, want %q", emails[0].Value, "test@example.com")
	}
}

func TestBackend_ListAddressObjects(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Empty list.
	objects, err := b.ListAddressObjects(ctx, addressBookPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Errorf("expected 0 objects, got %d", len(objects))
	}

	// Create two contacts.
	for _, name := range []string{"Alice", "Bob"} {
		if _, err := b.store.Upsert(&contacts.Contact{
			FormattedName: name,
			Kind:          "individual",
		}); err != nil {
			t.Fatal(err)
		}
	}

	objects, err = b.ListAddressObjects(ctx, addressBookPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Errorf("expected 2 objects, got %d", len(objects))
	}
}

func TestBackend_DeleteAddressObject(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	c, err := b.store.Upsert(&contacts.Contact{
		FormattedName: "Delete Me",
		Kind:          "individual",
	})
	if err != nil {
		t.Fatal(err)
	}

	path := objectPath(c.ID)

	// Delete should succeed.
	if err := b.DeleteAddressObject(ctx, path); err != nil {
		t.Fatal(err)
	}

	// Should be gone from listings.
	objects, err := b.ListAddressObjects(ctx, addressBookPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Errorf("expected 0 objects after delete, got %d", len(objects))
	}
}

func TestBackend_QueryAddressObjects(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Create contacts with emails.
	alice, err := b.store.Upsert(&contacts.Contact{
		FormattedName: "Alice",
		Kind:          "individual",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.store.AddProperty(alice.ID, &contacts.Property{
		Property: "EMAIL", Value: "alice@example.com",
	})

	bob, err := b.store.Upsert(&contacts.Contact{
		FormattedName: "Bob",
		Kind:          "individual",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.store.AddProperty(bob.ID, &contacts.Property{
		Property: "EMAIL", Value: "bob@other.com",
	})

	// Query for contacts with EMAIL containing "example".
	query := &carddav.AddressBookQuery{
		PropFilters: []carddav.PropFilter{
			{
				Name: vcard.FieldEmail,
				TextMatches: []carddav.TextMatch{
					{
						Text:      "example",
						MatchType: carddav.MatchContains,
					},
				},
			},
		},
	}

	results, err := b.QueryAddressObjects(ctx, addressBookPath, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if fn := results[0].Card.Value(vcard.FieldFormattedName); fn != "Alice" {
		t.Errorf("matched contact FN = %q, want %q", fn, "Alice")
	}
}

func TestBackend_CurrentUserPrincipal(t *testing.T) {
	b := newTestBackend(t)
	path, err := b.CurrentUserPrincipal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/carddav/" {
		t.Errorf("CurrentUserPrincipal = %q, want %q", path, "/carddav/")
	}
}

func TestBackend_CreateAddressBookNotSupported(t *testing.T) {
	b := newTestBackend(t)
	err := b.CreateAddressBook(context.Background(), &carddav.AddressBook{})
	if err == nil {
		t.Error("expected error for CreateAddressBook")
	}
}

func TestBackend_PutCreateNewContact(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// PUT to a new UUID that doesn't exist in the store — CardDAV
	// clients create contacts this way.
	newID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	path := objectPath(newID)

	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, newID.String())
	card.SetValue(vcard.FieldFormattedName, "New Contact")
	card.SetKind(vcard.KindIndividual)
	card.SetName(&vcard.Name{
		FamilyName: "Contact",
		GivenName:  "New",
	})
	card.Add(vcard.FieldEmail, &vcard.Field{
		Value:  "new@example.com",
		Params: vcard.Params{vcard.ParamType: {"home"}},
	})

	obj, err := b.PutAddressObject(ctx, path, card, nil)
	if err != nil {
		t.Fatal(err)
	}
	if obj.ETag == "" {
		t.Error("expected non-empty ETag")
	}

	// GET the newly created contact.
	got, err := b.GetAddressObject(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fn := got.Card.Value(vcard.FieldFormattedName); fn != "New Contact" {
		t.Errorf("FN = %q, want %q", fn, "New Contact")
	}

	emails := got.Card[vcard.FieldEmail]
	if len(emails) != 1 || emails[0].Value != "new@example.com" {
		t.Errorf("EMAIL = %+v", emails)
	}
}

func TestBackend_PutUIDMismatch(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Create a contact to get a valid path.
	c, err := b.store.Upsert(&contacts.Contact{
		FormattedName: "Mismatch Test",
		Kind:          "individual",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := objectPath(c.ID)

	// PUT a card with a different UID — should be rejected.
	otherID, _ := uuid.NewV7()
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, otherID.String())
	card.SetValue(vcard.FieldFormattedName, "Mismatch")
	card.SetKind(vcard.KindIndividual)

	_, err = b.PutAddressObject(ctx, path, card, nil)
	if err == nil {
		t.Error("expected error for UID mismatch, got nil")
	}
}

func TestBackend_RoundTrip(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Create a contact via the store.
	c, err := b.store.Upsert(&contacts.Contact{
		FormattedName: "Round Trip",
		Kind:          "individual",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := objectPath(c.ID)

	// PUT a full vCard.
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, c.ID.String())
	card.SetValue(vcard.FieldFormattedName, "Round Trip")
	card.SetKind(vcard.KindIndividual)
	card.SetName(&vcard.Name{
		FamilyName: "Trip",
		GivenName:  "Round",
	})
	card.SetValue(vcard.FieldOrganization, "Test Corp")
	card.SetValue(vcard.FieldTitle, "Engineer")
	card.SetValue(vcard.FieldNote, "Test note")
	card.SetValue("X-THANE-TRUST-ZONE", "trusted")
	card.SetValue("X-THANE-AI-SUMMARY", "Test summary")

	card.Add(vcard.FieldEmail, &vcard.Field{
		Value:  "rt@example.com",
		Params: vcard.Params{vcard.ParamType: {"work"}, vcard.ParamPreferred: {"1"}},
	})
	card.Add(vcard.FieldTelephone, &vcard.Field{
		Value:  "+15551234567",
		Params: vcard.Params{vcard.ParamType: {"cell"}},
	})

	putObj, err := b.PutAddressObject(ctx, path, card, nil)
	if err != nil {
		t.Fatal(err)
	}

	// GET it back.
	getObj, err := b.GetAddressObject(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Verify ETags match.
	if getObj.ETag != putObj.ETag {
		t.Errorf("ETag mismatch: put=%q get=%q", putObj.ETag, getObj.ETag)
	}

	// Verify fields round-tripped.
	got := getObj.Card
	if got.Value(vcard.FieldFormattedName) != "Round Trip" {
		t.Errorf("FN = %q", got.Value(vcard.FieldFormattedName))
	}
	if got.Value(vcard.FieldOrganization) != "Test Corp" {
		t.Errorf("ORG = %q", got.Value(vcard.FieldOrganization))
	}
	if got.Value(vcard.FieldTitle) != "Engineer" {
		t.Errorf("TITLE = %q", got.Value(vcard.FieldTitle))
	}
	if got.Value(vcard.FieldNote) != "Test note" {
		t.Errorf("NOTE = %q", got.Value(vcard.FieldNote))
	}
	if got.Value("X-THANE-TRUST-ZONE") != "trusted" {
		t.Errorf("X-THANE-TRUST-ZONE = %q", got.Value("X-THANE-TRUST-ZONE"))
	}

	emails := got[vcard.FieldEmail]
	if len(emails) != 1 || emails[0].Value != "rt@example.com" {
		t.Errorf("EMAIL = %+v", emails)
	}
}

// newTestCard builds a minimal valid vCard for one contact.
func newTestCard(name string, id uuid.UUID) vcard.Card {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, id.String())
	card.SetValue(vcard.FieldFormattedName, name)
	card.SetKind(vcard.KindIndividual)
	return card
}

// TestHAPersonBindingRoundTrip pins the operator custody path (#1450):
// CardDAV is the one surface that honors X-THANE-HA-PERSON — a PUT
// sets the binding, reads emit it, clearing the header clears the
// binding, and a claim already held by another contact fails the PUT.
func TestHAPersonBindingRoundTrip(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	id := uuid.New()
	card := newTestCard("Alice Operator", id)
	card.SetValue("X-THANE-HA-PERSON", "person.alice")
	if _, err := b.PutAddressObject(ctx, objectPath(id), card, nil); err != nil {
		t.Fatalf("put with binding: %v", err)
	}
	entity, ok, err := b.store.HAPersonEntity(id)
	if err != nil || !ok || entity != "person.alice" {
		t.Fatalf("binding not stored: %q ok=%v err=%v", entity, ok, err)
	}

	got, err := b.GetAddressObject(ctx, objectPath(id), nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Card.Value("X-THANE-HA-PERSON") != "person.alice" {
		t.Errorf("read did not emit binding: %q", got.Card.Value("X-THANE-HA-PERSON"))
	}

	// A second contact claiming the same person entity fails the PUT.
	rivalID := uuid.New()
	rival := newTestCard("Mallory Rival", rivalID)
	rival.SetValue("X-THANE-HA-PERSON", "person.alice")
	if _, err := b.PutAddressObject(ctx, objectPath(rivalID), rival, nil); err == nil {
		t.Fatal("duplicate person claim accepted over CardDAV")
	}

	// Clearing the header clears the binding.
	card2 := newTestCard("Alice Operator", id)
	if _, err := b.PutAddressObject(ctx, objectPath(id), card2, nil); err != nil {
		t.Fatalf("put without binding: %v", err)
	}
	entity, _, _ = b.store.HAPersonEntity(id)
	if entity != "" {
		t.Errorf("binding not cleared by header-less PUT: %q", entity)
	}
}

// TestHAPersonBindingPutIsAtomic pins PUT atomicity (#1450): a PUT
// whose binding is rejected must apply nothing — a brand-new contact
// with a claim another contact holds is not created at all.
func TestHAPersonBindingPutIsAtomic(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	holderID := uuid.New()
	holder := newTestCard("Alice Operator", holderID)
	holder.SetValue("X-THANE-HA-PERSON", "person.alice")
	if _, err := b.PutAddressObject(ctx, objectPath(holderID), holder, nil); err != nil {
		t.Fatalf("holder put: %v", err)
	}

	rivalID := uuid.New()
	rival := newTestCard("Mallory Rival", rivalID)
	rival.SetValue("X-THANE-HA-PERSON", "person.alice")
	if _, err := b.PutAddressObject(ctx, objectPath(rivalID), rival, nil); err == nil {
		t.Fatal("duplicate-claim PUT accepted")
	}
	// Atomicity: the rival contact must not exist at all.
	if _, err := b.store.GetWithProperties(rivalID); err == nil {
		t.Fatal("rejected PUT still created the contact — binding write is not atomic with the upsert")
	}
}

func TestConfiguredHAPersonBindingIsReadOnlyInCardDAV(t *testing.T) {
	b := newTestBackendWithConfigBindings(t, true)
	ctx := context.Background()

	operator, err := b.store.Upsert(&contacts.Contact{FormattedName: "Operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.store.ReplaceHAPersonBindings(map[uuid.UUID]string{
		operator.ID: "person.operator",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := b.GetAddressObject(ctx, objectPath(operator.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if entity := got.Card.Value("X-THANE-HA-PERSON"); entity != "person.operator" {
		t.Fatalf("GET binding = %q, want person.operator", entity)
	}

	// Clients commonly discard unknown X- fields. A header-less update
	// must preserve the configured binding while applying ordinary data.
	headerless := newTestCard("Renamed Operator", operator.ID)
	if _, err := b.PutAddressObject(ctx, objectPath(operator.ID), headerless, nil); err != nil {
		t.Fatalf("header-less PUT: %v", err)
	}
	entity, ok, err := b.store.HAPersonEntity(operator.ID)
	if err != nil || !ok || entity != "person.operator" {
		t.Fatalf("binding after header-less PUT = %q, %v, %v", entity, ok, err)
	}
	updated, err := b.store.Get(operator.ID)
	if err != nil || updated.FormattedName != "Renamed Operator" {
		t.Fatalf("ordinary contact update = %+v, %v", updated, err)
	}

	equal := newTestCard("Still Operator", operator.ID)
	equal.SetValue("X-THANE-HA-PERSON", "person.operator")
	if _, err := b.PutAddressObject(ctx, objectPath(operator.ID), equal, nil); err != nil {
		t.Fatalf("round-trip of configured value: %v", err)
	}

	clear := newTestCard("Rejected Clear", operator.ID)
	clear.SetValue("X-THANE-HA-PERSON", "  ")
	if _, err := b.PutAddressObject(ctx, objectPath(operator.ID), clear, nil); err == nil || !strings.Contains(err.Error(), "person.contact_bindings") {
		t.Fatalf("explicit clear of configured binding error = %v, want config guidance", err)
	}
	unchanged, err := b.store.Get(operator.ID)
	if err != nil || unchanged.FormattedName != "Still Operator" {
		t.Fatalf("rejected clear changed contact = %+v, %v", unchanged, err)
	}

	changed := newTestCard("Rejected Rename", operator.ID)
	changed.SetValue("X-THANE-HA-PERSON", "person.somebody_else")
	if _, err := b.PutAddressObject(ctx, objectPath(operator.ID), changed, nil); err == nil || !strings.Contains(err.Error(), "person.contact_bindings") {
		t.Fatalf("changed configured binding error = %v, want config guidance", err)
	}
	unchanged, err = b.store.Get(operator.ID)
	if err != nil || unchanged.FormattedName != "Still Operator" {
		t.Fatalf("rejected PUT changed contact = %+v, %v", unchanged, err)
	}

	newID := uuid.New()
	claim := newTestCard("New Claim", newID)
	claim.SetValue("X-THANE-HA-PERSON", "person.new_claim")
	if _, err := b.PutAddressObject(ctx, objectPath(newID), claim, nil); err == nil {
		t.Fatal("CardDAV created a binding while config owns the set")
	}
	if _, err := b.store.Get(newID); err == nil {
		t.Fatal("rejected configured-binding PUT still created the contact")
	}
	if err := b.DeleteAddressObject(ctx, objectPath(operator.ID)); err == nil || !strings.Contains(err.Error(), "person.contact_bindings") {
		t.Fatalf("delete configured-bound contact error = %v, want config guidance", err)
	}
	if entity, exists, err := b.store.HAPersonEntity(operator.ID); err != nil || !exists || entity != "person.operator" {
		t.Fatalf("binding after rejected DELETE = %q, %v, %v", entity, exists, err)
	}

	headerlessID := uuid.New()
	if _, err := b.PutAddressObject(ctx, objectPath(headerlessID), newTestCard("Ordinary Contact", headerlessID), nil); err != nil {
		t.Fatalf("header-less new contact should remain writable: %v", err)
	}
	if err := b.DeleteAddressObject(ctx, objectPath(headerlessID)); err != nil {
		t.Fatalf("delete unbound contact: %v", err)
	}
	if _, err := b.store.Get(headerlessID); err == nil {
		t.Fatal("unbound contact still exists after DELETE")
	}
}
