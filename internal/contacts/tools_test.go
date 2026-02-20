package contacts

import (
	"context"
	"strings"
	"testing"
)

// fakeEmbedder returns a fixed embedding for any text.
type fakeEmbedder struct {
	embedding []float32
	err       error
}

func (f *fakeEmbedder) Generate(_ context.Context, _ string) ([]float32, error) {
	return f.embedding, f.err
}

func newTestTools(t *testing.T) *Tools {
	t.Helper()
	store := newTestStore(t)
	return NewTools(store)
}

func TestSaveContact_New(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.SaveContact(`{"name":"Alice Johnson","kind":"person","relationship":"colleague","summary":"Works at Anthropic"}`)
	if err != nil {
		t.Fatalf("SaveContact() error = %v", err)
	}
	if !strings.Contains(result, "Alice Johnson") {
		t.Errorf("result = %q, want to contain 'Alice Johnson'", result)
	}
	if !strings.Contains(result, "Saved new contact") {
		t.Errorf("result = %q, want to contain 'Saved new contact'", result)
	}

	// Verify stored.
	c, err := tools.store.FindByName("Alice Johnson")
	if err != nil {
		t.Fatalf("FindByName() error = %v", err)
	}
	if c.Relationship != "colleague" {
		t.Errorf("Relationship = %q, want %q", c.Relationship, "colleague")
	}
}

func TestSaveContact_Update(t *testing.T) {
	tools := newTestTools(t)

	// Create.
	_, err := tools.SaveContact(`{"name":"Bob Smith","kind":"person","summary":"Original"}`)
	if err != nil {
		t.Fatal(err)
	}

	// Update.
	result, err := tools.SaveContact(`{"name":"Bob Smith","relationship":"friend","summary":"Updated"}`)
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
	if c.Summary != "Updated" {
		t.Errorf("Summary = %q, want %q", c.Summary, "Updated")
	}
	if c.Relationship != "friend" {
		t.Errorf("Relationship = %q, want %q", c.Relationship, "friend")
	}
}

func TestSaveContact_WithFacts(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Charlie Facts","kind":"person","facts":{"email":"charlie@example.com","phone":"555-9999"}}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Charlie Facts")
	if err != nil {
		t.Fatal(err)
	}

	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts["email"]) != 1 || facts["email"][0] != "charlie@example.com" {
		t.Errorf("email = %v, want [charlie@example.com]", facts["email"])
	}
	if len(facts["phone"]) != 1 || facts["phone"][0] != "555-9999" {
		t.Errorf("phone = %v, want [555-9999]", facts["phone"])
	}
}

func TestSaveContact_TopLevelFieldsRescued(t *testing.T) {
	tools := newTestTools(t)

	// Simulate what models actually send: email, phone, notes as top-level keys.
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
	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		"email": "shaded123@gmail.com",
		"phone": "555-1234",
		"notes": "First Thane beta tester candidate",
	}
	for key, want := range checks {
		vals := facts[key]
		if len(vals) != 1 || vals[0] != want {
			t.Errorf("fact %q = %v, want [%s]", key, vals, want)
		}
	}
}

func TestSaveContact_TopLevelFieldsMergeWithExplicitFacts(t *testing.T) {
	tools := newTestTools(t)

	// Both explicit facts and top-level fields present — both should be saved.
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
	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(facts["email"]) != 1 || facts["email"][0] != "mixed@example.com" {
		t.Errorf("email = %v, want [mixed@example.com]", facts["email"])
	}
	if len(facts["timezone"]) != 1 || facts["timezone"][0] != "America/Chicago" {
		t.Errorf("timezone = %v, want [America/Chicago]", facts["timezone"])
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
	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(facts["email"]) != 1 || facts["email"][0] != "explicit@example.com" {
		t.Errorf("email = %v, want [explicit@example.com] (explicit facts should win)", facts["email"])
	}
}

func TestSaveContact_TopLevelFieldsIgnoreNonString(t *testing.T) {
	tools := newTestTools(t)

	// Non-string top-level values should be ignored (not rescued).
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
	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	// email is a string — should be rescued.
	if len(facts["email"]) != 1 {
		t.Errorf("email should be rescued, got %v", facts["email"])
	}
	// count and active are not strings — should not be rescued.
	if _, exists := facts["count"]; exists {
		t.Error("non-string field 'count' should not be rescued")
	}
	if _, exists := facts["active"]; exists {
		t.Error("non-string field 'active' should not be rescued")
	}
}

func TestSaveContact_TopLevelFieldsOnUpdate(t *testing.T) {
	tools := newTestTools(t)

	// Create first.
	_, err := tools.SaveContact(`{"name": "Update Target", "kind": "person"}`)
	if err != nil {
		t.Fatal(err)
	}

	// Update with top-level fields.
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
	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts["organization"]) != 1 || facts["organization"][0] != "Acme Corp" {
		t.Errorf("organization = %v, want [Acme Corp]", facts["organization"])
	}
}

