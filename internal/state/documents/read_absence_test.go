package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newRequiredRootStore(t *testing.T) (*Store, string) {
	t.Helper()

	kbDir := filepath.Join(t.TempDir(), "kb")
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
	return store, kbDir
}

// TestReadMissingDocumentReportsNotFound covers the confusion that cost
// production real forensic time. Reading a document that was never
// written used to report "blocked by signature policy: file is not
// tracked in HEAD", which reads as a trust problem and sends the reader
// to check signers, policy, and root health — all of which were fine.
// Absence is not a trust verdict, and the answer must say so.
func TestReadMissingDocumentReportsNotFound(t *testing.T) {
	t.Parallel()

	store, _ := newRequiredRootStore(t)

	_, err := store.Read(context.Background(), "kb:never/written.md")
	if err == nil {
		t.Fatal("Read() of a missing document succeeded")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false; callers cannot distinguish absence from a fault", err)
	}
	if strings.Contains(err.Error(), "signature policy") {
		t.Errorf("error = %q, want absence reported as absence, not as a trust failure", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to say the document was not found", err)
	}
}

// TestReadStillVerifiesDocumentsThatExist is the other half: the
// reordering must not become a way to skip verification. A file that is
// present but untrusted is still refused, and still says why.
func TestReadStillVerifiesDocumentsThatExist(t *testing.T) {
	t.Parallel()

	store, kbDir := newRequiredRootStore(t)
	present := filepath.Join(kbDir, "present.md")
	if err := os.WriteFile(present, []byte("---\ntitle: Present\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := store.Read(context.Background(), "kb:present.md")
	if err == nil {
		t.Fatal("Read() served an untrusted document from a required-mode root")
	}
	if !strings.Contains(err.Error(), "blocked by signature policy") {
		t.Errorf("error = %q, want the signature policy refusal for content that exists", err)
	}
	if IsNotFound(err) {
		t.Errorf("error = %q, want a trust failure rather than an absence", err)
	}
}

// TestVerifyPathStillBlocksMissingFilesInRequiredRoots pins the
// property I nearly broke while fixing the read path. VerifyPath also
// guards new-file writes: a path that does not exist yet must still be
// checked, or writing to an unwritten path would skip policy entirely.
// The read-side reordering is deliberately scoped to reads for exactly
// this reason.
func TestVerifyPathStillBlocksMissingFilesInRequiredRoots(t *testing.T) {
	t.Parallel()

	store, kbDir := newRequiredRootStore(t)

	err := store.VerifyPath(context.Background(), filepath.Join(kbDir, "subdir", "new-file.md"), "file_tools_write")
	if err == nil {
		t.Fatal("VerifyPath() waved through a missing file in a required root — that is a write bypass")
	}
	if !strings.Contains(err.Error(), "blocked by signature policy") {
		t.Errorf("error = %q, want the policy block preserved for the write path", err)
	}
}
