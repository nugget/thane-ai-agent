package provenance

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSignedCommitVerifies creates a signed commit and verifies it with
// git verify-commit. This is the strongest correctness guarantee — if
// git accepts the signature, the full pipeline (sshsig format, commit
// object construction, gpgsig header injection) is correct.
func TestSignedCommitVerifies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	s := testStore(t)

	if err := s.Write(t.Context(), "test.md", "signed content", "test-signed"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// git verify-commit HEAD should succeed.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "-C", s.path, "verify-commit", "HEAD")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("git verify-commit failed: %v\nstdout: %s\nstderr: %s",
			err, stdout.String(), stderr.String())
	}

	// The output should mention "Good" signature.
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "Good") {
		t.Errorf("expected 'Good' in verify-commit output, got: %s", combined)
	}
}

// TestMultipleCommitsVerify verifies that a sequence of commits all
// have valid signatures.
func TestMultipleCommitsVerify(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	s := testStore(t)

	for _, content := range []string{"first", "second", "third"} {
		if err := s.Write(t.Context(), "doc.md", content, "write-"+content); err != nil {
			t.Fatalf("Write %q: %v", content, err)
		}
	}

	// Verify all commits.
	var logBuf bytes.Buffer
	if err := s.git(t.Context(), nil, &logBuf, "log", "--format=%H"); err != nil {
		t.Fatalf("git log: %v", err)
	}

	for hash := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		if hash == "" {
			continue
		}
		cmd := exec.Command("git", "-C", s.path, "verify-commit", hash)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("verify-commit %s failed: %v\noutput: %s", hash[:8], err, out)
		}
	}
}

// TestCommitHasCorrectMessage verifies the commit message matches what
// was passed to Write.
func TestCommitHasCorrectMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	s := testStore(t)

	if err := s.Write(t.Context(), "file.md", "data", "loop-metacognitive-42"); err != nil {
		t.Fatal(err)
	}

	var msgBuf bytes.Buffer
	if err := s.git(t.Context(), nil, &msgBuf, "log", "-1", "--format=%s"); err != nil {
		t.Fatal(err)
	}

	got := strings.TrimSpace(msgBuf.String())
	if got != "loop-metacognitive-42" {
		t.Errorf("commit message = %q, want %q", got, "loop-metacognitive-42")
	}
}

