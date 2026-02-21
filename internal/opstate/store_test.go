package opstate

import (
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "opstate_test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
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

func TestNewStore_InvalidPath(t *testing.T) {
	_, err := NewStore("/nonexistent/path/db.sqlite")
	if err == nil {
		t.Error("NewStore() should fail for invalid path")
	}
}

func TestStore_PersistAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist_test.db")

	// Open, write, close.
	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(1): %v", err)
	}
	if err := s1.Set("ns", "key", "persistent"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	s1.Close()

	// Reopen and verify.
	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	defer s2.Close()

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

func TestNewStore_InvalidPath_NoDir(t *testing.T) {
	// Use a path where the parent directory doesn't exist.
	dbPath := filepath.Join(t.TempDir(), "subdir", "nested", "db.sqlite")
	// Remove the temp dir's content to ensure subdir doesn't exist.
	_ = os.RemoveAll(filepath.Dir(filepath.Dir(dbPath)))

	_, err := NewStore(dbPath)
	if err == nil {
		t.Error("NewStore() should fail when parent directory doesn't exist")
	}
}
