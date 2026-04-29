package provenance

import (
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestStoreDeleteCreatesSignedDeletionCommit(t *testing.T) {
	s := testStore(t)

	if err := s.Write(t.Context(), "state.md", "present", "write-state"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Delete(t.Context(), "state.md", "delete-state"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.path, "state.md")); !os.IsNotExist(err) {
		t.Fatalf("state.md stat error = %v, want not exist", err)
	}
	hist, err := s.History(t.Context(), "state.md")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if hist.RevisionCount != 2 || hist.LastMessage != "delete-state" {
		t.Fatalf("History = %+v, want write and delete commits", hist)
	}
}

func TestStoreDeleteRejectsUntrackedFileWithoutRemovingIt(t *testing.T) {
	s := testStore(t)

	filename := "manual.md"
	path := filepath.Join(s.path, filename)
	if err := os.WriteFile(path, []byte("outside provenance"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err := s.Delete(t.Context(), filename, "delete-manual")
	if err == nil {
		t.Fatal("Delete returned nil, want untracked-file error")
	}
	if !strings.Contains(err.Error(), "untracked file") {
		t.Fatalf("Delete error = %v, want untracked-file message", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("manual.md stat after failed delete = %v, want file preserved", statErr)
	}
}

func TestStoreNewWithOptionsUsesExternalAllowedSigners(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	allowedPath := filepath.Join(dir, "allowed_signers")
	signer := testSigner(t)
	if err := os.WriteFile(allowedPath, []byte("thane@provenance.local "+signer.PublicKey()+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile allowed signers: %v", err)
	}
	storePath := filepath.Join(dir, "repo")
	s, err := NewWithOptions(storePath, signer, slog.Default(), Options{
		AllowedSignersPath: allowedPath,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storePath, ".allowed_signers")); !os.IsNotExist(err) {
		t.Fatalf(".allowed_signers stat error = %v, want repository-local file absent", err)
	}
	if err := s.Write(t.Context(), "test.md", "signed content", "test-external-signers"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.git(t.Context(), nil, nil, "verify-commit", "HEAD"); err != nil {
		t.Fatalf("verify-commit with external allowed signers: %v", err)
	}
}

func TestStoreNewPreservesExistingRepoLocalAllowedSigners(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	storePath := t.TempDir()
	existing := "trusted@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForPreservationOnly\n"
	if err := os.WriteFile(filepath.Join(storePath, ".allowed_signers"), []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile .allowed_signers: %v", err)
	}

	if _, err := New(storePath, testSigner(t), slog.Default()); err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(storePath, ".allowed_signers"))
	if err != nil {
		t.Fatalf("ReadFile .allowed_signers: %v", err)
	}
	if string(got) != existing {
		t.Fatalf(".allowed_signers was overwritten:\n got %q\nwant %q", got, existing)
	}
}

func TestStoreNewRejectsNonRegularRepoLocalAllowedSigners(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	storePath := t.TempDir()
	if err := os.Mkdir(filepath.Join(storePath, ".allowed_signers"), 0o755); err != nil {
		t.Fatalf("Mkdir .allowed_signers: %v", err)
	}

	_, err := New(storePath, testSigner(t), slog.Default())
	if err == nil {
		t.Fatal("New returned nil, want non-regular .allowed_signers error")
	}
	if !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("error = %v, want regular-file message", err)
	}
}

func TestVerifierMissingRepoLocalAllowedSignersIncludesPath(t *testing.T) {
	repoPath := t.TempDir()
	_, err := NewVerifier(repoPath, nil, Options{})
	if err == nil {
		t.Fatal("NewVerifier returned nil, want missing .allowed_signers error")
	}
	want := filepath.Join(repoPath, ".allowed_signers")
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), repoPath) {
		t.Fatalf("error = %v, want repo and allowed signers paths", err)
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

func TestStoreWriteFilesCreatesSingleCommit(t *testing.T) {
	s := testStore(t)

	if err := s.WriteFiles(t.Context(), map[string]string{
		"alpha.txt":      "alpha",
		"nested/beta.md": "beta",
	}, "bootstrap"); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}

	for name, want := range map[string]string{
		"alpha.txt":      "alpha",
		"nested/beta.md": "beta",
	} {
		got, err := s.Read(name)
		if err != nil {
			t.Fatalf("Read %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("Read %s = %q, want %q", name, got, want)
		}
		hist, err := s.History(t.Context(), name)
		if err != nil {
			t.Fatalf("History %s: %v", name, err)
		}
		if hist.RevisionCount != 1 || hist.LastMessage != "bootstrap" {
			t.Fatalf("History %s = %+v, want one bootstrap commit", name, hist)
		}
	}
}

func TestStoreWriteFilesRejectsEmptySet(t *testing.T) {
	s := testStore(t)

	if err := s.WriteFiles(t.Context(), map[string]string{}, "empty"); err == nil {
		t.Fatal("WriteFiles empty set returned nil, want error")
	}
}

func TestStoreWriteRejectsEmptyOrDotFilename(t *testing.T) {
	s := testStore(t)

	for _, tc := range []struct {
		name     string
		filename string
	}{
		{name: "empty", filename: ""},
		{name: "dot", filename: "."},
		{name: "dot-slash", filename: "./"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Write(t.Context(), tc.filename, "content", "bad"); err == nil {
				t.Fatal("Write returned nil, want error")
			}
			if err := s.WriteFiles(t.Context(), map[string]string{tc.filename: "content"}, "bad"); err == nil {
				t.Fatal("WriteFiles returned nil, want error")
			}
		})
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

func TestStoreWriteFilesNoChangeSkipsCommit(t *testing.T) {
	s := testStore(t)

	files := map[string]string{
		"alpha.txt":      "alpha",
		"nested/beta.md": "beta",
	}
	if err := s.WriteFiles(t.Context(), files, "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFiles(t.Context(), files, "second"); err != nil {
		t.Fatal(err)
	}

	for filename := range files {
		hist, err := s.History(t.Context(), filename)
		if err != nil {
			t.Fatalf("History %s: %v", filename, err)
		}
		if hist.RevisionCount != 1 || hist.LastMessage != "first" {
			t.Fatalf("History %s = %+v, want one first commit", filename, hist)
		}
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
