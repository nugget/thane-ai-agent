package provenance

import (
	"bytes"
	"os/exec"
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

	if err := s.Write("test.md", "signed content", "test-signed"); err != nil {
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
		if err := s.Write("doc.md", content, "write-"+content); err != nil {
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

	if err := s.Write("file.md", "data", "loop-metacognitive-42"); err != nil {
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
