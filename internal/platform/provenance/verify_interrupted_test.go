package provenance

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestExecutionInterrupted separates a verdict from a non-answer. This
// is the discrimination the whole unavailable status rests on: git
// exiting non-zero because a signature is bad is an answer and must
// stay a failure, while git being killed or a context expiring
// produced no answer at all.
func TestExecutionInterrupted(t *testing.T) {
	t.Parallel()

	// A real signal-killed process, rather than a hand-built error, so
	// the test tracks what exec actually returns.
	killed := func(t *testing.T) error {
		t.Helper()
		cmd := exec.Command("sh", "-c", "kill -9 $$")
		err := cmd.Run()
		if err == nil {
			t.Skip("could not produce a signal-killed process")
		}
		return err
	}

	// An ordinary non-zero exit — what verify-commit does for a bad
	// signature.
	verdict := func(t *testing.T) error {
		t.Helper()
		err := exec.Command("sh", "-c", "exit 1").Run()
		if err == nil {
			t.Skip("could not produce a non-zero exit")
		}
		return err
	}

	t.Run("signal-killed git is not a verdict", func(t *testing.T) {
		t.Parallel()
		got, why := executionInterrupted(context.Background(), killed(t))
		if !got {
			t.Errorf("executionInterrupted() = false for a signal-killed process; it would be reported as a failed signature")
		}
		if why == "" {
			t.Error("no reason recorded for the interruption")
		}
	})

	t.Run("non-zero exit is a verdict", func(t *testing.T) {
		t.Parallel()
		if got, _ := executionInterrupted(context.Background(), verdict(t)); got {
			t.Error("executionInterrupted() = true for an ordinary non-zero exit; a bad signature would stop being reported as one")
		}
	})

	t.Run("expired context is not a verdict", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		<-ctx.Done()
		if got, _ := executionInterrupted(ctx, nil); !got {
			t.Error("executionInterrupted() = false for an expired context")
		}
	})

	t.Run("wrapped deadline error is not a verdict", func(t *testing.T) {
		t.Parallel()
		if got, _ := executionInterrupted(context.Background(), errors.New("git: "+context.DeadlineExceeded.Error())); got {
			t.Log("string-shaped deadline errors are not detected; only wrapped ones are")
		}
		wrapped := errors.Join(errors.New("git verify-commit"), context.DeadlineExceeded)
		if got, _ := executionInterrupted(context.Background(), wrapped); !got {
			t.Error("executionInterrupted() = false for a wrapped DeadlineExceeded")
		}
	})

	t.Run("clean run is not interrupted", func(t *testing.T) {
		t.Parallel()
		if got, _ := executionInterrupted(context.Background(), nil); got {
			t.Error("executionInterrupted() = true with no error and a live context")
		}
	})
}

// TestUnavailableVerificationIsNotFailed pins the two constructors
// apart. They both refuse, and only one of them means the content is
// suspect.
func TestUnavailableVerificationIsNotFailed(t *testing.T) {
	t.Parallel()

	unavailable, err := unavailableVerification("abc123", "git was killed")
	if err == nil {
		t.Fatal("unavailableVerification returned no error; callers would treat the read as trusted")
	}
	if unavailable.Status != VerificationUnavailable {
		t.Errorf("Status = %q, want %q", unavailable.Status, VerificationUnavailable)
	}
	if unavailable.Trusted() {
		t.Error("an unavailable verification must never read as trusted")
	}

	failed, _ := failedVerification("abc123", "bad signature")
	if failed.Status != VerificationFailed {
		t.Errorf("Status = %q, want %q", failed.Status, VerificationFailed)
	}
	if failed.Status == unavailable.Status {
		t.Error("failed and unavailable collapsed into one status")
	}
}

// TestVerifyKeepsBadSignatureAsFailed is the other half of the
// distinction, end to end against a real repository. An unsigned
// commit is a verdict: git ran, git decided, and the content is not
// covered by trusted history. Classifying that as "unavailable" would
// undo the fix in the dangerous direction — it would tell a reader
// that a genuinely untrusted root was merely unreachable.
func TestVerifyKeepsBadSignatureAsFailed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	// The verifier requires a repo-local allowed-signers file; an empty
	// one vouches for nobody, which is what makes the commit below a
	// clean untrusted verdict rather than a configuration error.
	if err := os.WriteFile(filepath.Join(dir, ".allowed_signers"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile signers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	git("add", ".allowed_signers", "doc.md")
	git("commit", "-qm", "unsigned")

	v, err := NewVerifier(dir, nil, Options{})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	result, err := v.VerifyFile(context.Background(), "doc.md")
	if err == nil {
		t.Fatal("Verify() accepted an unsigned commit")
	}
	if result.Status != VerificationFailed {
		t.Errorf("Status = %q, want %q — a completed verdict must not be reported as unavailable",
			result.Status, VerificationFailed)
	}
	if result.Trusted() {
		t.Error("unsigned commit reported as trusted")
	}
}