func TestSaveContact_WithEmbedding(t *testing.T) {
	tools := newTestTools(t)
	tools.SetEmbeddingClient(&fakeEmbedder{embedding: []float32{0.1, 0.2, 0.3}})

	_, err := tools.SaveContact(`{"name":"Embedded Eve","kind":"person","summary":"Has embedding"}`)
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

func TestSaveContact_NameRequired(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"kind":"person"}`)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestLookupContact_ByName(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Dana Recall","kind":"person","relationship":"friend","summary":"Test recall"}`)
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
	if !strings.Contains(result, "friend") {
		t.Errorf("result = %q, want to contain 'friend'", result)
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

	_, err := tools.SaveContact(`{"name":"Eve Search","kind":"person","summary":"Backend developer"}`)
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

	_, _ = tools.SaveContact(`{"name":"PersonA","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"CompanyA","kind":"company"}`)

	result, err := tools.LookupContact(`{"kind":"company"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "CompanyA") {
		t.Errorf("result = %q, want to contain 'CompanyA'", result)
	}
	if strings.Contains(result, "PersonA") {
		t.Errorf("result should not contain 'PersonA'")
	}
}

func TestLookupContact_ByFact(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"Frank Fact","kind":"person","facts":{"email":"frank@example.com"}}`)

	result, err := tools.LookupContact(`{"key":"email","value":"frank@example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Frank Fact") {
		t.Errorf("result = %q, want to contain 'Frank Fact'", result)
	}
}

func TestLookupContact_Stats(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"P1","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"P2","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"C1","kind":"company"}`)

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

	_, _ = tools.SaveContact(`{"name":"Grace Forget","kind":"person"}`)

	result, err := tools.ForgetContact(`{"name":"Grace Forget"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Forgot contact") {
		t.Errorf("result = %q, want to contain 'Forgot contact'", result)
	}

	// Verify deleted.
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

	_, _ = tools.SaveContact(`{"name":"Alpha","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"Beta","kind":"company"}`)
	_, _ = tools.SaveContact(`{"name":"Gamma","kind":"person"}`)

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

	_, _ = tools.SaveContact(`{"name":"PersonX","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"CompanyX","kind":"company"}`)

	result, err := tools.ListContacts(`{"kind":"company"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "CompanyX") {
		t.Errorf("result = %q, want to contain 'CompanyX'", result)
	}
	if strings.Contains(result, "PersonX") {
		t.Errorf("result should not contain 'PersonX'")
	}
}

func TestListContacts_WithLimit(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.SaveContact(`{"name":"A1","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"A2","kind":"person"}`)
	_, _ = tools.SaveContact(`{"name":"A3","kind":"person"}`)

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

func TestSaveContact_TrustZone(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Trusted Pal","kind":"person","trust_zone":"trusted"}`)
	if err != nil {
		t.Fatalf("SaveContact() error = %v", err)
	}

	c, err := tools.store.FindByName("Trusted Pal")
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustZone != "trusted" {
		t.Errorf("TrustZone = %q, want %q", c.TrustZone, "trusted")
	}
}

func TestSaveContact_TrustZoneUpdate(t *testing.T) {
	tools := newTestTools(t)

	// Create with default trust zone.
	_, err := tools.SaveContact(`{"name":"Zone Updater","kind":"person"}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Zone Updater")
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustZone != "known" {
		t.Fatalf("initial TrustZone = %q, want %q", c.TrustZone, "known")
	}

	// Update to trusted.
	_, err = tools.SaveContact(`{"name":"Zone Updater","trust_zone":"trusted"}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err = tools.store.FindByName("Zone Updater")
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustZone != "trusted" {
		t.Errorf("updated TrustZone = %q, want %q", c.TrustZone, "trusted")
	}

	// Update with empty trust_zone should preserve the existing value.
	_, err = tools.SaveContact(`{"name":"Zone Updater","summary":"New summary"}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err = tools.store.FindByName("Zone Updater")
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustZone != "trusted" {
		t.Errorf("TrustZone after empty update = %q, want %q (should be preserved)", c.TrustZone, "trusted")
	}
}

func TestSaveContact_TrustZoneNotRescuedAsFact(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.SaveContact(`{"name":"Zone Not Fact","trust_zone":"owner"}`)
	if err != nil {
		t.Fatal(err)
	}

	c, err := tools.store.FindByName("Zone Not Fact")
	if err != nil {
		t.Fatal(err)
	}

	facts, err := tools.store.GetFacts(c.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, exists := facts["trust_zone"]; exists {
		t.Error("trust_zone should not be rescued as a fact")
	}
	if c.TrustZone != "owner" {
		t.Errorf("TrustZone = %q, want %q", c.TrustZone, "owner")
	}
}

func TestFormatContact_TrustZone(t *testing.T) {
	c := &Contact{
		Name:      "Test Person",
		Kind:      "person",
		TrustZone: "trusted",
		Facts:     map[string][]string{},
	}

	result := formatContact(c)
	if !strings.Contains(result, "Kind: person | Trust: trusted") {
		t.Errorf("formatContact() = %q, want to contain 'Kind: person | Trust: trusted'", result)
	}
}

func TestGenerateMissingEmbeddings(t *testing.T) {
	tools := newTestTools(t)
	tools.SetEmbeddingClient(&fakeEmbedder{embedding: []float32{0.5, 0.5}})

	_, _ = tools.SaveContact(`{"name":"NeedsEmbed","kind":"person","summary":"No embed yet"}`)

	// The save already generates an embedding, so clear it for test.
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
