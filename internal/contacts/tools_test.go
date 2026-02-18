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

func TestRememberContact_New(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.RememberContact(`{"name":"Alice Johnson","kind":"person","relationship":"colleague","summary":"Works at Anthropic"}`)
	if err != nil {
		t.Fatalf("RememberContact() error = %v", err)
	}
	if !strings.Contains(result, "Alice Johnson") {
		t.Errorf("result = %q, want to contain 'Alice Johnson'", result)
	}
	if !strings.Contains(result, "Remembered new contact") {
		t.Errorf("result = %q, want to contain 'Remembered new contact'", result)
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

func TestRememberContact_Update(t *testing.T) {
	tools := newTestTools(t)

	// Create.
	_, err := tools.RememberContact(`{"name":"Bob Smith","kind":"person","summary":"Original"}`)
	if err != nil {
		t.Fatal(err)
	}

	// Update.
	result, err := tools.RememberContact(`{"name":"Bob Smith","relationship":"friend","summary":"Updated"}`)
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

func TestRememberContact_WithFacts(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.RememberContact(`{"name":"Charlie Facts","kind":"person","facts":{"email":"charlie@example.com","phone":"555-9999"}}`)
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
	if facts["email"] != "charlie@example.com" {
		t.Errorf("email = %q, want %q", facts["email"], "charlie@example.com")
	}
	if facts["phone"] != "555-9999" {
		t.Errorf("phone = %q, want %q", facts["phone"], "555-9999")
	}
}

func TestRememberContact_WithEmbedding(t *testing.T) {
	tools := newTestTools(t)
	tools.SetEmbeddingClient(&fakeEmbedder{embedding: []float32{0.1, 0.2, 0.3}})

	_, err := tools.RememberContact(`{"name":"Embedded Eve","kind":"person","summary":"Has embedding"}`)
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

func TestRememberContact_NameRequired(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.RememberContact(`{"kind":"person"}`)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRecallContact_ByName(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.RememberContact(`{"name":"Dana Recall","kind":"person","relationship":"friend","summary":"Test recall"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.RecallContact(`{"name":"Dana Recall"}`)
	if err != nil {
		t.Fatalf("RecallContact() error = %v", err)
	}
	if !strings.Contains(result, "Dana Recall") {
		t.Errorf("result = %q, want to contain 'Dana Recall'", result)
	}
	if !strings.Contains(result, "friend") {
		t.Errorf("result = %q, want to contain 'friend'", result)
	}
}

func TestRecallContact_ByName_NotFound(t *testing.T) {
	tools := newTestTools(t)

	result, err := tools.RecallContact(`{"name":"Nobody"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No contact found") {
		t.Errorf("result = %q, want 'No contact found'", result)
	}
}

func TestRecallContact_ByQuery(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.RememberContact(`{"name":"Eve Search","kind":"person","summary":"Backend developer"}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := tools.RecallContact(`{"query":"developer"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Eve Search") {
		t.Errorf("result = %q, want to contain 'Eve Search'", result)
	}
}

func TestRecallContact_ByKind(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.RememberContact(`{"name":"PersonA","kind":"person"}`)
	_, _ = tools.RememberContact(`{"name":"CompanyA","kind":"company"}`)

	result, err := tools.RecallContact(`{"kind":"company"}`)
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

func TestRecallContact_ByFact(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.RememberContact(`{"name":"Frank Fact","kind":"person","facts":{"email":"frank@example.com"}}`)

	result, err := tools.RecallContact(`{"key":"email","value":"frank@example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Frank Fact") {
		t.Errorf("result = %q, want to contain 'Frank Fact'", result)
	}
}

func TestRecallContact_Stats(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.RememberContact(`{"name":"P1","kind":"person"}`)
	_, _ = tools.RememberContact(`{"name":"P2","kind":"person"}`)
	_, _ = tools.RememberContact(`{"name":"C1","kind":"company"}`)

	result, err := tools.RecallContact(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "3 contacts") {
		t.Errorf("result = %q, want to contain '3 contacts'", result)
	}
}

func TestForgetContact(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.RememberContact(`{"name":"Grace Forget","kind":"person"}`)

	result, err := tools.ForgetContact(`{"name":"Grace Forget"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Forgot contact") {
		t.Errorf("result = %q, want to contain 'Forgot contact'", result)
	}

	// Verify deleted.
	recall, err := tools.RecallContact(`{"name":"Grace Forget"}`)
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

func TestUpdateContactFact(t *testing.T) {
	tools := newTestTools(t)

	_, _ = tools.RememberContact(`{"name":"Hank Update","kind":"person"}`)

	result, err := tools.UpdateContactFact(`{"name":"Hank Update","key":"role","value":"Engineer"}`)
	if err != nil {
		t.Fatalf("UpdateContactFact() error = %v", err)
	}
	if !strings.Contains(result, "role") {
		t.Errorf("result = %q, want to contain 'role'", result)
	}

	// Verify fact was set.
	c, _ := tools.store.FindByName("Hank Update")
	facts, _ := tools.store.GetFacts(c.ID)
	if facts["role"] != "Engineer" {
		t.Errorf("role = %q, want %q", facts["role"], "Engineer")
	}
}

func TestUpdateContactFact_MissingArgs(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.UpdateContactFact(`{"name":"Someone"}`)
	if err == nil {
		t.Error("expected error for missing key/value")
	}
}

func TestUpdateContactFact_ContactNotFound(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.UpdateContactFact(`{"name":"Nobody","key":"email","value":"test@example.com"}`)
	if err == nil {
		t.Error("expected error for non-existent contact")
	}
}

func TestGenerateMissingEmbeddings(t *testing.T) {
	tools := newTestTools(t)
	tools.SetEmbeddingClient(&fakeEmbedder{embedding: []float32{0.5, 0.5}})

	_, _ = tools.RememberContact(`{"name":"NeedsEmbed","kind":"person","summary":"No embed yet"}`)

	// The remember already generates an embedding, so clear it for test.
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
