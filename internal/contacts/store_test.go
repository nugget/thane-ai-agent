package contacts

import (
	"database/sql"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "thane-contacts-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	store, err := NewStore(tmpFile.Name(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestCreateAndGet(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{
		Name:         "Alice Johnson",
		Kind:         "person",
		Relationship: "colleague",
		Summary:      "Works at Anthropic",
	}

	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("expected non-nil UUID")
	}
	if created.Name != "Alice Johnson" {
		t.Errorf("Name = %q, want %q", created.Name, "Alice Johnson")
	}
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != "Alice Johnson" {
		t.Errorf("Get().Name = %q, want %q", got.Name, "Alice Johnson")
	}
	if got.Kind != "person" {
		t.Errorf("Get().Kind = %q, want %q", got.Kind, "person")
	}
	if got.Relationship != "colleague" {
		t.Errorf("Get().Relationship = %q, want %q", got.Relationship, "colleague")
	}
	if got.Summary != "Works at Anthropic" {
		t.Errorf("Get().Summary = %q, want %q", got.Summary, "Works at Anthropic")
	}
}

func TestUpsertByName(t *testing.T) {
	store := newTestStore(t)

	// Create initial contact.
	c := &Contact{Name: "Bob Smith", Kind: "person", Summary: "Original summary"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	// Update by re-using the returned ID.
	created.Summary = "Updated summary"
	created.Relationship = "friend"
	updated, err := store.Upsert(created)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if updated.ID != created.ID {
		t.Errorf("ID changed on update: got %s, want %s", updated.ID, created.ID)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Summary != "Updated summary" {
		t.Errorf("Summary = %q, want %q", got.Summary, "Updated summary")
	}
	if got.Relationship != "friend" {
		t.Errorf("Relationship = %q, want %q", got.Relationship, "friend")
	}
}

func TestSoftDelete(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Charlie Deleted", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Get should return not found.
	_, err = store.Get(created.ID)
	if err != sql.ErrNoRows {
		t.Errorf("Get() after delete: got err = %v, want sql.ErrNoRows", err)
	}

	// FindByName should return not found.
	_, err = store.FindByName("Charlie Deleted")
	if err != sql.ErrNoRows {
		t.Errorf("FindByName() after delete: got err = %v, want sql.ErrNoRows", err)
	}
}

func TestSoftDeleteNotFound(t *testing.T) {
	store := newTestStore(t)

	err := store.Delete(uuid.New())
	if err == nil {
		t.Error("Delete() non-existent contact should return error")
	}
}

func TestDeleteByName(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Delete Me", Kind: "person"}
	_, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if err := store.DeleteByName("delete me"); err != nil {
		t.Fatalf("DeleteByName() error = %v", err)
	}

	_, err = store.FindByName("Delete Me")
	if err != sql.ErrNoRows {
		t.Errorf("FindByName() after DeleteByName: got err = %v, want sql.ErrNoRows", err)
	}
}

func TestFindByName_CaseInsensitive(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Dana O'Brien", Kind: "person", Summary: "Data scientist"}
	_, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	tests := []struct {
		name  string
		query string
	}{
		{"exact case", "Dana O'Brien"},
		{"lowercase", "dana o'brien"},
		{"uppercase", "DANA O'BRIEN"},
		{"mixed case", "dAnA o'BrIeN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.FindByName(tt.query)
			if err != nil {
				t.Fatalf("FindByName(%q) error = %v", tt.query, err)
			}
			if got.Name != "Dana O'Brien" {
				t.Errorf("FindByName(%q).Name = %q, want %q", tt.query, got.Name, "Dana O'Brien")
			}
		})
	}
}

func TestFindByName_NotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.FindByName("Nobody Here")
	if err != sql.ErrNoRows {
		t.Errorf("FindByName() non-existent: got err = %v, want sql.ErrNoRows", err)
	}
}

func TestDefaultKind(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "No Kind Set"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.Kind != "person" {
		t.Errorf("Kind = %q, want %q", created.Kind, "person")
	}
}

