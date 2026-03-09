package contacts

import (
	"database/sql"
	"log/slog"
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
		FormattedName: "Alice Johnson",
		Kind:          "individual",
		AISummary:     "Works at Anthropic",
	}

	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("expected non-nil UUID")
	}
	if created.FormattedName != "Alice Johnson" {
		t.Errorf("FormattedName = %q, want %q", created.FormattedName, "Alice Johnson")
	}
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.FormattedName != "Alice Johnson" {
		t.Errorf("Get().FormattedName = %q, want %q", got.FormattedName, "Alice Johnson")
	}
	if got.Kind != "individual" {
		t.Errorf("Get().Kind = %q, want %q", got.Kind, "individual")
	}
	if got.AISummary != "Works at Anthropic" {
		t.Errorf("Get().AISummary = %q, want %q", got.AISummary, "Works at Anthropic")
	}
}

func TestUpsertByName(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Bob Smith", Kind: "individual", AISummary: "Original summary"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	created.AISummary = "Updated summary"
	created.Note = "Some notes"
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
	if got.AISummary != "Updated summary" {
		t.Errorf("AISummary = %q, want %q", got.AISummary, "Updated summary")
	}
	if got.Note != "Some notes" {
		t.Errorf("Note = %q, want %q", got.Note, "Some notes")
	}
}

