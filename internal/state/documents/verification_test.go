package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// TestStoreVerifyPath_MissingFileInsideRequiredRootBlocked covers the
// new-file write / missing inject-file path. The file does not exist
// on disk yet, but it lives inside a managed `required` root, so the
// verifier must run and the call must error rather than silently
// passing through. Without [evalSymlinksAllowingMissing] the original
// EvalSymlinks would have returned an "no such file or directory"
// error from rootRefForPath and disguised the actual policy decision.
func TestStoreVerifyPath_MissingFileInsideRequiredRootBlocked(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStoreWithOptions(db, map[string]string{"kb": kbDir}, nil, StoreOptions{
		RootPolicies: map[string]RootPolicy{"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
			Git:       RootGitPolicy{Enabled: true, VerifySignatures: VerificationRequired},
		}},
		RootVerifiers: map[string]RootVerifier{"kb": fakeRootVerifier{}},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}

	missing := filepath.Join(kbDir, "subdir", "new-file.md")
	err = store.VerifyPath(context.Background(), missing, "file_tools_write")
	if err == nil {
		t.Fatal("VerifyPath should block a missing file inside a required-mode root")
	}
	if !strings.Contains(err.Error(), "blocked by signature policy") {
		t.Fatalf("error = %v, want signature policy block (not a fs error)", err)
	}
}

// TestStoreVerifyPath_MissingFileOutsideRootIsPassthrough confirms
// that a missing file in a path with no managed-root ancestor stays
// a pure passthrough — that's the warden behavior file-tool writes
// outside any doc root rely on.
func TestStoreVerifyPath_MissingFileOutsideRootIsPassthrough(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStoreWithOptions(db, map[string]string{"kb": kbDir}, nil, StoreOptions{
		RootPolicies: map[string]RootPolicy{"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
			Git:       RootGitPolicy{Enabled: true, VerifySignatures: VerificationRequired},
		}},
		RootVerifiers: map[string]RootVerifier{"kb": fakeRootVerifier{}},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}

	// Path is outside kbDir, never existed.
	outside := filepath.Join(rootDir, "elsewhere", "scratch.md")
	if err := store.VerifyPath(context.Background(), outside, "file_tools_write"); err != nil {
		t.Fatalf("VerifyPath outside any root should be passthrough, got %v", err)
	}
}

// TestStoreVerifyPath_MissingFileWarnModeDoesNotBlock confirms that
// a missing file under warn-mode policy succeeds (the verifier still
// records a warning internally, but VerifyPath does not error). This
// is the path operators rely on for inject-files that may
// legitimately not exist yet — startup should not fail just because
// of mode=warn.
func TestStoreVerifyPath_MissingFileWarnModeDoesNotBlock(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStoreWithOptions(db, map[string]string{"kb": kbDir}, nil, StoreOptions{
		RootPolicies: map[string]RootPolicy{"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
			Git:       RootGitPolicy{Enabled: true, VerifySignatures: VerificationWarn},
		}},
		RootVerifiers: map[string]RootVerifier{"kb": fakeRootVerifier{}},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}

	missing := filepath.Join(kbDir, "deeply", "nested", "absent.md")
	if err := store.VerifyPath(context.Background(), missing, "inject_files"); err != nil {
		t.Fatalf("warn mode should not block a missing file; got %v", err)
	}
}