func TestSearch_FTS(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available")
	}

	contacts := []*Contact{
		{Name: "Eve Engineer", Kind: "person", Relationship: "colleague", Summary: "Backend developer at Acme Corp"},
		{Name: "Frank Finance", Kind: "person", Relationship: "vendor", Summary: "Financial advisor specializing in tax planning"},
		{Name: "Acme Corp", Kind: "company", Summary: "Technology company in San Francisco"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatalf("Upsert(%q) error = %v", c.Name, err)
		}
	}

	tests := []struct {
		name     string
		query    string
		wantMin  int
		wantName string
	}{
		{"by name", "Eve", 1, "Eve Engineer"},
		{"by summary word", "tax", 1, "Frank Finance"},
		{"by company name", "Acme", 1, "Acme Corp"},
		{"by relationship", "vendor", 1, "Frank Finance"},
		{"no results", "xyznonexistent", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.Search(tt.query)
			if err != nil {
				t.Fatalf("Search(%q) error = %v", tt.query, err)
			}
			if len(results) < tt.wantMin {
				t.Errorf("Search(%q) returned %d results, want >= %d", tt.query, len(results), tt.wantMin)
			}
			if tt.wantName != "" {
				found := false
				for _, r := range results {
					if r.Name == tt.wantName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Search(%q) did not return contact %q", tt.query, tt.wantName)
				}
			}
		})
	}
}

func TestSearch_ExcludesDeleted(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available")
	}

	c := &Contact{Name: "Grace Ghosted", Kind: "person", Summary: "Will be deleted soon"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	results, err := store.Search("Grace")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result before delete, got %d", len(results))
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatal(err)
	}

	results, err = store.Search("Grace")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}
}

func TestSearch_LIKEFallback(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Hank Helper", Kind: "person", Summary: "Friendly handyman"}
	if _, err := store.Upsert(c); err != nil {
		t.Fatal(err)
	}

	// Test LIKE path directly regardless of FTS availability.
	results, err := store.searchLIKE("handyman")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("searchLIKE returned %d results, want 1", len(results))
	}
	if results[0].Name != "Hank Helper" {
		t.Errorf("Name = %q, want %q", results[0].Name, "Hank Helper")
	}
}

func TestSetFact_GetFacts(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Ivy Info", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SetFact(created.ID, "email", "ivy@example.com"); err != nil {
		t.Fatalf("SetFact(email) error = %v", err)
	}
	if err := store.SetFact(created.ID, "phone", "555-1234"); err != nil {
		t.Fatalf("SetFact(phone) error = %v", err)
	}

	facts, err := store.GetFacts(created.ID)
	if err != nil {
		t.Fatalf("GetFacts() error = %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("GetFacts() returned %d facts, want 2", len(facts))
	}
	if len(facts["email"]) != 1 || facts["email"][0] != "ivy@example.com" {
		t.Errorf("email = %v, want [ivy@example.com]", facts["email"])
	}
	if len(facts["phone"]) != 1 || facts["phone"][0] != "555-1234" {
		t.Errorf("phone = %v, want [555-1234]", facts["phone"])
	}
}

