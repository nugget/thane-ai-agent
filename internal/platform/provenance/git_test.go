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
