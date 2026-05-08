package opstate

import (
	"context"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestGetMissing(t *testing.T) {
	s := testStore(t)

	val, err := s.Get("ns", "missing")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "" {
		t.Errorf("Get() = %q, want empty string for missing key", val)
	}
}

func TestSetAndGet(t *testing.T) {
	s := testStore(t)

	if err := s.Set("email_poll", "personal:INBOX", "4217"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	val, err := s.Get("email_poll", "personal:INBOX")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "4217" {
		t.Errorf("Get() = %q, want %q", val, "4217")
	}
}

func TestSetUpsert(t *testing.T) {
	s := testStore(t)

	if err := s.Set("ns", "key", "v1"); err != nil {
		t.Fatalf("Set(v1) error: %v", err)
	}
	if err := s.Set("ns", "key", "v2"); err != nil {
		t.Fatalf("Set(v2) error: %v", err)
	}

	val, err := s.Get("ns", "key")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "v2" {
		t.Errorf("Get() = %q, want %q after upsert", val, "v2")
	}
}

func TestDelete(t *testing.T) {
	s := testStore(t)

	if err := s.Set("ns", "key", "val"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if err := s.Delete("ns", "key"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	val, err := s.Get("ns", "key")
	if err != nil {
		t.Fatalf("Get() after delete error: %v", err)
	}
	if val != "" {
		t.Errorf("Get() = %q after delete, want empty", val)
	}
}

func TestDeleteMissing(t *testing.T) {
	s := testStore(t)

	// Deleting a non-existent key should not error.
	if err := s.Delete("ns", "nope"); err != nil {
		t.Errorf("Delete(missing) error: %v", err)
	}
}

func TestNamespaceIsolation(t *testing.T) {
	s := testStore(t)

	if err := s.Set("alpha", "key", "a-val"); err != nil {
		t.Fatalf("Set(alpha) error: %v", err)
	}
	if err := s.Set("beta", "key", "b-val"); err != nil {
		t.Fatalf("Set(beta) error: %v", err)
	}

	aVal, err := s.Get("alpha", "key")
	if err != nil {
		t.Fatalf("Get(alpha) error: %v", err)
	}
	bVal, err := s.Get("beta", "key")
	if err != nil {
		t.Fatalf("Get(beta) error: %v", err)
	}

	if aVal != "a-val" {
		t.Errorf("alpha/key = %q, want %q", aVal, "a-val")
	}
	if bVal != "b-val" {
		t.Errorf("beta/key = %q, want %q", bVal, "b-val")
	}
}

func TestList(t *testing.T) {
	s := testStore(t)

	if err := s.Set("ns", "a", "1"); err != nil {
		t.Fatalf("Set(a) error: %v", err)
	}
	if err := s.Set("ns", "b", "2"); err != nil {
		t.Fatalf("Set(b) error: %v", err)
	}
	// Different namespace — should not appear.
	if err := s.Set("other", "c", "3"); err != nil {
		t.Fatalf("Set(other) error: %v", err)
	}

	result, err := s.List("ns")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(result))
	}
	if result["a"] != "1" || result["b"] != "2" {
		t.Errorf("List() = %v, want {a:1, b:2}", result)
	}
}

func TestListEmpty(t *testing.T) {
	s := testStore(t)

	result, err := s.List("empty")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if result == nil {
		t.Error("List() returned nil, want empty map")
	}
	if len(result) != 0 {
		t.Errorf("List() returned %d entries, want 0", len(result))
	}
}

func TestNewStore_NilDB(t *testing.T) {
	// A nil *sql.DB should fail during migration.
	_, err := NewStore(nil, nil)
	if err == nil {
		t.Error("NewStore(nil) should fail")
	}
}

func TestStore_SharedConnection(t *testing.T) {
	// Two Store instances on the same *sql.DB see each other's writes.
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	s1, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore(1): %v", err)
	}
	if err := s1.Set("ns", "key", "persistent"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	// Create a second Store on the same DB — data should be visible.
	s2, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}

	val, err := s2.Get("ns", "key")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "persistent" {
		t.Errorf("Get() = %q after reopen, want %q", val, "persistent")
	}
}

func TestDeleteNamespace(t *testing.T) {
	s := testStore(t)

	// Populate two namespaces.
	if err := s.Set("target", "a", "1"); err != nil {
		t.Fatalf("Set(target/a): %v", err)
	}
	if err := s.Set("target", "b", "2"); err != nil {
		t.Fatalf("Set(target/b): %v", err)
	}
	if err := s.Set("other", "c", "3"); err != nil {
		t.Fatalf("Set(other/c): %v", err)
	}

	if err := s.DeleteNamespace("target"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}

	// Target namespace should be empty.
	targetEntries, err := s.List("target")
	if err != nil {
		t.Fatalf("List(target): %v", err)
	}
	if len(targetEntries) != 0 {
		t.Errorf("target namespace has %d entries after delete, want 0", len(targetEntries))
	}

	// Other namespace should be untouched.
	otherVal, err := s.Get("other", "c")
	if err != nil {
		t.Fatalf("Get(other/c): %v", err)
	}
	if otherVal != "3" {
		t.Errorf("other/c = %q, want %q (should be untouched)", otherVal, "3")
	}
}

func TestDeleteNamespace_Empty(t *testing.T) {
	s := testStore(t)

	// Deleting a non-existent namespace should not error.
	if err := s.DeleteNamespace("nonexistent"); err != nil {
		t.Errorf("DeleteNamespace(empty): %v", err)
	}
}

// --- TTL tests ---

// insertExpired inserts a row with an expires_at timestamp in the past
// via raw SQL, avoiding any wall-clock delay in tests.
func insertExpired(t *testing.T, s *Store, namespace, key, value string, ago time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO operational_state (namespace, key, value, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		namespace, key, value,
		now.Format(time.RFC3339),
		now.Add(-ago).Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insertExpired(%s/%s): %v", namespace, key, err)
	}
}

func TestSetWithTTL_BeforeExpiry(t *testing.T) {
	s := testStore(t)

	if err := s.SetWithTTL("ns", "key", "val", 1*time.Hour); err != nil {
		t.Fatalf("SetWithTTL: %v", err)
	}

	got, err := s.Get("ns", "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "val" {
		t.Errorf("Get() = %q, want %q (should be visible before TTL)", got, "val")
	}
}

func TestSetWithTTL_AfterExpiry(t *testing.T) {
	s := testStore(t)

	// Insert a row that expired 1 minute ago.
	insertExpired(t, s, "ns", "expired-key", "old-val", 1*time.Minute)

	got, err := s.Get("ns", "expired-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "" {
		t.Errorf("Get() = %q, want empty (expired key should be invisible)", got)
	}
}

func TestSet_NeverExpires(t *testing.T) {
	s := testStore(t)

	// Plain Set should clear any previous TTL.
	if err := s.SetWithTTL("ns", "key", "ttl-val", 1*time.Hour); err != nil {
		t.Fatalf("SetWithTTL: %v", err)
	}

	// Overwrite with plain Set (no TTL).
	if err := s.Set("ns", "key", "permanent"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Verify expires_at is NULL.
	var expiresAt *string
	err := s.db.QueryRow(
		`SELECT expires_at FROM operational_state WHERE namespace = ? AND key = ?`,
		"ns", "key",
	).Scan(&expiresAt)
	if err != nil {
		t.Fatalf("scan expires_at: %v", err)
	}
	if expiresAt != nil {
		t.Errorf("expires_at = %q, want NULL after plain Set()", *expiresAt)
	}

	got, err := s.Get("ns", "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "permanent" {
		t.Errorf("Get() = %q, want %q", got, "permanent")
	}
}

func TestDeleteExpired(t *testing.T) {
	s := testStore(t)

	// Insert a mix: expired, live TTL, and permanent.
	insertExpired(t, s, "ns", "old1", "v1", 2*time.Hour)
	insertExpired(t, s, "ns", "old2", "v2", 30*time.Minute)
	if err := s.SetWithTTL("ns", "fresh", "v3", 1*time.Hour); err != nil {
		t.Fatalf("SetWithTTL(fresh): %v", err)
	}
	if err := s.Set("ns", "permanent", "v4"); err != nil {
		t.Fatalf("Set(permanent): %v", err)
	}

	n, err := s.DeleteExpired(context.Background())
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 2 {
		t.Errorf("DeleteExpired() = %d, want 2", n)
	}

	// Verify only fresh and permanent survive.
	result, err := s.List("ns")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("List() returned %d entries, want 2 (fresh + permanent)", len(result))
	}
	if result["fresh"] != "v3" {
		t.Errorf("fresh = %q, want %q", result["fresh"], "v3")
	}
	if result["permanent"] != "v4" {
		t.Errorf("permanent = %q, want %q", result["permanent"], "v4")
	}
}

func TestList_FiltersExpired(t *testing.T) {
	s := testStore(t)

	if err := s.Set("ns", "alive", "yes"); err != nil {
		t.Fatalf("Set(alive): %v", err)
	}
	insertExpired(t, s, "ns", "dead", "no", 5*time.Minute)

	result, err := s.List("ns")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("List() returned %d entries, want 1", len(result))
	}
	if result["alive"] != "yes" {
		t.Errorf("alive = %q, want %q", result["alive"], "yes")
	}
	if _, ok := result["dead"]; ok {
		t.Error("List() should not include expired key")
	}
}
