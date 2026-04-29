package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakePathVerifier implements PathVerifier for tests. It records each
// call and returns whatever err is configured (nil for trusted reads,
// non-nil to simulate a required-policy block).
type fakePathVerifier struct {
	err   error
	calls []verifyCall
}

type verifyCall struct {
	path     string
	consumer string
}

func (f *fakePathVerifier) VerifyPath(_ context.Context, path string, consumer string) error {
	f.calls = append(f.calls, verifyCall{path: path, consumer: consumer})
	return f.err
}

// TestFileTools_Read_VerifierBlocks confirms that a verifier that
// reports a policy violation prevents the read from returning
// content — closing the bypass surfaced by issue #788.
func TestFileTools_Read_VerifierBlocks(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "secret.md")
	if err := os.WriteFile(target, []byte("classified"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	ft := NewFileTools(workspace, nil)
	verifier := &fakePathVerifier{err: errors.New("blocked by signature policy")}
	ft.SetPathVerifier(verifier)

	_, err := ft.Read(context.Background(), "secret.md", 0, 0)
	if err == nil {
		t.Fatal("Read should propagate verifier error")
	}
	if len(verifier.calls) != 1 {
		t.Fatalf("expected 1 verifier call, got %d", len(verifier.calls))
	}
	if verifier.calls[0].consumer != "file_tools_read" {
		t.Errorf("consumer = %q, want file_tools_read", verifier.calls[0].consumer)
	}
}

// TestFileTools_Read_VerifierAllows confirms that a passing verifier
// is transparent — content is returned as before.
func TestFileTools_Read_VerifierAllows(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "ok.md")
	if err := os.WriteFile(target, []byte("trusted"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	ft := NewFileTools(workspace, nil)
	verifier := &fakePathVerifier{}
	ft.SetPathVerifier(verifier)

	got, err := ft.Read(context.Background(), "ok.md", 0, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "trusted" {
		t.Errorf("Read = %q, want trusted", got)
	}
	if len(verifier.calls) != 1 {
		t.Fatalf("expected 1 verifier call, got %d", len(verifier.calls))
	}
}

// TestFileTools_Write_VerifierBlocks confirms that a verifier
// rejection prevents the write from happening.
func TestFileTools_Write_VerifierBlocks(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "out.md")

	ft := NewFileTools(workspace, nil)
	verifier := &fakePathVerifier{err: errors.New("blocked")}
	ft.SetPathVerifier(verifier)

	if err := ft.Write(context.Background(), "out.md", "data"); err == nil {
		t.Fatal("Write should propagate verifier error")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should not have been written; stat err = %v", err)
	}
	if len(verifier.calls) != 1 || verifier.calls[0].consumer != "file_tools_write" {
		t.Errorf("verifier calls = %#v, want exactly one file_tools_write", verifier.calls)
	}
}

// TestFileTools_Edit_VerifierBlocks confirms that Edit consults the
// verifier before the read-modify-write sequence.
func TestFileTools_Edit_VerifierBlocks(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "doc.md")
	original := "alpha"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	ft := NewFileTools(workspace, nil)
	verifier := &fakePathVerifier{err: errors.New("blocked")}
	ft.SetPathVerifier(verifier)

	if err := ft.Edit(context.Background(), "doc.md", "alpha", "beta"); err == nil {
		t.Fatal("Edit should propagate verifier error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != original {
		t.Errorf("file contents mutated despite verifier rejection: got %q", got)
	}
	if len(verifier.calls) != 1 || verifier.calls[0].consumer != "file_tools_edit" {
		t.Errorf("verifier calls = %#v, want exactly one file_tools_edit", verifier.calls)
	}
}

// TestFileTools_Read_NoVerifierUnchanged confirms that the verifier
// hook is opt-in: when SetPathVerifier hasn't been called, Read works
// as it did before.
func TestFileTools_Read_NoVerifierUnchanged(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "plain.md")
	if err := os.WriteFile(target, []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	ft := NewFileTools(workspace, nil)
	got, err := ft.Read(context.Background(), "plain.md", 0, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "hello" {
		t.Errorf("Read = %q, want hello", got)
	}
}