func TestVerifierAcceptsSignedCleanFile(t *testing.T) {
	s := testStore(t)
	if err := s.Write(t.Context(), "test.md", "signed content", "test-signed"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	verifier, err := NewVerifier(s.path, nil, Options{})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	result, err := verifier.VerifyFile(t.Context(), "test.md")
	if err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if !result.Trusted() || result.Commit == "" {
		t.Fatalf("VerifyFile result = %+v, want trusted commit", result)
	}
}

func TestVerifierAcceptsRootWithRepoLocalAllowedSigners(t *testing.T) {
	s := testStore(t)
	if err := s.Write(t.Context(), "test.md", "signed content", "test-signed"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	verifier, err := NewVerifier(s.path, nil, Options{})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	result, err := verifier.VerifyTree(t.Context(), "")
	if err != nil {
		t.Fatalf("VerifyTree: %v", err)
	}
	if !result.Trusted() || result.Commit == "" {
		t.Fatalf("VerifyTree result = %+v, want trusted commit", result)
	}
}

func TestVerifierRejectsDirtyWorktreeFile(t *testing.T) {
	s := testStore(t)
	if err := s.Write(t.Context(), "test.md", "signed content", "test-signed"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.WriteFile(s.FilePath("test.md"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	verifier, err := NewVerifier(s.path, nil, Options{})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	result, err := verifier.VerifyFile(t.Context(), "test.md")
	if err == nil {
		t.Fatal("VerifyFile returned nil, want dirty worktree error")
	}
	if result.Trusted() || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("VerifyFile result/error = %+v / %v, want dirty failure", result, err)
	}
}

func TestVerifierRejectsUntrustedSigner(t *testing.T) {
	s := testStore(t)
	if err := s.Write(t.Context(), "test.md", "signed content", "test-signed"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wrongSigner := testSigner(t)
	allowedPath := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(allowedPath, []byte("thane@provenance.local "+wrongSigner.PublicKey()+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile allowed signers: %v", err)
	}
	verifier, err := NewVerifier(s.path, nil, Options{AllowedSignersPath: allowedPath})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	result, err := verifier.VerifyFile(t.Context(), "test.md")
	if err == nil {
		t.Fatal("VerifyFile returned nil, want untrusted signer error")
	}
	if result.Trusted() {
		t.Fatalf("VerifyFile result = %+v, want untrusted", result)
	}
}

// TestBootstrapBirthCommitCreatesHEAD covers the case where a managed
// doc root is first wired up: ensureRepo has run (git init,
// .allowed_signers present), but no commit exists yet. After
// BootstrapBirthCommit, HEAD should exist and verify cleanly.
func TestBootstrapBirthCommitCreatesHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	s, err := New(dir, testSigner(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Sanity: ensureRepo wrote .allowed_signers and ran git init,
	// but no HEAD yet.
	if err := s.git(t.Context(), nil, nil, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		t.Fatal("expected fresh repo to have no HEAD yet")
	}

	if err := s.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	// HEAD should now exist and verify cleanly.
	verifier, err := NewVerifier(s.path, nil, Options{})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	result, err := verifier.VerifyTree(t.Context(), "")
	if err != nil {
		t.Fatalf("VerifyTree on bootstrapped repo: %v", err)
	}
	if !result.Trusted() {
		t.Fatalf("VerifyTree result = %+v, want trusted", result)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf("expected .gitignore from birth commit: %v", err)
	}
}

// TestBootstrapBirthCommitIsIdempotent verifies the call is a no-op
// once HEAD exists, so callers can invoke it on every startup.
func TestBootstrapBirthCommitIsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	s := testStore(t)
	if err := s.Write(t.Context(), "test.md", "first", "first commit"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var beforeBuf bytes.Buffer
	if err := s.git(t.Context(), nil, &beforeBuf, "rev-parse", "HEAD"); err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	beforeHEAD := strings.TrimSpace(beforeBuf.String())

	if err := s.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	var afterBuf bytes.Buffer
	if err := s.git(t.Context(), nil, &afterBuf, "rev-parse", "HEAD"); err != nil {
		t.Fatalf("rev-parse HEAD after: %v", err)
	}
	if got := strings.TrimSpace(afterBuf.String()); got != beforeHEAD {
		t.Fatalf("HEAD changed after bootstrap idempotent call: before=%s after=%s", beforeHEAD, got)
	}
}

// TestBootstrapBirthCommitLeavesExistingContentUntracked guards the
// "no auto-import" rule from issue #789. If the operator drops a
// directory full of pre-existing files in front of us, bootstrap
// commits only the bootstrap files; the rest stay untracked so the
// operator must explicitly bring them under signed history.
func TestBootstrapBirthCommitLeavesExistingContentUntracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user-content.md"), []byte("operator data"), 0o644); err != nil {
		t.Fatalf("seed user content: %v", err)
	}

	s, err := New(dir, testSigner(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	var lsBuf bytes.Buffer
	if err := s.git(t.Context(), nil, &lsBuf, "ls-files"); err != nil {
		t.Fatalf("ls-files: %v", err)
	}
	tracked := strings.TrimSpace(lsBuf.String())
	if strings.Contains(tracked, "user-content.md") {
		t.Errorf("user-content.md was auto-tracked; want untracked. tracked=%q", tracked)
	}
	if !strings.Contains(tracked, ".gitignore") {
		t.Errorf(".gitignore should be tracked after bootstrap. tracked=%q", tracked)
	}
}