func TestSetFact_MultiValue(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Jack MultiPhone", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	// SetFact adds multiple values for the same key.
	if err := store.SetFact(created.ID, "phone", "555-1111"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFact(created.ID, "phone", "555-2222"); err != nil {
		t.Fatal(err)
	}

	facts, err := store.GetFacts(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts["phone"]) != 2 {
		t.Errorf("expected 2 phone values, got %d: %v", len(facts["phone"]), facts["phone"])
	}
}

func TestSetFact_Idempotent(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Jack Idempotent", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	// Setting the same triple twice should be a no-op.
	if err := store.SetFact(created.ID, "email", "jack@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFact(created.ID, "email", "jack@example.com"); err != nil {
		t.Fatal(err)
	}

	facts, err := store.GetFacts(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts["email"]) != 1 {
		t.Errorf("expected 1 email value after duplicate SetFact, got %d", len(facts["email"]))
	}
}

func TestReplaceFact(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Replace Tester", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	// Add two phone numbers then replace with one.
	_ = store.SetFact(created.ID, "phone", "555-1111")
	_ = store.SetFact(created.ID, "phone", "555-2222")

	if err := store.ReplaceFact(created.ID, "phone", "555-3333"); err != nil {
		t.Fatal(err)
	}

	facts, err := store.GetFacts(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts["phone"]) != 1 || facts["phone"][0] != "555-3333" {
		t.Errorf("phone = %v, want [555-3333]", facts["phone"])
	}
}

func TestDeleteFact(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Delete Fact Tester", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	_ = store.SetFact(created.ID, "phone", "555-1111")
	_ = store.SetFact(created.ID, "phone", "555-2222")

	if err := store.DeleteFact(created.ID, "phone", "555-1111"); err != nil {
		t.Fatal(err)
	}

	facts, err := store.GetFacts(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts["phone"]) != 1 || facts["phone"][0] != "555-2222" {
		t.Errorf("phone = %v, want [555-2222]", facts["phone"])
	}
}

func TestDeleteFact_NotFound(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "No Such Fact", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	err = store.DeleteFact(created.ID, "phone", "nonexistent")
	if err == nil {
		t.Error("expected error deleting nonexistent fact")
	}
}

func TestGetWithFacts(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Kelly Complete", Kind: "person", Summary: "Has facts"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFact(created.ID, "employer", "Widgets Inc"); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetWithFacts(created.ID)
	if err != nil {
		t.Fatalf("GetWithFacts() error = %v", err)
	}
	if got.Name != "Kelly Complete" {
		t.Errorf("Name = %q, want %q", got.Name, "Kelly Complete")
	}
	if len(got.Facts["employer"]) != 1 || got.Facts["employer"][0] != "Widgets Inc" {
		t.Errorf("Facts[employer] = %v, want [Widgets Inc]", got.Facts["employer"])
	}
}

func TestFindByFact(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{Name: "Leo Lamp", Kind: "person"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFact(created1.ID, "email", "leo@example.com"); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{Name: "Mia Mirror", Kind: "person"}
	created2, err := store.Upsert(c2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFact(created2.ID, "email", "mia@example.com"); err != nil {
		t.Fatal(err)
	}

	// Search by email domain.
	results, err := store.FindByFact("email", "example.com")
	if err != nil {
		t.Fatalf("FindByFact() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("FindByFact() returned %d results, want 2", len(results))
	}

	// Search for specific email.
	results, err = store.FindByFact("email", "leo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("FindByFact() returned %d results, want 1", len(results))
	}
	if results[0].Name != "Leo Lamp" {
		t.Errorf("Name = %q, want %q", results[0].Name, "Leo Lamp")
	}
}

func TestListByKind(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{Name: "PersonA", Kind: "person"},
		{Name: "PersonB", Kind: "person"},
		{Name: "CompanyA", Kind: "company"},
		{Name: "OrgA", Kind: "organization"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		kind string
		want int
	}{
		{"person", 2},
		{"company", 1},
		{"organization", 1},
		{"other", 0},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			results, err := store.ListByKind(tt.kind)
			if err != nil {
				t.Fatalf("ListByKind(%q) error = %v", tt.kind, err)
			}
			if len(results) != tt.want {
				t.Errorf("ListByKind(%q) returned %d, want %d", tt.kind, len(results), tt.want)
			}
		})
	}
}

func TestListAll(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{Name: "Zara", Kind: "person"},
		{Name: "Adam", Kind: "person"},
		{Name: "Mid Corp", Kind: "company"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatal(err)
		}
	}

	results, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("ListAll() returned %d, want 3", len(results))
	}
	// Should be sorted by name.
	if results[0].Name != "Adam" {
		t.Errorf("first contact = %q, want %q", results[0].Name, "Adam")
	}
}

func TestResurrectSoftDeleted(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Zombie Contact", Kind: "person", Summary: "Will be deleted then resurrected"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatal(err)
	}

	// Resurrect by upserting with same ID.
	created.Summary = "Back from the dead"
	_, err = store.Upsert(created)
	if err != nil {
		t.Fatalf("Upsert() resurrect error = %v", err)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() after resurrect error = %v", err)
	}
	if got.Summary != "Back from the dead" {
		t.Errorf("Summary = %q, want %q", got.Summary, "Back from the dead")
	}
}

func TestEmbeddingEncodeDecode(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 3.14159, -0.001}

	encoded := encodeEmbedding(original)
	decoded := decodeEmbedding(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("value %d: got %f, want %f", i, decoded[i], original[i])
		}
	}
}

func TestEmbeddingEncodeEmpty(t *testing.T) {
	if encoded := encodeEmbedding(nil); encoded != nil {
		t.Errorf("expected nil for nil input, got %v", encoded)
	}
	if encoded := encodeEmbedding([]float32{}); encoded != nil {
		t.Errorf("expected nil for empty input, got %v", encoded)
	}
}

func TestEmbeddingDecodeEmpty(t *testing.T) {
	if decoded := decodeEmbedding(nil); decoded != nil {
		t.Errorf("expected nil for nil input, got %v", decoded)
	}
	if decoded := decodeEmbedding([]byte{}); decoded != nil {
		t.Errorf("expected nil for empty input, got %v", decoded)
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float32
		expected float32
	}{
		{"identical vectors", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"orthogonal vectors", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0},
		{"opposite vectors", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0},
		{"different lengths", []float32{1, 2}, []float32{1, 2, 3}, 0.0},
		{"zero vector", []float32{0, 0, 0}, []float32{1, 2, 3}, 0.0},
		{"empty", []float32{}, []float32{}, 0.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if math.Abs(float64(got-tc.expected)) > 0.0001 {
				t.Errorf("got %f, want %f", got, tc.expected)
			}
		})
	}
}

