package provenance

import (
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testSigner creates an in-memory ed25519 signer for tests.
func testSigner(t *testing.T) *SSHFileSigner {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSSHSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// testStore creates a Store in a temp directory with a test signer.
func testStore(t *testing.T) *Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	s, err := New(dir, testSigner(t), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreInitCreatesRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	storePath := filepath.Join(dir, "repo")

	_, err := New(storePath, testSigner(t), slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Verify .git directory exists.
	if _, err := os.Stat(filepath.Join(storePath, ".git")); err != nil {
		t.Errorf("expected .git directory: %v", err)
	}

	// Verify .allowed_signers exists.
	if _, err := os.Stat(filepath.Join(storePath, ".allowed_signers")); err != nil {
		t.Errorf("expected .allowed_signers: %v", err)
	}
}

func TestStoreWriteRead(t *testing.T) {
	s := testStore(t)

	content := "# Ego\n\nSelf-reflection content here.\n"
	if err := s.Write(t.Context(), "ego.md", content, "test-write"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := s.Read("ego.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got != content {
		t.Errorf("Read = %q, want %q", got, content)
	}
}

func TestStoreWriteCreatesSubdirectories(t *testing.T) {
	s := testStore(t)

	if err := s.Write(t.Context(), "sub/dir/file.md", "nested", "test-nested"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := s.Read("sub/dir/file.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "nested" {
		t.Errorf("Read = %q, want %q", got, "nested")
	}
}

func TestStoreWriteNoChangeSkipsCommit(t *testing.T) {
	s := testStore(t)

	if err := s.Write(t.Context(), "test.md", "content", "first"); err != nil {
		t.Fatal(err)
	}

	// Write identical content — should not create a new commit.
	if err := s.Write(t.Context(), "test.md", "content", "second"); err != nil {
		t.Fatal(err)
	}

	hist, err := s.History(t.Context(), "test.md")
	if err != nil {
		t.Fatal(err)
	}

	if hist.RevisionCount != 1 {
		t.Errorf("RevisionCount = %d, want 1 (no-change write should not create commit)", hist.RevisionCount)
	}
}

func TestStoreHistory(t *testing.T) {
	s := testStore(t)

	// Create a few revisions.
	for i, msg := range []string{"first-write", "second-write", "third-write"} {
		content := "revision " + msg
		if err := s.Write(t.Context(), "state.md", content, msg); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		// Small delay so timestamps differ.
		time.Sleep(10 * time.Millisecond)
	}

	hist, err := s.History(t.Context(), "state.md")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if hist.RevisionCount != 3 {
		t.Errorf("RevisionCount = %d, want 3", hist.RevisionCount)
	}

	if hist.LastMessage != "third-write" {
		t.Errorf("LastMessage = %q, want %q", hist.LastMessage, "third-write")
	}

	if hist.LastModified.IsZero() {
		t.Error("LastModified is zero")
	}

	if len(hist.RecentEdits) != 3 {
		t.Errorf("len(RecentEdits) = %d, want 3", len(hist.RecentEdits))
	}

	// Newest first.
	if len(hist.RecentEdits) >= 2 {
		if hist.RecentEdits[0].Message != "third-write" {
			t.Errorf("RecentEdits[0].Message = %q, want %q", hist.RecentEdits[0].Message, "third-write")
		}
		if hist.RecentEdits[1].Message != "second-write" {
			t.Errorf("RecentEdits[1].Message = %q, want %q", hist.RecentEdits[1].Message, "second-write")
		}
	}
}

func TestStoreHistoryEmptyRepo(t *testing.T) {
	s := testStore(t)

	hist, err := s.History(t.Context(), "nonexistent.md")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if hist.RevisionCount != 0 {
		t.Errorf("RevisionCount = %d, want 0", hist.RevisionCount)
	}
}

func TestStoreFilePath(t *testing.T) {
	s := testStore(t)

	got := s.FilePath("ego.md")
	want := filepath.Join(s.Path(), "ego.md")
	if got != want {
		t.Errorf("FilePath = %q, want %q", got, want)
	}
}