func TestSoftDelete(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Charlie Deleted", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = store.Get(created.ID)
	if err != sql.ErrNoRows {
		t.Errorf("Get() after delete: got err = %v, want sql.ErrNoRows", err)
	}

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

	c := &Contact{FormattedName: "Delete Me", Kind: "individual"}
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

	c := &Contact{FormattedName: "Dana O'Brien", Kind: "individual", AISummary: "Data scientist"}
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
			if got.FormattedName != "Dana O'Brien" {
				t.Errorf("FindByName(%q).FormattedName = %q, want %q", tt.query, got.FormattedName, "Dana O'Brien")
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

	c := &Contact{FormattedName: "No Kind Set"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if created.Kind != "individual" {
		t.Errorf("Kind = %q, want %q", created.Kind, "individual")
	}
}

func TestUpsert_InvalidKind(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Bad Kind", Kind: "person"}
	_, err := store.Upsert(c)
	if err == nil {
		t.Error("expected error for invalid kind, got nil")
	}
	if !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("error = %q, want to contain 'invalid kind'", err.Error())
	}
}

func TestSearch_FTS(t *testing.T) {
	store := newTestStore(t)
	if !store.ftsEnabled {
		t.Skip("FTS5 not available")
	}

	contacts := []*Contact{
		{FormattedName: "Eve Engineer", Kind: "individual", Org: "Acme Corp", AISummary: "Backend developer"},
		{FormattedName: "Frank Finance", Kind: "individual", Note: "Financial advisor specializing in tax planning"},
		{FormattedName: "Acme Corp", Kind: "org", AISummary: "Technology company in San Francisco"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatalf("Upsert(%q) error = %v", c.FormattedName, err)
		}
	}

	tests := []struct {
		name     string
		query    string
		wantMin  int
		wantName string
	}{
		{"by name", "Eve", 1, "Eve Engineer"},
		{"by note word", "tax", 1, "Frank Finance"},
		{"by org name", "Acme", 1, "Acme Corp"},
		{"by ai_summary", "developer", 1, "Eve Engineer"},
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
					if r.FormattedName == tt.wantName {
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

	c := &Contact{FormattedName: "Grace Ghosted", Kind: "individual", AISummary: "Will be deleted soon"}
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

	c := &Contact{FormattedName: "Hank Helper", Kind: "individual", AISummary: "Friendly handyman"}
	if _, err := store.Upsert(c); err != nil {
		t.Fatal(err)
	}

	results, err := store.searchLIKE("handyman")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("searchLIKE returned %d results, want 1", len(results))
	}
	if results[0].FormattedName != "Hank Helper" {
		t.Errorf("FormattedName = %q, want %q", results[0].FormattedName, "Hank Helper")
	}
}

func TestAddProperty_GetPropertiesMap(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Ivy Info", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AddProperty(created.ID, &Property{Property: "timezone", Value: "America/Chicago"}); err != nil {
		t.Fatalf("AddProperty(timezone) error = %v", err)
	}
	if err := store.AddProperty(created.ID, &Property{Property: "ha_companion_app", Value: "mobile_app_nuggets_iphone"}); err != nil {
		t.Fatalf("AddProperty(ha_companion_app) error = %v", err)
	}

	props, err := store.GetPropertiesMap(created.ID)
	if err != nil {
		t.Fatalf("GetPropertiesMap() error = %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("GetPropertiesMap() returned %d keys, want 2", len(props))
	}
	if len(props["timezone"]) != 1 || props["timezone"][0] != "America/Chicago" {
		t.Errorf("timezone = %v, want [America/Chicago]", props["timezone"])
	}
	if len(props["ha_companion_app"]) != 1 || props["ha_companion_app"][0] != "mobile_app_nuggets_iphone" {
		t.Errorf("ha_companion_app = %v, want [mobile_app_nuggets_iphone]", props["ha_companion_app"])
	}
}

func TestAddProperty_MultiValue(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Jack MultiTag", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AddProperty(created.ID, &Property{Property: "notification_preference", Value: "urgent_only"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProperty(created.ID, &Property{Property: "notification_preference", Value: "no_marketing"}); err != nil {
		t.Fatal(err)
	}

	props, err := store.GetPropertiesMap(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(props["notification_preference"]) != 2 {
		t.Errorf("expected 2 notification_preference values, got %d: %v", len(props["notification_preference"]), props["notification_preference"])
	}
}

func TestAddProperty_DuplicateGuard(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Jack Idempotent", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AddProperty(created.ID, &Property{Property: "timezone", Value: "America/Chicago"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProperty(created.ID, &Property{Property: "timezone", Value: "America/Chicago"}); err != nil {
		t.Fatal(err)
	}

	props, err := store.GetPropertiesMap(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(props["timezone"]) != 1 {
		t.Errorf("expected 1 timezone value after duplicate AddProperty, got %d", len(props["timezone"]))
	}
}

func TestListByKind(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{FormattedName: "IndivA", Kind: "individual"},
		{FormattedName: "IndivB", Kind: "individual"},
		{FormattedName: "OrgA", Kind: "org"},
		{FormattedName: "GroupA", Kind: "group"},
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
		{"individual", 2},
		{"org", 1},
		{"group", 1},
		{"location", 0},
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
		{FormattedName: "Zara", Kind: "individual"},
		{FormattedName: "Adam", Kind: "individual"},
		{FormattedName: "Mid Corp", Kind: "org"},
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
	if results[0].FormattedName != "Adam" {
		t.Errorf("first contact = %q, want %q", results[0].FormattedName, "Adam")
	}
}

func TestResurrectSoftDeleted(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Zombie Contact", Kind: "individual", AISummary: "Will be deleted then resurrected"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatal(err)
	}

	created.AISummary = "Back from the dead"
	_, err = store.Upsert(created)
	if err != nil {
		t.Fatalf("Upsert() resurrect error = %v", err)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get() after resurrect error = %v", err)
	}
	if got.AISummary != "Back from the dead" {
		t.Errorf("AISummary = %q, want %q", got.AISummary, "Back from the dead")
	}
}

func TestSemanticSearch(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Near Match", Kind: "individual"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbedding(created1.ID, []float32{0.9, 0.1, 0.0}); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{FormattedName: "Far Match", Kind: "individual"}
	created2, err := store.Upsert(c2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbedding(created2.ID, []float32{0.0, 0.0, 1.0}); err != nil {
		t.Fatal(err)
	}

	c3 := &Contact{FormattedName: "No Embedding", Kind: "individual"}
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

	if contacts[0].FormattedName != "Near Match" {
		t.Errorf("first result = %q, want %q", contacts[0].FormattedName, "Near Match")
	}
	if scores[0] < scores[1] {
		t.Errorf("first score (%f) should be >= second score (%f)", scores[0], scores[1])
	}
}

func TestSemanticSearch_ExcludesDeleted(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Deleted Embedded", Kind: "individual"}
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

	c1 := &Contact{FormattedName: "Has Embedding", Kind: "individual"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbedding(created1.ID, []float32{1.0, 0.0}); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{FormattedName: "No Embedding", Kind: "individual"}
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
	if contacts[0].FormattedName != "No Embedding" {
		t.Errorf("FormattedName = %q, want %q", contacts[0].FormattedName, "No Embedding")
	}
}

func TestStats(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{FormattedName: "P1", Kind: "individual"},
		{FormattedName: "P2", Kind: "individual"},
		{FormattedName: "C1", Kind: "org"},
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
	if kinds["individual"] != 2 {
		t.Errorf("kinds[individual] = %d, want 2", kinds["individual"])
	}
	if kinds["org"] != 1 {
		t.Errorf("kinds[org] = %d, want 1", kinds["org"])
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

	c := &Contact{FormattedName: "Limit Zero", Kind: "individual"}
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

	c := &Contact{FormattedName: "No Zone Set", Kind: "individual"}
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

	c := &Contact{FormattedName: "Trusted Friend", Kind: "individual", TrustZone: "trusted"}
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

	c := &Contact{FormattedName: "Bad Zone", Kind: "individual", TrustZone: "superadmin"}
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

	c := &Contact{FormattedName: "The Owner", Kind: "individual", TrustZone: "owner"}
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
		{FormattedName: "Owner A", Kind: "individual", TrustZone: "owner"},
		{FormattedName: "Trusted B", Kind: "individual", TrustZone: "trusted"},
		{FormattedName: "Trusted C", Kind: "individual", TrustZone: "trusted"},
		{FormattedName: "Known D", Kind: "individual", TrustZone: "known"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatalf("Upsert(%q) error = %v", c.FormattedName, err)
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

func TestFindByPropertyExact_HACompanionApp(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Dan Egan", Kind: "individual"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.AddProperty(created1.ID, &Property{Property: "ha_companion_app", Value: "mobile_app_dan"})

	c2 := &Contact{FormattedName: "Daniel Craig", Kind: "individual"}
	created2, err := store.Upsert(c2)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.AddProperty(created2.ID, &Property{Property: "ha_companion_app", Value: "mobile_app_daniel"})

	// Exact match should find only Dan Egan.
	results, err := store.FindByPropertyExact("ha_companion_app", "mobile_app_dan")
	if err != nil {
		t.Fatalf("FindByPropertyExact() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("FindByPropertyExact() returned %d results, want 1", len(results))
	}
	if results[0].FormattedName != "Dan Egan" {
		t.Errorf("FormattedName = %q, want %q", results[0].FormattedName, "Dan Egan")
	}

	// Case-insensitive match.
	results, err = store.FindByPropertyExact("ha_companion_app", "MOBILE_APP_DAN")
	if err != nil {
		t.Fatalf("FindByPropertyExact() case-insensitive error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("FindByPropertyExact() case-insensitive returned %d results, want 1", len(results))
	}

	// No match for partial value.
	results, err = store.FindByPropertyExact("ha_companion_app", "mobile_app_d")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("FindByPropertyExact() partial match returned %d results, want 0", len(results))
	}
}

func TestResolveContact_ExactName(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Alice Johnson", Kind: "individual"}
	if _, err := store.Upsert(c); err != nil {
		t.Fatal(err)
	}

	got, err := store.ResolveContact("alice johnson")
	if err != nil {
		t.Fatalf("ResolveContact() error = %v", err)
	}
	if got.FormattedName != "Alice Johnson" {
		t.Errorf("FormattedName = %q, want %q", got.FormattedName, "Alice Johnson")
	}
}

func TestResolveContact_Nickname(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "David McNett", Kind: "individual", Nickname: "Nugget"}
	if _, err := store.Upsert(c); err != nil {
		t.Fatal(err)
	}

	// Should resolve via Nickname column.
	got, err := store.ResolveContact("nugget")
	if err != nil {
		t.Fatalf("ResolveContact() error = %v", err)
	}
	if got.FormattedName != "David McNett" {
		t.Errorf("FormattedName = %q, want %q", got.FormattedName, "David McNett")
	}
}

func TestResolveContact_SearchFallback(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Eve Engineer", Kind: "individual", AISummary: "Backend developer"}
	if _, err := store.Upsert(c); err != nil {
		t.Fatal(err)
	}

	got, err := store.ResolveContact("Eve")
	if err != nil {
		t.Fatalf("ResolveContact() error = %v", err)
	}
	if got.FormattedName != "Eve Engineer" {
		t.Errorf("FormattedName = %q, want %q", got.FormattedName, "Eve Engineer")
	}
}

func TestResolveContact_Ambiguous(t *testing.T) {
	store := newTestStore(t)

	contacts := []*Contact{
		{FormattedName: "Eve Alpha", Kind: "individual", AISummary: "Eve works on alpha"},
		{FormattedName: "Eve Beta", Kind: "individual", AISummary: "Eve works on beta"},
	}
	for _, c := range contacts {
		if _, err := store.Upsert(c); err != nil {
			t.Fatal(err)
		}
	}

	_, err := store.ResolveContact("Eve")
	if err == nil {
		t.Fatal("expected error for ambiguous contact")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want to contain 'ambiguous'", err.Error())
	}
}

func TestResolveContact_NotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.ResolveContact("Nobody Here")
	if err != sql.ErrNoRows {
		t.Errorf("ResolveContact() non-existent: got err = %v, want sql.ErrNoRows", err)
	}
}

func TestResolveContact_PriorityOrder(t *testing.T) {
	store := newTestStore(t)

	// Create a contact named "Nugget" and a different contact with
	// Nickname = "Nugget". The exact name match should win.
	c1 := &Contact{FormattedName: "Nugget", Kind: "individual"}
	if _, err := store.Upsert(c1); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{FormattedName: "David McNett", Kind: "individual", Nickname: "Nugget"}
	if _, err := store.Upsert(c2); err != nil {
		t.Fatal(err)
	}

	got, err := store.ResolveContact("Nugget")
	if err != nil {
		t.Fatalf("ResolveContact() error = %v", err)
	}
	if got.FormattedName != "Nugget" {
		t.Errorf("FormattedName = %q, want %q (exact match should win)", got.FormattedName, "Nugget")
	}
}

func TestUpsert_DuplicateActiveName(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Unique Person", Kind: "individual"}
	if _, err := store.Upsert(c1); err != nil {
		t.Fatal(err)
	}

	c2 := &Contact{FormattedName: "unique person", Kind: "individual"}
	_, err := store.Upsert(c2)
	if err == nil {
		t.Error("expected error inserting duplicate active name, got nil")
	}
}

// --- Property CRUD tests ---

func TestAddProperty_GetProperties(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Prop Tester", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AddProperty(created.ID, &Property{
		Property: "EMAIL",
		Value:    "test@example.com",
		Type:     "work",
		Pref:     1,
	}); err != nil {
		t.Fatalf("AddProperty(EMAIL) error = %v", err)
	}
	if err := store.AddProperty(created.ID, &Property{
		Property: "TEL",
		Value:    "+15551234567",
		Type:     "cell",
	}); err != nil {
		t.Fatalf("AddProperty(TEL) error = %v", err)
	}

	props, err := store.GetProperties(created.ID)
	if err != nil {
		t.Fatalf("GetProperties() error = %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("GetProperties() returned %d, want 2", len(props))
	}

	// Properties are ordered by property name, then pref.
	email := props[0]
	if email.Property != "EMAIL" || email.Value != "test@example.com" || email.Type != "work" || email.Pref != 1 {
		t.Errorf("EMAIL prop = %+v", email)
	}
	tel := props[1]
	if tel.Property != "TEL" || tel.Value != "+15551234567" || tel.Type != "cell" {
		t.Errorf("TEL prop = %+v", tel)
	}
}

func TestFindByPropertyExact(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Email Alice", Kind: "individual"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.AddProperty(created1.ID, &Property{Property: "EMAIL", Value: "alice@example.com"})

	c2 := &Contact{FormattedName: "Email Bob", Kind: "individual"}
	created2, err := store.Upsert(c2)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.AddProperty(created2.ID, &Property{Property: "EMAIL", Value: "bob@example.com"})

	// Exact match.
	results, err := store.FindByPropertyExact("EMAIL", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].FormattedName != "Email Alice" {
		t.Errorf("FormattedName = %q, want %q", results[0].FormattedName, "Email Alice")
	}

	// Case-insensitive.
	results, err = store.FindByPropertyExact("EMAIL", "ALICE@EXAMPLE.COM")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("case-insensitive: got %d results, want 1", len(results))
	}
}

func TestFindByProperty_LIKE(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Tel Alice", Kind: "individual"}
	created1, err := store.Upsert(c1)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.AddProperty(created1.ID, &Property{Property: "TEL", Value: "+15551111111"})

	c2 := &Contact{FormattedName: "Tel Bob", Kind: "individual"}
	created2, err := store.Upsert(c2)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.AddProperty(created2.ID, &Property{Property: "TEL", Value: "+15552222222"})

	// Partial match should find both.
	results, err := store.FindByProperty("TEL", "+1555")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestDeleteProperty(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Del Prop", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	p := &Property{Property: "EMAIL", Value: "del@example.com"}
	_ = store.AddProperty(created.ID, p)

	if err := store.DeleteProperty(p.ID); err != nil {
		t.Fatalf("DeleteProperty() error = %v", err)
	}

	props, _ := store.GetProperties(created.ID)
	if len(props) != 0 {
		t.Errorf("expected 0 properties after delete, got %d", len(props))
	}
}

func TestDeleteContactProperties(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Multi Email", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	_ = store.AddProperty(created.ID, &Property{Property: "EMAIL", Value: "a@example.com"})
	_ = store.AddProperty(created.ID, &Property{Property: "EMAIL", Value: "b@example.com"})
	_ = store.AddProperty(created.ID, &Property{Property: "TEL", Value: "+15551234567"})

	if err := store.DeleteContactProperties(created.ID, "EMAIL"); err != nil {
		t.Fatal(err)
	}

	props, _ := store.GetProperties(created.ID)
	if len(props) != 1 {
		t.Fatalf("expected 1 property after deleting EMAILs, got %d", len(props))
	}
	if props[0].Property != "TEL" {
		t.Errorf("remaining property = %q, want TEL", props[0].Property)
	}
}

func TestGetWithProperties(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Full Contact", Kind: "individual"}
	created, err := store.Upsert(c)
	if err != nil {
		t.Fatal(err)
	}

	_ = store.AddProperty(created.ID, &Property{Property: "EMAIL", Value: "full@example.com"})
	_ = store.AddProperty(created.ID, &Property{Property: "timezone", Value: "America/Chicago"})

	got, err := store.GetWithProperties(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Properties) != 2 {
		t.Errorf("Properties count = %d, want 2", len(got.Properties))
	}
}

func TestFindByNickname(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "David McNett", Kind: "individual", Nickname: "Nugget"}
	if _, err := store.Upsert(c); err != nil {
		t.Fatal(err)
	}

	got, err := store.FindByNickname("nugget")
	if err != nil {
		t.Fatalf("FindByNickname() error = %v", err)
	}
	if got.FormattedName != "David McNett" {
		t.Errorf("FormattedName = %q, want %q", got.FormattedName, "David McNett")
	}

	_, err = store.FindByNickname("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("FindByNickname(nonexistent) error = %v, want sql.ErrNoRows", err)
	}
}

func TestUpsert_VCardFields(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{
		FormattedName: "Dr. Jane Smith Jr.",
		Kind:          "individual",
		FamilyName:    "Smith",
		GivenName:     "Jane",
		NamePrefix:    "Dr.",
		NameSuffix:    "Jr.",
		Nickname:      "Janey",
		Org:           "Acme Corp",
		Title:         "VP Engineering",
		Role:          "Technical Leadership",
		Note:          "Met at conference",
		AISummary:     "Leads eng team at Acme",
	}

	created, err := store.Upsert(c)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.FamilyName != "Smith" {
		t.Errorf("FamilyName = %q, want %q", got.FamilyName, "Smith")
	}
	if got.GivenName != "Jane" {
		t.Errorf("GivenName = %q, want %q", got.GivenName, "Jane")
	}
	if got.NamePrefix != "Dr." {
		t.Errorf("NamePrefix = %q, want %q", got.NamePrefix, "Dr.")
	}
	if got.Nickname != "Janey" {
		t.Errorf("Nickname = %q, want %q", got.Nickname, "Janey")
	}
	if got.Org != "Acme Corp" {
		t.Errorf("Org = %q, want %q", got.Org, "Acme Corp")
	}
	if got.Title != "VP Engineering" {
		t.Errorf("Title = %q, want %q", got.Title, "VP Engineering")
	}
	if got.Role != "Technical Leadership" {
		t.Errorf("Role = %q, want %q", got.Role, "Technical Leadership")
	}
	if got.Note != "Met at conference" {
		t.Errorf("Note = %q, want %q", got.Note, "Met at conference")
	}
	if got.Rev == "" {
		t.Error("Rev should be set automatically")
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	store := newTestStore(t)

	var enabled int
	err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled)
	if err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Errorf("foreign_keys pragma = %d, want 1", enabled)
	}

	// Inserting a property for a non-existent contact should fail.
	bogusID := uuid.New()
	err = store.AddProperty(bogusID, &Property{Property: "EMAIL", Value: "nobody@example.com"})
	if err == nil {
		t.Error("expected foreign key violation for bogus contact_id")
	}
}
