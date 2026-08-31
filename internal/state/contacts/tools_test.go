package contacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// fakeEmbedder returns a fixed embedding for any text.
type fakeEmbedder struct {
	embedding []float32
	err       error
}

type callbackEmbedder struct {
	generate func()
}

func (e *callbackEmbedder) Generate(_ context.Context, _ string) ([]float32, error) {
	e.generate()
	return []float32{0.25}, nil
}

func (f *fakeEmbedder) Generate(_ context.Context, _ string) ([]float32, error) {
	return f.embedding, f.err
}

func newTestTools(t *testing.T) *Tools {
	t.Helper()
	store := newTestStore(t)
	return NewTools(store, nil)
}

func TestSaveContact_New(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.SaveContact(`{"name":"Alice Johnson","kind":"individual","ai_summary":"Works at Anthropic"}`)
	if err != nil {
		t.Fatalf("SaveContact() error = %v", err)
	}
	if !strings.Contains(result, "Alice Johnson") {
		t.Errorf("result = %q, want to contain 'Alice Johnson'", result)
	}
	if !strings.Contains(result, "Saved new contact") {
		t.Errorf("result = %q, want to contain 'Saved new contact'", result)
	}

	c, err := tools.store.FindByName("Alice Johnson")
	if err != nil {
		t.Fatalf("FindByName() error = %v", err)
	}
	if c.AISummary != "Works at Anthropic" {
		t.Errorf("AISummary = %q, want %q", c.AISummary, "Works at Anthropic")
	}
}

func TestSaveContact_Update(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Bob Smith","kind":"individual","ai_summary":"Original"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.SaveContact(`{"name":"Bob Smith","note":"Updated note","ai_summary":"Updated"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Updated contact") {
		t.Errorf("result = %q, want to contain 'Updated contact'", result)
	}

	c, err := tools.store.FindByName("Bob Smith")
	if err != nil {
		t.Fatal(err)
	}
	if c.AISummary != "Updated" {
		t.Errorf("AISummary = %q, want %q", c.AISummary, "Updated")
	}
	if c.Note != "Updated note" {
		t.Errorf("Note = %q, want %q", c.Note, "Updated note")
	}
}

