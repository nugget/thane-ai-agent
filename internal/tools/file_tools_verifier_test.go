package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePathVerifier implements PathVerifier for tests. It records each
// call and returns whatever err is configured (nil for trusted reads,
// non-nil to simulate a required-policy block).
type fakePathVerifier struct {
	err         error
	mutationErr error
	verify      func(path, consumer string) error
	calls       []verifyCall
}

type verifyCall struct {
	path     string
	consumer string
}

func (f *fakePathVerifier) VerifyPath(_ context.Context, path string, consumer string) error {
	f.calls = append(f.calls, verifyCall{path: path, consumer: consumer})
	if f.verify != nil {
		return f.verify(path, consumer)
	}
	return f.err
}

func (f *fakePathVerifier) VerifyMutationPath(_ context.Context, path string, consumer string) error {
	f.calls = append(f.calls, verifyCall{path: path, consumer: consumer})
	if f.mutationErr != nil {
		return f.mutationErr
	}
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

func TestFileTools_Write_UsesMutationVerifier(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "out.md")

	ft := NewFileTools(workspace, nil)
	verifier := &fakePathVerifier{mutationErr: errors.New("raw mutation blocked")}
	ft.SetPathVerifier(verifier)

	if err := ft.Write(context.Background(), "out.md", "data"); err == nil {
		t.Fatal("Write should propagate mutation verifier error")
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

func TestFileTools_MetadataAndWalkSurfacesRespectManagedDocumentBoundary(t *testing.T) {
	workspace := t.TempDir()
	managed := filepath.Join(workspace, "managed")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("MkdirAll managed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managed, "private.md"), []byte("PRIVATE_CODEC_MARKER"), 0o600); err != nil {
		t.Fatalf("seed managed file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "public.txt"), []byte("PUBLIC_MARKER"), 0o600); err != nil {
		t.Fatalf("seed public file: %v", err)
	}

	ft := NewFileTools(workspace, nil)
	managedResolved, err := filepath.EvalSymlinks(managed)
	if err != nil {
		t.Fatalf("EvalSymlinks managed: %v", err)
	}
	ft.SetPathVerifier(&fakePathVerifier{verify: func(path, _ string) error {
		if path == managedResolved || strings.HasPrefix(path, managedResolved+string(filepath.Separator)) {
			return errors.New("indexed managed document path; use doc_read")
		}
		return nil
	}})

	for name, call := range map[string]func() error{
		"list":   func() error { _, err := ft.List(t.Context(), "managed"); return err },
		"search": func() error { _, err := ft.Search(t.Context(), "managed", "*.md", 2); return err },
		"grep":   func() error { _, err := ft.Grep(t.Context(), "managed", "MARKER", "", 2, false); return err },
		"tree":   func() error { _, err := ft.Tree(t.Context(), "managed", 2); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil || !strings.Contains(err.Error(), "doc_read") {
				t.Fatalf("direct managed access error = %v, want document-tool redirect", err)
			}
		})
	}

	search, err := ft.Search(t.Context(), ".", "*", 3)
	if err != nil {
		t.Fatalf("Search workspace: %v", err)
	}
	if !strings.Contains(search, "public.txt") || strings.Contains(search, "private.md") {
		t.Fatalf("workspace search crossed managed boundary: %s", search)
	}
	grep, err := ft.Grep(t.Context(), ".", "MARKER", "", 3, false)
	if err != nil {
		t.Fatalf("Grep workspace: %v", err)
	}
	if !strings.Contains(grep, "PUBLIC_MARKER") || strings.Contains(grep, "PRIVATE_CODEC_MARKER") {
		t.Fatalf("workspace grep crossed managed boundary: %s", grep)
	}
	tree, err := ft.Tree(t.Context(), ".", 3)
	if err != nil {
		t.Fatalf("Tree workspace: %v", err)
	}
	if !strings.Contains(tree, "public.txt") || strings.Contains(tree, "private.md") {
		t.Fatalf("workspace tree crossed managed boundary: %s", tree)
	}
	stat, err := ft.Stat(t.Context(), "managed/private.md,public.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !strings.Contains(stat, "use doc_read") || !strings.Contains(stat, "public.txt: type=file") {
		t.Fatalf("stat boundary result = %s", stat)
	}
}