func TestSemanticSearch(t *testing.T) {
	store := newTestStore(t)

	// Create contacts and set embeddings.
	c1 := &Contact{Name: "Near Match", Kind: "person"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	// Embedding close to query.
	if err := store.SetEmbedding(created1.ID, []float32{0.9, 0.1, 0.0}); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{Name: "Far Match", Kind: "person"}
	created2, err := store.Upsert(c2)
	if err != nil {
		t.Fatal(err)
	}
	// Embedding far from query.
	if err := store.SetEmbedding(created2.ID, []float32{0.0, 0.0, 1.0}); err != nil {
		t.Fatal(err)
	}

	c3 := &Contact{Name: "No Embedding", Kind: "person"}
	if _, err := store.Upsert(c3); err != nil {
		t.Fatal(err)
	}

	query := []float32{1.0, 0.0, 0.0}
	contacts, scores, err := store.SemanticSearch(query, 2)
	if err != nil {
		t.Fatalf("SemanticSearch() error = %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("SemanticSearch() returned %d contacts, want 2", len(contacts))
	}

	// First result should be the nearest.
	if contacts[0].Name != "Near Match" {
		t.Errorf("first result = %q, want %q", contacts[0].Name, "Near Match")
	}
	if scores[0] < scores[1] {
		t.Errorf("first score (%f) should be >= second score (%f)", scores[0], scores[1])
	}
}

func TestSemanticSearch_ExcludesDeleted(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Deleted Embedded", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbedding(created.ID, []float32{1.0, 0.0, 0.0}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(created.ID); err != nil {
		t.Fatal(err)
	}

	contacts, _, err := store.SemanticSearch([]float32{1.0, 0.0, 0.0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 0 {
		t.Errorf("SemanticSearch returned %d deleted contacts, want 0", len(contacts))
	}
}

func TestGetContactsWithoutEmbeddings(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{Name: "Has Embedding", Kind: "person"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbedding(created1.ID, []float32{1.0, 0.0}); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{Name: "No Embedding", Kind: "person"}
	if _, err := store.Upsert(c2); err != nil {
		t.Fatal(err)
	}

	contacts, err := store.GetContactsWithoutEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts without embeddings, want 1", len(contacts))
	}
	if contacts[0].Name != "No Embedding" {
		t.Errorf("Name = %q, want %q", contacts[0].Name, "No Embedding")
	}
}

func TestStats(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{Name: "P1", Kind: "person"},
		{Name: "P2", Kind: "person"},
		{Name: "C1", Kind: "company"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatal(err)
		}
	}

	stats := store.Stats()
	if stats["total"] != 3 {
		t.Errorf("total = %v, want 3", stats["total"])
	}
	kinds, ok := stats["kinds"].(map[string]int)
	if !ok {
		t.Fatal("kinds not a map[string]int")
	}
	if kinds["person"] != 2 {
		t.Errorf("kinds[person] = %d, want 2", kinds["person"])
	}
	if kinds["company"] != 1 {
		t.Errorf("kinds[company] = %d, want 1", kinds["company"])
	}
}

func TestSanitizeFTS5Query(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple word", "hello", `"hello"`},
		{"two words", "Alice Johnson", `"Alice" OR "Johnson"`},
		{"special chars", "o'brien", `"o'brien"`},
		{"empty", "", ""},
		{"whitespace only", "   \t\n  ", ""},
		{"with quotes", `say "hello"`, `"say" OR """hello"""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFTS5Query(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFTS5Query(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFTS5Enabled(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available in test environment")
	}
}

func TestSemanticSearch_ZeroLimit(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Limit Zero", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbedding(created.ID, []float32{1.0, 0.0, 0.0}); err != nil {
		t.Fatal(err)
	}

	contacts, scores, err := store.SemanticSearch([]float32{1.0, 0.0, 0.0}, 0)
	if err != nil {
		t.Fatalf("SemanticSearch(limit=0) error = %v", err)
	}
	if contacts != nil {
		t.Errorf("expected nil contacts for limit=0, got %d", len(contacts))
	}
	if scores != nil {
		t.Errorf("expected nil scores for limit=0, got %d", len(scores))
	}

	// Negative limit should also return empty.
	contacts, scores, err = store.SemanticSearch([]float32{1.0, 0.0, 0.0}, -5)
	if err != nil {
		t.Fatalf("SemanticSearch(limit=-5) error = %v", err)
	}
	if contacts != nil {
		t.Errorf("expected nil contacts for limit=-5, got %d", len(contacts))
	}
	if scores != nil {
		t.Errorf("expected nil scores for limit=-5, got %d", len(scores))
	}
}

func TestUpsert_TrustZoneDefault(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "No Zone Set", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.TrustZone != "known" {
		t.Errorf("TrustZone = %q, want %q", created.TrustZone, "known")
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.TrustZone != "known" {
		t.Errorf("Get().TrustZone = %q, want %q", got.TrustZone, "known")
	}
}

func TestUpsert_TrustZoneSet(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Trusted Friend", Kind: "person", TrustZone: "trusted"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.TrustZone != "trusted" {
		t.Errorf("TrustZone = %q, want %q", created.TrustZone, "trusted")
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.TrustZone != "trusted" {
		t.Errorf("Get().TrustZone = %q, want %q", got.TrustZone, "trusted")
	}
}

func TestUpsert_TrustZoneValidation(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "Bad Zone", Kind: "person", TrustZone: "superadmin"}
	_, err := store.Upsert(c)
	if err == nil {
		t.Error("expected error for invalid trust zone, got nil")
	}
	if !strings.Contains(err.Error(), "invalid trust zone") {
		t.Errorf("error = %q, want to contain 'invalid trust zone'", err.Error())
	}
}

func TestUpsert_TrustZoneOwner(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{Name: "The Owner", Kind: "person", TrustZone: "owner"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.TrustZone != "owner" {
		t.Errorf("TrustZone = %q, want %q", created.TrustZone, "owner")
	}
}

func TestFindByTrustZone(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{Name: "Owner A", Kind: "person", TrustZone: "owner"},
		{Name: "Trusted B", Kind: "person", TrustZone: "trusted"},
		{Name: "Trusted C", Kind: "person", TrustZone: "trusted"},
		{Name: "Known D", Kind: "person", TrustZone: "known"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatalf("Upsert(%q) error = %v", c.Name, err)
		}
	}

	tests := []struct {
		zone string
		want int
	}{
		{"owner", 1},
		{"trusted", 2},
		{"known", 1},
		{"unknown", 0},
	}

	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			results, err := store.FindByTrustZone(tt.zone)
			if err != nil {
				t.Fatalf("FindByTrustZone(%q) error = %v", tt.zone, err)
			}
			if len(results) != tt.want {
				t.Errorf("FindByTrustZone(%q) returned %d, want %d", tt.zone, len(results), tt.want)
			}
		})
	}
}

func TestMigrate_TrustLevelFacts(t *testing.T) {
	// Create a store, manually insert a contact with trust_level fact,
	// then trigger migration and verify the trust_zone was set.
	store := newTestStore(t)

	c := &Contact{Name: "Legacy Friend", Kind: "person"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	// Manually insert a trust_level fact (simulating legacy data).
	_, err = store.db.Exec(
		`INSERT INTO contact_facts (contact_id, key, value, updated_at) VALUES (?, 'trust_level', 'Close friend', ?)`,
		created.ID.String(), "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the contact starts at "known".
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustZone != "known" {
		t.Fatalf("TrustZone before migration = %q, want %q", got.TrustZone, "known")
	}

	// Run migration again.
	store.migrateTrustLevelFacts()

	got, err = store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustZone != "trusted" {
		t.Errorf("TrustZone after migration = %q, want %q", got.TrustZone, "trusted")
	}
}

func TestMapTrustLevelToZone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Close friend", "trusted"},
		{"trusted ally", "trusted"},
		{"family member", "trusted"},
		{"close collaborator", "trusted"},
		{"TRUSTED FRIEND", "trusted"},
		{"acquaintance", "known"},
		{"vendor", "known"},
		{"random person", "known"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapTrustLevelToZone(tt.input)
			if got != tt.want {
				t.Errorf("mapTrustLevelToZone(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUpsert_DuplicateActiveName(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{Name: "Unique Person", Kind: "person"}
	if _, err := store.Upsert(c1); err != nil {
		t.Fatal(err)
	}

	// Inserting a second contact with the same name (case-insensitive)
	// should fail due to the unique index on active contacts.
	c2 := &Contact{Name: "unique person", Kind: "person"}
	_, err := store.Upsert(c2)
	if err == nil {
		t.Error("expected error inserting duplicate active name, got nil")
	}
}