func TestSaveContact_WithFacts(t *testing.T) {
	tools := newTestTools(t)

	// All entries go to contact_properties.
	_, err := tools.SaveContact(`{"name":"Charlie Facts","kind":"individual","facts":{"email":"charlie@example.com","phone":"555-9999","timezone":"America/Chicago"}}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Charlie Facts")
	if err != nil {
		t.Fatal(err)
	}

	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEmail, foundTel, foundTZ := false, false, false
	for _, p := range props {
		if p.Property == "EMAIL" && p.Value == "charlie@example.com" {
			foundEmail = true
		}
		if p.Property == "TEL" && p.Value == "555-9999" {
			foundTel = true
		}
		if p.Property == "timezone" && p.Value == "America/Chicago" {
			foundTZ = true
		}
	}
	if !foundEmail {
		t.Error("expected EMAIL property for charlie@example.com")
	}
	if !foundTel {
		t.Error("expected TEL property for 555-9999")
	}
	if !foundTZ {
		t.Error("expected timezone property for America/Chicago")
	}
}

func TestSaveContactFromModelRecordsPropertyProvenanceAndSignalsOnce(t *testing.T) {
	iteration := 3
	provenance := &PropertyProvenance{
		Source:         "contact_save",
		Model:          "test-model",
		LoopID:         "loop-archivist-1",
		ConversationID: "loop-archivist-1-123",
		SessionID:      "019c52f0-9ce8-7708-867f-35da2e6b4777",
		RequestID:      "r_contact",
		ToolCallID:     "call_contact",
		Iteration:      &iteration,
	}
	var mutations []ContactMutation
	store := newTestStore(t)
	tools := NewTools(store, func(_ context.Context, mutation ContactMutation) error {
		mutations = append(mutations, mutation)
		return nil
	})

	result, err := tools.SaveContactFromModel(context.Background(), `{
		"name":"Provenance Person",
		"kind":"individual",
		"facts":{"email":"person@example.com","timezone":"America/Chicago"},
		"origin_tags":["signal","SIGNAL","projects"]
	}`, provenance)
	if err != nil {
		t.Fatalf("SaveContactFromModel() error = %v", err)
	}
	if !strings.Contains(result, "Saved new contact") {
		t.Fatalf("result = %q, want new-contact result", result)
	}
	if len(mutations) != 1 {
		t.Fatalf("mutation calls = %d, want 1", len(mutations))
	}
	wantFields := []string{"formatted_name", "kind", "property:EMAIL", "property:X-THANE-ORIGIN-TAG", "property:timezone"}
	if !reflect.DeepEqual(mutations[0].Fields, wantFields) {
		t.Errorf("mutation fields = %#v, want %#v", mutations[0].Fields, wantFields)
	}
	if mutations[0].Provenance != provenance {
		t.Error("mutation did not preserve the model-turn provenance")
	}

	contact, err := tools.store.FindByName("Provenance Person")
	if err != nil {
		t.Fatal(err)
	}
	properties, err := tools.store.GetProperties(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(properties) != 4 {
		t.Fatalf("properties = %#v, want 4", properties)
	}
	for _, property := range properties {
		if !reflect.DeepEqual(property.Provenance, provenance) {
			t.Errorf("property %s provenance = %#v, want %#v", property.Property, property.Provenance, provenance)
		}
	}

	newer := &PropertyProvenance{Source: "contact_save", RequestID: "r_retry"}
	result, err = tools.SaveContactFromModel(context.Background(), `{
		"name":"Provenance Person",
		"kind":"individual",
		"facts":{"email":"PERSON@example.com","timezone":"america/chicago"},
		"origin_tags":["PROJECTS","SIGNAL"]
	}`, newer)
	if err != nil {
		t.Fatalf("repeated SaveContactFromModel() error = %v", err)
	}
	if !strings.Contains(result, "Contact unchanged") || !strings.Contains(result, "no dossier refresh") {
		t.Fatalf("no-op result = %q, want explicit unchanged result", result)
	}
	if len(mutations) != 1 {
		t.Fatalf("no-op mutation calls = %d, want 1 total", len(mutations))
	}
	properties, err = tools.store.GetProperties(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, property := range properties {
		if !reflect.DeepEqual(property.Provenance, provenance) {
			t.Errorf("no-op rewrote property %s provenance to %#v", property.Property, property.Provenance)
		}
	}
}

func TestSaveContactFromModelDoesNotSignalRejectedOrRolledBackWrites(t *testing.T) {
	calls := 0
	store := newTestStore(t)
	tools := NewTools(store, func(context.Context, ContactMutation) error {
		calls++
		return nil
	})
	provenance := &PropertyProvenance{Source: "contact_save", RequestID: "r_rejected"}

	if _, err := tools.SaveContactFromModel(context.Background(), `{
		"name":"Custody Rejection",
		"kind":"individual",
		"trust_zone":"admin"
	}`, provenance); err == nil || !strings.Contains(err.Error(), "operator-custodied") {
		t.Fatalf("trust-zone rejection error = %v", err)
	}
	if _, err := tools.store.db.Exec(`
		CREATE TRIGGER fail_model_property
		BEFORE INSERT ON contact_properties
		BEGIN
			SELECT RAISE(ABORT, 'forced property rollback');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.SaveContactFromModel(context.Background(), `{
		"name":"Rollback Rejection",
		"kind":"individual",
		"facts":{"email":"nobody@example.com"}
	}`, provenance); err == nil || !strings.Contains(err.Error(), "forced property rollback") {
		t.Fatalf("property rollback error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("rejected/rolled-back writes signaled %d mutations, want 0", calls)
	}
	if _, err := tools.store.FindByName("Rollback Rejection"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled-back contact lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestSaveContact_OriginPolicyProperties(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{
		"name":"Origin Policy",
		"kind":"individual",
		"origin_tags":["signal","projects"],
		"origin_context_refs":["kb:projects/current.md"]
	}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Origin Policy")
	if err != nil {
		t.Fatal(err)
	}
	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := propertyValues(props, PropertyOriginTag), []string{"signal", "projects"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("origin tags = %#v, want %#v", got, want)
	}
	if got, want := propertyValues(props, PropertyOriginContextRef), []string{"kb:projects/current.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("origin context refs = %#v, want %#v", got, want)
	}

	_, err = tools.SaveContact(`{
		"name":"Origin Policy",
		"origin_tags":[],
		"origin_context_refs":[]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	props, err = tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := propertyValues(props, PropertyOriginTag); len(got) != 0 {
		t.Fatalf("origin tags should be cleared, got %#v", got)
	}
	if got := propertyValues(props, PropertyOriginContextRef); len(got) != 0 {
		t.Fatalf("origin context refs should be cleared, got %#v", got)
	}
}

func propertyValues(props []Property, name string) []string {
	var values []string
	for _, prop := range props {
		if prop.Property == name {
			values = append(values, prop.Value)
		}
	}
	return values
}

func TestSaveContact_TopLevelFieldsRescued(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.SaveContact(`{
		"name": "James Harren",
		"email": "shaded123@gmail.com",
		"phone": "555-1234",
		"notes": "First Thane beta tester candidate"
	}`)
	if err != nil {
		t.Fatalf("SaveContact() error = %v", err)
	}
	if !strings.Contains(result, "Saved new contact") {
		t.Errorf("result = %q, want 'Saved new contact'", result)
	}

	c, err := tools.store.FindByName("James Harren")
	if err != nil {
		t.Fatal(err)
	}

	// email → EMAIL property, phone → TEL property.
	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEmail, foundTel := false, false
	for _, p := range props {
		if p.Property == "EMAIL" && p.Value == "shaded123@gmail.com" {
			foundEmail = true
		}
		if p.Property == "TEL" && p.Value == "555-1234" {
			foundTel = true
		}
	}
	if !foundEmail {
		t.Error("expected EMAIL property for rescued email")
	}
	if !foundTel {
		t.Error("expected TEL property for rescued phone")
	}

	// notes goes to properties (with its key as-is).
	foundNotes := false
	for _, p := range props {
		if p.Property == "notes" && p.Value == "First Thane beta tester candidate" {
			foundNotes = true
		}
	}
	if !foundNotes {
		t.Error("expected notes property for rescued notes field")
	}
}

func TestSaveContact_TopLevelFieldsMergeWithExplicitFacts(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{
		"name": "Mixed Fields",
		"email": "mixed@example.com",
		"facts": {"timezone": "America/Chicago"}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Mixed Fields")
	if err != nil {
		t.Fatal(err)
	}

	// email → EMAIL property.
	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEmail := false
	for _, p := range props {
		if p.Property == "EMAIL" && p.Value == "mixed@example.com" {
			foundEmail = true
		}
	}
	if !foundEmail {
		t.Error("expected EMAIL property for rescued email")
	}

	// timezone → property.
	foundTZ := false
	for _, p := range props {
		if p.Property == "timezone" && p.Value == "America/Chicago" {
			foundTZ = true
		}
	}
	if !foundTZ {
		t.Error("expected timezone property for America/Chicago")
	}
}

func TestSaveContact_ExplicitFactsTakePrecedence(t *testing.T) {
	tools := newTestTools(t)

	// When the same key appears both top-level and in facts, the explicit
	// facts value must win — rescue should not overwrite it.
	_, err := tools.SaveContact(`{
		"name": "Conflict Fields",
		"email": "top-level@example.com",
		"facts": {"email": "explicit@example.com"}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Conflict Fields")
	if err != nil {
		t.Fatal(err)
	}

	// The explicit email should be the one stored as a property.
	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundExplicit := false
	for _, p := range props {
		if p.Property == "EMAIL" && p.Value == "explicit@example.com" {
			foundExplicit = true
		}
	}
	if !foundExplicit {
		t.Errorf("expected EMAIL property for explicit@example.com, got props: %+v", props)
	}
}

func TestSaveContact_TopLevelFieldsIgnoreNonString(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{
		"name": "Typed Fields",
		"email": "typed@example.com",
		"count": 42,
		"active": true
	}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Typed Fields")
	if err != nil {
		t.Fatal(err)
	}

	// email → EMAIL property.
	props, _ := tools.store.GetProperties(c.ID)
	foundEmail := false
	for _, p := range props {
		if p.Property == "EMAIL" {
			foundEmail = true
		}
	}
	if !foundEmail {
		t.Error("email should be rescued as EMAIL property")
	}

	// count and active are not strings — should not be rescued.
	propsMap, _ := tools.store.GetPropertiesMap(c.ID)
	if _, exists := propsMap["count"]; exists {
		t.Error("non-string field 'count' should not be rescued")
	}
	if _, exists := propsMap["active"]; exists {
		t.Error("non-string field 'active' should not be rescued")
	}
}

func TestSaveContact_TopLevelFieldsOnUpdate(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name": "Update Target", "kind": "individual"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.SaveContact(`{
		"name": "Update Target",
		"organization": "Acme Corp"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Updated contact") {
		t.Errorf("result = %q, want 'Updated contact'", result)
	}

	c, err := tools.store.FindByName("Update Target")
	if err != nil {
		t.Fatal(err)
	}
	// "organization" is not a recognized vCard key, so it goes to properties with its key as-is.
	propsMap, err := tools.store.GetPropertiesMap(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(propsMap["organization"]) != 1 || propsMap["organization"][0] != "Acme Corp" {
		t.Errorf("organization = %v, want [Acme Corp]", propsMap["organization"])
	}
}

func TestSaveContact_WithEmbedding(t *testing.T) {
	tools := newTestTools(t)
	tools.SetEmbeddingClient(&fakeEmbedder{embedding: []float32{0.1, 0.2, 0.3}})

	_, err := tools.SaveContact(`{"name":"Embedded Eve","kind":"individual","ai_summary":"Has embedding"}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Embedded Eve")
	if err != nil {
		t.Fatal(err)
	}

	contacts, _, err := tools.store.SemanticSearch([]float32{0.1, 0.2, 0.3}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].ID != c.ID {
		t.Error("expected semantic search to find the contact with embedding")
	}
}

func TestSaveContactFromModelQueuesRefreshBeforeEmbedding(t *testing.T) {
	var order []string
	tools := NewTools(newTestStore(t), func(context.Context, ContactMutation) error {
		order = append(order, "refresh")
		return nil
	})
	tools.SetEmbeddingClient(&callbackEmbedder{generate: func() {
		order = append(order, "embedding")
	}})

	if _, err := tools.SaveContactFromModel(t.Context(), `{"name":"Ordered Contact","kind":"individual"}`, &PropertyProvenance{Source: "contact_save"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"refresh", "embedding"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("post-commit order = %#v, want %#v", order, want)
	}
}

func TestSaveContact_NameRequired(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"kind":"individual"}`)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestLookupContact_ByName(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Dana Recall","kind":"individual","ai_summary":"Test recall"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.LookupContact(`{"name":"Dana Recall"}`)
	if err != nil {
		t.Fatalf("LookupContact() error = %v", err)
	}
	if !strings.Contains(result, "Dana Recall") {
		t.Errorf("result = %q, want to contain 'Dana Recall'", result)
	}
	if !strings.Contains(result, "Test recall") {
		t.Errorf("result = %q, want to contain 'Test recall'", result)
	}
}

func TestLookupContact_ByName_NotFound(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.LookupContact(`{"name":"Nobody"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No contact found") {
		t.Errorf("result = %q, want 'No contact found'", result)
	}
}

func TestLookupContact_ByQuery(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Eve Search","kind":"individual","ai_summary":"Backend developer"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.LookupContact(`{"query":"developer"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Eve Search") {
		t.Errorf("result = %q, want to contain 'Eve Search'", result)
	}
}

func TestLookupContact_ByKind(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"IndivA","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"OrgA","kind":"org"}`)

	result, err := tools.LookupContact(`{"kind":"org"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "OrgA") {
		t.Errorf("result = %q, want to contain 'OrgA'", result)
	}
	if strings.Contains(result, "IndivA") {
		t.Errorf("result should not contain 'IndivA'")
	}
}

func TestLookupContact_ByPropertyKey(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Frank Prop","kind":"individual","facts":{"timezone":"America/Chicago"}}`)

	result, err := tools.LookupContact(`{"key":"timezone","value":"America/Chicago"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Frank Prop") {
		t.Errorf("result = %q, want to contain 'Frank Prop'", result)
	}
}

func TestLookupContact_ByProperty(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Prop Person","kind":"individual","facts":{"email":"prop@example.com"}}`)

	// Search by email key — should route to property lookup.
	result, err := tools.LookupContact(`{"key":"email","value":"prop@example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Prop Person") {
		t.Errorf("result = %q, want to contain 'Prop Person'", result)
	}
}

func TestLookupContact_Stats(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"P1","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"P2","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"C1","kind":"org"}`)

	result, err := tools.LookupContact(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "3 contacts") {
		t.Errorf("result = %q, want to contain '3 contacts'", result)
	}
}

func TestForgetContact(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Grace Forget","kind":"individual"}`)

	result, err := tools.ForgetContact(`{"name":"Grace Forget"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Forgot contact") {
		t.Errorf("result = %q, want to contain 'Forgot contact'", result)
	}

	recall, err := tools.LookupContact(`{"name":"Grace Forget"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recall, "No contact found") {
		t.Errorf("recall = %q, want 'No contact found'", recall)
	}
}

func TestForgetContact_NameRequired(t *testing.T) {
	tools := newTestTools(t)
	_, err := tools.ForgetContact(`{}`)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestForgetContact_NotFound(t *testing.T) {
	tools := newTestTools(t)
	_, err := tools.ForgetContact(`{"name":"Nobody"}`)
	if err == nil {
		t.Error("expected error for non-existent contact")
	}
}

func TestListContacts_All(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Alpha","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"Beta","kind":"org"}`)
	_, _ = tools.SaveContact(`{"name":"Gamma","kind":"individual"}`)

	result, err := tools.ListContacts(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "3 contact(s)") {
		t.Errorf("result = %q, want to contain '3 contact(s)'", result)
	}
	if !strings.Contains(result, "Alpha") || !strings.Contains(result, "Beta") || !strings.Contains(result, "Gamma") {
		t.Errorf("result should contain all contacts, got %q", result)
	}
}

func TestListContacts_ByKind(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"IndivX","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"OrgX","kind":"org"}`)

	result, err := tools.ListContacts(`{"kind":"org"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "OrgX") {
		t.Errorf("result = %q, want to contain 'OrgX'", result)
	}
	if strings.Contains(result, "IndivX") {
		t.Errorf("result should not contain 'IndivX'")
	}
}

func TestListContacts_WithLimit(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"A1","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"A2","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"A3","kind":"individual"}`)

	result, err := tools.ListContacts(`{"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "2 contact(s)") {
		t.Errorf("result = %q, want to contain '2 contact(s)'", result)
	}
}

func TestListContacts_Empty(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.ListContacts(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No contacts") {
		t.Errorf("result = %q, want 'No contacts'", result)
	}
}

// TestSaveContact_TrustZoneIsOperatorCustody pins the #1450 security
// boundary: trust zones confer inherited authority on every companion
// device bound to a contact, so the everyday save path must never be a
// promotion path. Any attempt is a hard, teaching error — for create
// and update alike — and a zone the operator assigned survives every
// tool-driven update untouched.

// setZone assigns a trust zone through the store directly — the
// operator-custody path — since contact_save refuses zone mutation by
// design (#1450).
func setZone(t *testing.T, tools *Tools, name, zone string) {
	t.Helper()
	c, err := tools.store.FindByName(name)
	if err != nil {
		t.Fatalf("setZone find %s: %v", name, err)
	}
	c.TrustZone = zone
	if _, err := tools.store.Upsert(c); err != nil {
		t.Fatalf("setZone upsert %s: %v", name, err)
	}
}

func TestSaveContact_TrustZoneIsOperatorCustody(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Trusted Pal","kind":"individual","trust_zone":"trusted"}`)
	if err == nil || !strings.Contains(err.Error(), "operator-custodied") {
		t.Fatalf("zone set on create must be refused with custody teaching, got %v", err)
	}

	if _, err := tools.SaveContact(`{"name":"Zone Holder","kind":"individual"}`); err != nil {
		t.Fatal(err)
	}
	c, err := tools.store.FindByName("Zone Holder")
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustZone != "known" {
		t.Fatalf("create default TrustZone = %q, want %q", c.TrustZone, "known")
	}

	_, err = tools.SaveContact(`{"name":"Zone Holder","trust_zone":"admin"}`)
	if err == nil || !strings.Contains(err.Error(), "operator-custodied") {
		t.Fatalf("zone promotion via update must be refused, got %v", err)
	}

	// Operator-assigned zones survive tool-driven updates untouched.
	c.TrustZone = ZoneTrusted
	if _, err := tools.store.Upsert(c); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.SaveContact(`{"name":"Zone Holder","ai_summary":"New summary"}`); err != nil {
		t.Fatal(err)
	}
	c, err = tools.store.FindByName("Zone Holder")
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustZone != ZoneTrusted {
		t.Errorf("TrustZone after unrelated update = %q, want preserved %q", c.TrustZone, ZoneTrusted)
	}
}

func TestFormatContact_TrustZone(t *testing.T) {
	c := &Contact{
		FormattedName: "Test Person",
		Kind:          "individual",
		TrustZone:     "trusted",
	}

	result := formatContact(c)
	if !strings.Contains(result, "Kind: individual | Trust: trusted") {
		t.Errorf("formatContact() = %q, want to contain 'Kind: individual | Trust: trusted'", result)
	}
}

func TestLookupContactIncludesConfiguredDossierTrailhead(t *testing.T) {
	tools := newTestTools(t)
	if _, err := tools.SaveContact(`{"name":"Dossier Person","kind":"individual"}`); err != nil {
		t.Fatal(err)
	}
	contact, err := tools.store.FindByName("Dossier Person")
	if err != nil {
		t.Fatal(err)
	}

	without, err := tools.LookupContact(`{"name":"Dossier Person"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "Dossier:") || strings.Contains(without, "Contact ID:") {
		t.Fatalf("unconfigured dossier root leaked a dead trailhead: %s", without)
	}

	tools.ConfigureDossierRoot(true, false)
	with, err := tools.LookupContact(`{"name":"Dossier Person"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Contact ID: " + contact.ID.String(), "Dossier target: " + DossierRef(contact.ID), "may be absent"} {
		if !strings.Contains(with, want) {
			t.Fatalf("configured result = %q, want %q", with, want)
		}
	}
	if strings.Contains(with, "if absent, create") {
		t.Fatalf("read-only dossier trailhead suggested creation: %s", with)
	}

	tools.ConfigureDossierRoot(true, true)
	tools.ConfigureDossierDocuments(func(_ context.Context, args documents.RefArgs) (string, error) {
		return args.Ref, nil
	}, (&recordingDossierWriter{}).Write)
	writable, err := tools.LookupContact(`{"name":"Dossier Person"}`)
	if err != nil {
		t.Fatal(err)
	}
	wantCall := `Dossier access: contact_dossier_read(contact_id="` + contact.ID.String() + `")`
	if !strings.Contains(writable, wantCall) {
		t.Fatalf("managed dossier trailhead = %q, want exact read call %q", writable, wantCall)
	}
	if strings.Contains(writable, DossierRef(contact.ID)) || strings.Contains(writable, "doc_read") {
		t.Fatalf("managed dossier trailhead exposed the error-prone raw ref path: %s", writable)
	}
}

func TestGenerateMissingEmbeddings(t *testing.T) {
	tools := newTestTools(t)
	tools.SetEmbeddingClient(&fakeEmbedder{embedding: []float32{0.5, 0.5}})

	_, _ = tools.SaveContact(`{"name":"NeedsEmbed","kind":"individual","ai_summary":"No embed yet"}`)

	c, _ := tools.store.FindByName("NeedsEmbed")
	_ = tools.store.SetEmbedding(c.ID, nil)

	count, err := tools.GenerateMissingEmbeddings()
	if err != nil {
		t.Fatalf("GenerateMissingEmbeddings() error = %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestGenerateMissingEmbeddings_NoClient(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.GenerateMissingEmbeddings()
	if err == nil {
		t.Error("expected error with no embedding client")
	}
}

func TestSaveContact_IMPPPropertyPrefix(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Signal User","kind":"individual","facts":{"signal":"+15551234567"}}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Signal User")
	if err != nil {
		t.Fatal(err)
	}

	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, p := range props {
		if p.Property == "IMPP" && p.Value == "signal:+15551234567" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected IMPP property with signal: prefix, got: %+v", props)
	}
}

func TestSaveContact_IMPPMatrixColonHandling(t *testing.T) {
	tools := newTestTools(t)

	// Matrix IDs contain colons (@user:server.com) — the prefix logic
	// must not skip them.
	_, err := tools.SaveContact(`{"name":"Matrix User","kind":"individual","facts":{"matrix":"@alice:matrix.org"}}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Matrix User")
	if err != nil {
		t.Fatal(err)
	}

	props, err := tools.store.GetProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, p := range props {
		if p.Property == "IMPP" && p.Value == "matrix:@alice:matrix.org" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected IMPP property with matrix: prefix for Matrix ID, got: %+v", props)
	}
}

// --- VCF Export/Import Tool Tests ---

func TestExportVCF_BasicExport(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Export Test","kind":"individual","given_name":"Export","family_name":"Test","facts":{"email":"export@example.com"}}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.ExportVCF(`{"name":"Export Test","format":"text"}`)
	if err != nil {
		t.Fatalf("ExportVCF() error = %v", err)
	}
	if !strings.Contains(result, "BEGIN:VCARD") {
		t.Errorf("result should contain BEGIN:VCARD, got %q", result)
	}
	if !strings.Contains(result, "Export Test") {
		t.Errorf("result should contain contact name, got %q", result)
	}
	if !strings.Contains(result, "export@example.com") {
		t.Errorf("result should contain email, got %q", result)
	}
}

func TestExportVCF_FileFormat(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"File Export","kind":"individual"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.ExportVCF(`{"name":"File Export"}`)
	if err != nil {
		t.Fatalf("ExportVCF() error = %v", err)
	}
	if !strings.Contains(result, "Exported vCard to") {
		t.Errorf("result = %q, want to contain 'Exported vCard to'", result)
	}
	// Extract path and verify file exists.
	path := strings.TrimPrefix(result, "Exported vCard to ")
	t.Cleanup(func() { os.Remove(path) })
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("exported file %q does not exist", path)
	}
}

func TestExportVCF_SelfContact(t *testing.T) {
	tools := newTestTools(t)
	tools.SetSelfContactName("Thane Agent")

	_, err := tools.SaveContact(`{"name":"Thane Agent","kind":"individual","facts":{"email":"thane@example.com"}}`)
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, tools, "Thane Agent", "admin")

	result, err := tools.ExportVCF(`{"name":"self","format":"text"}`)
	if err != nil {
		t.Fatalf("ExportVCF() error = %v", err)
	}
	if !strings.Contains(result, "Thane Agent") {
		t.Errorf("self export should resolve to configured name, got %q", result)
	}
}

func TestExportVCF_SelfNotConfigured(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.ExportVCF(`{"name":"self","format":"text"}`)
	if err == nil {
		t.Error("expected error when self-contact not configured")
	}
	if !strings.Contains(err.Error(), "self-contact not configured") {
		t.Errorf("error = %v, want to mention self-contact not configured", err)
	}
}

func TestExportVCF_SelfWithTrustZoneFilter(t *testing.T) {
	tools := newTestTools(t)
	tools.SetSelfContactName("Agent Self")

	_, err := tools.SaveContact(`{"name":"Agent Self","kind":"individual","facts":{"email":"agent@example.com","phone":"555-0000"}}`)
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, tools, "Agent Self", "admin")

	// Export for unknown zone — should strip email and phone.
	result, err := tools.ExportVCF(`{"name":"self","format":"text","recipient_trust_zone":"unknown"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "agent@example.com") {
		t.Error("unknown zone export should not contain email")
	}
	if strings.Contains(result, "555-0000") {
		t.Error("unknown zone export should not contain phone")
	}
}

func TestOwnerContact_ConfiguredName(t *testing.T) {
	tools := newTestTools(t)
	tools.SetOwnerContactName("Aimee")

	_, err := tools.SaveContact(`{"name":"Aimee","kind":"individual","facts":{"email":"aimee@example.com"}}`)
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, tools, "Aimee", "household")

	result, err := tools.OwnerContact(`{}`)
	if err != nil {
		t.Fatalf("OwnerContact() error = %v", err)
	}
	if !strings.Contains(result, "**Aimee**") {
		t.Fatalf("OwnerContact() = %q, want contact name", result)
	}
	if !strings.Contains(result, "aimee@example.com") {
		t.Fatalf("OwnerContact() = %q, want contact property details", result)
	}
}

func TestOwnerContact_ConfiguredOperatorID(t *testing.T) {
	tools := newTestTools(t)
	if _, err := tools.SaveContact(`{"name":"Original Name","kind":"individual","facts":{"email":"operator@example.com"}}`); err != nil {
		t.Fatal(err)
	}
	operator, err := tools.store.FindByName("Original Name")
	if err != nil {
		t.Fatal(err)
	}
	tools.ConfigureOperatorContactID(operator.ID)

	operator.FormattedName = "Renamed Operator"
	if _, err := tools.store.Upsert(operator); err != nil {
		t.Fatal(err)
	}

	result, err := tools.OwnerContact(`{}`)
	if err != nil {
		t.Fatalf("OwnerContact() error = %v", err)
	}
	if !strings.Contains(result, "**Renamed Operator**") {
		t.Fatalf("OwnerContact() = %q, want UUID-selected renamed contact", result)
	}
}

func TestOwnerContact_FallsBackToSoleAdmin(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Nugget","kind":"individual","facts":{"phone":"+15551234567"}}`)
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, tools, "Nugget", "admin")

	result, err := tools.OwnerContact(`{}`)
	if err != nil {
		t.Fatalf("OwnerContact() error = %v", err)
	}
	if !strings.Contains(result, "**Nugget**") {
		t.Fatalf("OwnerContact() = %q, want admin contact name", result)
	}
}

func TestOwnerContact_IncludesActiveOwnerChannels(t *testing.T) {
	tools := newTestTools(t)
	tools.SetOwnerContactName("Aimee")
	tools.SetOwnerActivitySource(func() []OwnerChannelActivity {
		return []OwnerChannelActivity{
			{
				Channel:        "owu",
				LoopID:         "loop-1",
				LoopName:       "owu/task",
				ConversationID: "owu-conv-1",
				State:          "processing",
				LastActive:     time.Date(2026, 4, 5, 19, 0, 0, 0, time.UTC),
			},
			{
				Channel:        "signal",
				LoopID:         "loop-2",
				LoopName:       "signal/+15551234567",
				ConversationID: "signal-15551234567",
				ContactName:    "Aimee",
				State:          "idle",
				LastActive:     time.Date(2026, 4, 5, 18, 45, 0, 0, time.UTC),
			},
		}
	})

	_, err := tools.SaveContact(`{"name":"Aimee","kind":"individual","facts":{"email":"aimee@example.com"}}`)
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, tools, "Aimee", "household")

	result, err := tools.OwnerContact(`{}`)
	if err != nil {
		t.Fatalf("OwnerContact() error = %v", err)
	}
	if !strings.Contains(result, "active_owner_channels") {
		t.Fatalf("OwnerContact() = %q, want structured owner channel summary", result)
	}
	if !strings.Contains(result, "\"owu\"") || !strings.Contains(result, "\"signal\"") {
		t.Fatalf("OwnerContact() = %q, want both owner channels", result)
	}
	if !strings.Contains(result, "last_active_delta") || strings.Contains(result, "last_active\"") {
		t.Fatalf("OwnerContact() = %q, want delta-oriented owner channel activity", result)
	}
}

func TestOwnerContact_AmbiguousAdminRequiresConfig(t *testing.T) {
	tools := newTestTools(t)

	for _, name := range []string{"Aimee", "Nugget"} {
		_, err := tools.SaveContact(fmt.Sprintf(`{"name":%q,"kind":"individual"}`, name))
		if err != nil {
			t.Fatal(err)
		}
		setZone(t, tools, name, "admin")
	}

	_, err := tools.OwnerContact(`{}`)
	if err == nil {
		t.Fatal("expected ambiguity error when multiple admin contacts exist")
	}
	if !strings.Contains(err.Error(), "identity.operator_contact_id") {
		t.Fatalf("error = %v, want owner contact config hint", err)
	}
}

func TestOwnerContact_LimitsOwnerActivitySummary(t *testing.T) {
	tools := newTestTools(t)
	tools.SetOwnerContactName("Aimee")
	tools.SetOwnerActivitySource(func() []OwnerChannelActivity {
		out := make([]OwnerChannelActivity, 0, ownerActivitySummaryLimit+3)
		for i := 0; i < ownerActivitySummaryLimit+3; i++ {
			out = append(out, OwnerChannelActivity{
				Channel:        "owu",
				LoopID:         fmt.Sprintf("loop-%d", i),
				ConversationID: fmt.Sprintf("conv-%d", i),
				LastActive:     time.Date(2026, 4, 5, 19, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Minute),
			})
		}
		return out
	})

	if _, err := tools.SaveContact(`{"name":"Aimee","kind":"individual","facts":{"email":"aimee@example.com"}}`); err != nil {
		t.Fatal(err)
	}

	result, err := tools.OwnerContact(`{}`)
	setZone(t, tools, "Aimee", "household")
	if err != nil {
		t.Fatalf("OwnerContact() error = %v", err)
	}

	start := strings.Index(result, "```json\n")
	endRel := -1
	if start >= 0 {
		endRel = strings.Index(result[start+8:], "\n```")
	}
	if start < 0 || endRel < 0 {
		t.Fatalf("OwnerContact() = %q, want embedded JSON summary", result)
	}
	end := start + 8 + endRel

	var payload struct {
		ActiveOwnerChannels []map[string]any `json:"active_owner_channels"`
		Total               int              `json:"total"`
		Displayed           int              `json:"displayed"`
		Omitted             int              `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(result[start+8:end]), &payload); err != nil {
		t.Fatalf("unmarshal owner activity summary: %v", err)
	}
	if payload.Total != ownerActivitySummaryLimit+3 {
		t.Fatalf("summary total = %d, want %d", payload.Total, ownerActivitySummaryLimit+3)
	}
	if len(payload.ActiveOwnerChannels) != ownerActivitySummaryLimit {
		t.Fatalf("visible owner channels = %d, want %d", len(payload.ActiveOwnerChannels), ownerActivitySummaryLimit)
	}
	if payload.Displayed != ownerActivitySummaryLimit {
		t.Fatalf("displayed = %d, want %d", payload.Displayed, ownerActivitySummaryLimit)
	}
	if payload.Omitted != 3 {
		t.Fatalf("omitted = %d, want 3", payload.Omitted)
	}
}

func TestExportVCF_NameRequired(t *testing.T) {
	tools := newTestTools(t)
	_, err := tools.ExportVCF(`{}`)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestExportVCF_NotFound(t *testing.T) {
	tools := newTestTools(t)
	_, err := tools.ExportVCF(`{"name":"Nobody"}`)
	if err == nil {
		t.Error("expected error for non-existent contact")
	}
}

func TestExportAllVCF_Basic(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"All A","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"All B","kind":"org"}`)
	_, _ = tools.SaveContact(`{"name":"All C","kind":"individual"}`)

	result, err := tools.ExportAllVCF(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "3 contacts") {
		t.Errorf("result = %q, want to contain '3 contacts'", result)
	}
	// Clean up temp file.
	path := strings.TrimPrefix(result, "Exported 3 contacts to ")
	t.Cleanup(func() { os.Remove(path) })
}

func TestExportAllVCF_FilterByKind(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Kind A","kind":"individual"}`)
	_, _ = tools.SaveContact(`{"name":"Kind B","kind":"org"}`)

	result, err := tools.ExportAllVCF(`{"kind":"org"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 contacts") {
		t.Errorf("result = %q, want to contain '1 contacts'", result)
	}
	path := strings.TrimPrefix(result, "Exported 1 contacts to ")
	t.Cleanup(func() { os.Remove(path) })
}

func TestExportAllVCF_FilterByTrustZone(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"TZ A","kind":"individual"}`)
	setZone(t, tools, "TZ A", "trusted")
	_, _ = tools.SaveContact(`{"name":"TZ B","kind":"individual"}`)
	setZone(t, tools, "TZ B", "known")

	result, err := tools.ExportAllVCF(`{"trust_zone":"trusted"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 contacts") {
		t.Errorf("result = %q, want to contain '1 contacts'", result)
	}
	path := strings.TrimPrefix(result, "Exported 1 contacts to ")
	t.Cleanup(func() { os.Remove(path) })
}

func TestExportAllVCF_FilterBothKindAndTrustZone(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Both A","kind":"individual"}`)
	setZone(t, tools, "Both A", "trusted")
	_, _ = tools.SaveContact(`{"name":"Both B","kind":"org"}`)
	setZone(t, tools, "Both B", "trusted")
	_, _ = tools.SaveContact(`{"name":"Both C","kind":"individual"}`)
	setZone(t, tools, "Both C", "known")

	result, err := tools.ExportAllVCF(`{"kind":"individual","trust_zone":"trusted"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 contacts") {
		t.Errorf("result = %q, want '1 contacts' (intersection), got %q", result, result)
	}
	path := strings.TrimPrefix(result, "Exported 1 contacts to ")
	t.Cleanup(func() { os.Remove(path) })
}

func TestExportAllVCF_Empty(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.ExportAllVCF(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No contacts to export" {
		t.Errorf("result = %q, want 'No contacts to export'", result)
	}
}

func TestImportVCF_FromText(t *testing.T) {
	tools := newTestTools(t)

	vcardText := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Import Alice\r\nN:Alice;Import;;;\r\nEMAIL:import.alice@example.com\r\nEND:VCARD"

	result, err := tools.ImportVCF(`{"text":"` + strings.ReplaceAll(vcardText, "\r\n", "\\r\\n") + `"}`)
	if err != nil {
		t.Fatalf("ImportVCF() error = %v", err)
	}
	if !strings.Contains(result, "1 created") {
		t.Errorf("result = %q, want '1 created'", result)
	}

	c, err := tools.store.FindByName("Import Alice")
	if err != nil {
		t.Fatalf("imported contact not found: %v", err)
	}
	if c.GivenName != "Import" {
		t.Errorf("GivenName = %q, want %q", c.GivenName, "Import")
	}
}

func TestImportVCF_MergeByEmail(t *testing.T) {
	tools := newTestTools(t)

	// Pre-existing contact with email.
	_, err := tools.SaveContact(`{"name":"Existing Bob","kind":"individual","ai_summary":"Original summary","facts":{"email":"bob@example.com"}}`)
	if err != nil {
		t.Fatal(err)
	}
	setZone(t, tools, "Existing Bob", "trusted")

	// Import a vCard with same email but different name and added fields.
	vcardText := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Robert Smith\r\nN:Smith;Robert;;;\r\nEMAIL:bob@example.com\r\nORG:Acme Corp\r\nEND:VCARD"

	result, err := tools.ImportVCF(`{"text":"` + strings.ReplaceAll(vcardText, "\r\n", "\\r\\n") + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 merged") {
		t.Errorf("result = %q, want '1 merged'", result)
	}

	// Verify merge filled empty fields.
	c, err := tools.store.FindByName("Existing Bob")
	if err != nil {
		t.Fatal(err)
	}
	if c.Org != "Acme Corp" {
		t.Errorf("Org = %q, want %q (should be filled from import)", c.Org, "Acme Corp")
	}
	// TrustZone must not be overwritten.
	if c.TrustZone != "trusted" {
		t.Errorf("TrustZone = %q, want %q (must be preserved)", c.TrustZone, "trusted")
	}
	// AISummary must not be overwritten.
	if c.AISummary != "Original summary" {
		t.Errorf("AISummary = %q, want %q (must be preserved)", c.AISummary, "Original summary")
	}
}

func TestImportVCF_MergeByName(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Name Match","kind":"individual"}`)
	if err != nil {
		t.Fatal(err)
	}

	vcardText := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Name Match\r\nORG:NewCorp\r\nEND:VCARD"

	result, err := tools.ImportVCF(`{"text":"` + strings.ReplaceAll(vcardText, "\r\n", "\\r\\n") + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 merged") {
		t.Errorf("result = %q, want '1 merged'", result)
	}

	c, err := tools.store.FindByName("Name Match")
	if err != nil {
		t.Fatal(err)
	}
	if c.Org != "NewCorp" {
		t.Errorf("Org = %q, want %q", c.Org, "NewCorp")
	}
}

func TestImportVCF_DryRun(t *testing.T) {
	tools := newTestTools(t)

	vcardText := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:DryRun Contact\r\nEND:VCARD"

	result, err := tools.ImportVCF(`{"text":"` + strings.ReplaceAll(vcardText, "\r\n", "\\r\\n") + `","dry_run":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Dry run") {
		t.Errorf("result = %q, want 'Dry run'", result)
	}
	if !strings.Contains(result, "Would create") {
		t.Errorf("result = %q, want 'Would create'", result)
	}

	// Verify no contact was created.
	_, err = tools.store.FindByName("DryRun Contact")
	if err == nil {
		t.Error("dry run should not create contacts")
	}
}

func TestImportVCF_NoMerge(t *testing.T) {
	tools := newTestTools(t)

	// Pre-create a contact with email.
	_, err := tools.SaveContact(`{"name":"NoMerge Existing","kind":"individual","facts":{"email":"nomerge@example.com"}}`)
	if err != nil {
		t.Fatal(err)
	}

	// Import a vCard with the same email but different name.
	// With merge=false it should create a new contact instead of merging.
	vcardText := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:NoMerge New\r\nEMAIL:nomerge@example.com\r\nEND:VCARD"

	result, err := tools.ImportVCF(`{"text":"` + strings.ReplaceAll(vcardText, "\r\n", "\\r\\n") + `","merge":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 created") {
		t.Errorf("result = %q, want '1 created' (merge disabled)", result)
	}

	// Both contacts should exist.
	_, err = tools.store.FindByName("NoMerge Existing")
	if err != nil {
		t.Error("original contact should still exist")
	}
	_, err = tools.store.FindByName("NoMerge New")
	if err != nil {
		t.Error("new contact should be created separately")
	}
}

func TestImportVCF_FromFile(t *testing.T) {
	tools := newTestTools(t)

	vcardText := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:File Contact\r\nEND:VCARD\r\n"

	f, err := os.CreateTemp("", "test-import-*.vcf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })

	if _, err := f.WriteString(vcardText); err != nil {
		t.Fatal(err)
	}
	f.Close()

	result, err := tools.ImportVCF(`{"path":"` + f.Name() + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "1 created") {
		t.Errorf("result = %q, want '1 created'", result)
	}
}

func TestImportVCF_RequiresPathOrText(t *testing.T) {
	tools := newTestTools(t)
	_, err := tools.ImportVCF(`{}`)
	if err == nil {
		t.Error("expected error when neither path nor text provided")
	}
}

func TestExportVCFQR_Basic(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"QR Contact","kind":"individual"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.ExportVCFQR(`{"name":"QR Contact"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "QR code vCard written to") {
		t.Errorf("result = %q, want 'QR code vCard written to'", result)
	}

	// Extract path and verify file exists and is non-empty.
	path := strings.TrimPrefix(result, "QR code vCard written to ")
	t.Cleanup(func() { os.Remove(path) })
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		t.Fatalf("QR file %q does not exist", path)
	}
	if info.Size() == 0 {
		t.Error("QR PNG file is empty")
	}
}

func TestExportVCFQR_SizeLimit(t *testing.T) {
	tools := newTestTools(t)

	// Create a contact with a very long note to exceed QR capacity.
	longNote := strings.Repeat("This is a very long note. ", 200)
	_, err := tools.SaveContact(`{"name":"Big QR","kind":"individual","note":"` + longNote + `"}`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tools.ExportVCFQR(`{"name":"Big QR"}`)
	if err == nil {
		t.Error("expected error for oversized vCard")
	}
	if err != nil && !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %v, want to mention 'too large'", err)
	}
}

func TestImportExportVCF_RoundTrip(t *testing.T) {
	tools := newTestTools(t)

	// Create a contact with properties.
	_, err := tools.SaveContact(`{"name":"Round Trip","kind":"individual","given_name":"Round","family_name":"Trip","org":"TestCorp","facts":{"email":"roundtrip@example.com","phone":"555-1234"}}`)
	if err != nil {
		t.Fatal(err)
	}

	// Export as text.
	text, err := tools.ExportVCF(`{"name":"Round Trip","format":"text"}`)
	if err != nil {
		t.Fatal(err)
	}

	// Write exported vCard to a temp file for import (avoids JSON
	// escaping issues with \r\n in vCard text).
	f, err := os.CreateTemp("", "roundtrip-*.vcf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Import into a fresh tools instance.
	tools2 := newTestTools(t)
	result, err := tools2.ImportVCF(`{"path":"` + f.Name() + `"}`)
	if err != nil {
		t.Fatalf("ImportVCF round-trip error = %v", err)
	}
	if !strings.Contains(result, "1 created") {
		t.Errorf("result = %q, want '1 created'", result)
	}

	// Verify the imported contact.
	c, err := tools2.store.FindByName("Round Trip")
	if err != nil {
		t.Fatal(err)
	}
	if c.Org != "TestCorp" {
		t.Errorf("Org = %q, want %q", c.Org, "TestCorp")
	}
	if c.GivenName != "Round" {
		t.Errorf("GivenName = %q, want %q", c.GivenName, "Round")
	}
}

// TestImportVCF_TrustZoneNotImportable pins the #1450 custody boundary
// on the sibling import path: a model-supplied vCard cannot mint an
// elevated contact through X-THANE-TRUST-ZONE — new contacts start at
// the default zone regardless of the header, on both the merge:false
// and unique-name create branches.
func TestImportVCF_TrustZoneNotImportable(t *testing.T) {
	tools := newTestTools(t)

	vcf := "BEGIN:VCARD\nVERSION:4.0\nFN:Mallory Escalation\nX-THANE-TRUST-ZONE:admin\nEND:VCARD"
	for _, merge := range []bool{false, true} {
		payload, _ := json.Marshal(map[string]any{"text": vcf, "merge": merge})
		if _, err := tools.ImportVCF(string(payload)); err != nil {
			t.Fatalf("ImportVCF(merge=%v): %v", merge, err)
		}
		c, err := tools.store.FindByName("Mallory Escalation")
		if err != nil {
			t.Fatalf("find imported (merge=%v): %v", merge, err)
		}
		if c.TrustZone != ZoneKnown {
			t.Errorf("merge=%v: imported contact zone = %q, want %q — X-THANE-TRUST-ZONE must not confer authority via a model tool", merge, c.TrustZone, ZoneKnown)
		}
		// Clean up so the second iteration exercises the create branch too.
		_ = tools.store.Delete(c.ID)
	}
}

// TestImportVCF_HAPersonNotImportable pins custody symmetry with trust
// zones (#1450): X-THANE-HA-PERSON is honored only by the CardDAV
// backend; a model-supplied vCard can neither set the binding nor
// smuggle the header in as a generic property.
func TestImportVCF_HAPersonNotImportable(t *testing.T) {
	tools := newTestTools(t)
	vcf := "BEGIN:VCARD\nVERSION:4.0\nFN:Sneaky Binder\nX-THANE-HA-PERSON:person.alice\nEND:VCARD"
	payload, _ := json.Marshal(map[string]any{"text": vcf, "merge": false})
	if _, err := tools.ImportVCF(string(payload)); err != nil {
		t.Fatalf("ImportVCF: %v", err)
	}
	c, err := tools.store.FindByName("Sneaky Binder")
	if err != nil {
		t.Fatal(err)
	}
	if entity, _, _ := tools.store.HAPersonEntity(c.ID); entity != "" {
		t.Errorf("model import set the HA person binding: %q", entity)
	}
	props, err := tools.store.GetPropertiesMap(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for k := range props {
		if strings.Contains(strings.ToLower(k), "ha-person") || strings.Contains(strings.ToLower(k), "ha_person") {
			t.Errorf("header rescued as property %q", k)
		}
	}
}
