package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

type recordingRootWriter struct {
	root    string
	writes  []string
	deletes []string
}

type fakeRootVerifier struct {
	files map[string]SignatureVerification
	root  SignatureVerification
}

func (v fakeRootVerifier) Verify(_ context.Context, filename string) (SignatureVerification, error) {
	result, ok := v.files[filename]
	if !ok {
		result = SignatureVerification{Status: SignatureFailed, Message: "untrusted test document"}
	}
	if result.Status == SignatureTrusted {
		return result, nil
	}
	return result, os.ErrPermission
}

func (v fakeRootVerifier) VerifyRoot(_ context.Context) (SignatureVerification, error) {
	result := v.root
	if result.Status == "" {
		result = SignatureVerification{Status: SignatureTrusted, Message: "trusted test root"}
	}
	if result.Status == SignatureTrusted {
		return result, nil
	}
	return result, os.ErrPermission
}

func (w *recordingRootWriter) Write(_ context.Context, filename, content, message string) error {
	w.writes = append(w.writes, filename+"|"+message)
	path := filepath.Join(w.root, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (w *recordingRootWriter) Delete(_ context.Context, filename, message string) error {
	w.deletes = append(w.deletes, filename+"|"+message)
	return os.Remove(filepath.Join(w.root, filename))
}

func TestStorePolicyReadOnlyBlocksManagedMutations(t *testing.T) {
	t.Parallel()

	store, _ := newPolicyStore(t, map[string]RootPolicy{
		"kb": {
			Indexing:  true,
			Authoring: AuthoringReadOnly,
		},
	}, nil)
	body := "Denied."
	_, err := store.Write(context.Background(), WriteArgs{
		Ref:  "kb:blocked.md",
		Body: &body,
	})
	if err == nil {
		t.Fatal("Write returned nil, want authoring policy error")
	}
	if !strings.Contains(err.Error(), `document root "kb" authoring is "read_only"`) {
		t.Fatalf("Write error = %v, want read_only policy error", err)
	}
}

func TestStorePolicyIndexingFalseSkipsRefreshAndWriteIndexing(t *testing.T) {
	t.Parallel()

	store, kbDir := newPolicyStore(t, map[string]RootPolicy{
		"kb": {
			Indexing:  false,
			Authoring: AuthoringManaged,
		},
	}, nil)
	writeFile(t, filepath.Join(kbDir, "direct.md"), "# Direct\n\nIndexed nowhere.\n")

	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	results, err := store.Search(ctx, SearchQuery{Root: "kb", Query: "nowhere", Limit: 10})
	if err != nil {
		t.Fatalf("Search before write: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search before write returned %d results, want 0", len(results))
	}

	body := "Still not indexed."
	if _, err := store.Write(ctx, WriteArgs{Ref: "kb:managed.md", Body: &body}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	results, err = store.Search(ctx, SearchQuery{Root: "kb", Query: "indexed", Limit: 10})
	if err != nil {
		t.Fatalf("Search after write: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search after write returned %d results, want 0", len(results))
	}
	roots, err := store.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].Root != "kb" || roots[0].DocumentCount != 0 || roots[0].Policy.Indexing {
		t.Fatalf("Roots = %#v, want visible non-indexed kb root with zero indexed docs", roots)
	}
}

func TestStorePolicyIndexingFalsePurgesPreviouslyIndexedRows(t *testing.T) {
	t.Parallel()

	store, kbDir := newPolicyStore(t, map[string]RootPolicy{
		"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
		},
	}, nil)
	writeFile(t, filepath.Join(kbDir, "stale.md"), "# Stale\n\nPreviously searchable.\n")

	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	results, err := store.Search(ctx, SearchQuery{Root: "kb", Query: "searchable", Limit: 10})
	if err != nil {
		t.Fatalf("initial Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("initial Search returned %d results, want 1", len(results))
	}

	store.rootPolicies["kb"] = RootPolicy{
		Indexing:  false,
		Authoring: AuthoringManaged,
	}
	store.lastRefresh = time.Time{}
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("policy Refresh: %v", err)
	}
	results, err = store.Search(ctx, SearchQuery{Root: "kb", Query: "searchable", Limit: 10})
	if err != nil {
		t.Fatalf("Search after disabling indexing: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search after disabling indexing returned %d results, want 0", len(results))
	}
	roots, err := store.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].DocumentCount != 0 || roots[0].LastModifiedAt != "" {
		t.Fatalf("Roots = %#v, want purged non-indexed root stats", roots)
	}
}

func TestStorePolicyRootWriterHandlesWriteAndDelete(t *testing.T) {
	t.Parallel()

	writer := &recordingRootWriter{}
	store, kbDir := newPolicyStore(t, map[string]RootPolicy{
		"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
			Git: RootGitPolicy{
				Enabled:     true,
				SignCommits: true,
			},
		},
	}, map[string]RootWriter{"kb": writer})
	writer.root = kbDir

	ctx := context.Background()
	body := "Committed by root writer."
	if _, err := store.Write(ctx, WriteArgs{Ref: "kb:writer/doc.md", Body: &body}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(writer.writes) != 1 || writer.writes[0] != "writer/doc.md|doc_write kb:writer/doc.md" {
		t.Fatalf("writer.writes = %#v, want one doc_write call", writer.writes)
	}

	if _, err := store.Delete(ctx, DeleteArgs{Ref: "kb:writer/doc.md"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(writer.deletes) != 1 || writer.deletes[0] != "writer/doc.md|doc_delete kb:writer/doc.md" {
		t.Fatalf("writer.deletes = %#v, want one doc_delete call", writer.deletes)
	}
}

func TestStorePolicyRequiredSignatureBlocksReadAndIndex(t *testing.T) {
	t.Parallel()

	store, kbDir := newPolicyStoreWithOptions(t, map[string]RootPolicy{
		"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
			Git: RootGitPolicy{
				Enabled:          true,
				VerifySignatures: VerificationRequired,
			},
		},
	}, nil, map[string]RootVerifier{"kb": fakeRootVerifier{
		files: map[string]SignatureVerification{
			"unsafe.md": {Status: SignatureFailed, Message: "unsigned test content"},
		},
		root: SignatureVerification{Status: SignatureFailed, Message: "dirty root"},
	}})
	writeFile(t, filepath.Join(kbDir, "unsafe.md"), "# Unsafe\n\nShould not load.\n")

	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	results, err := store.Search(ctx, SearchQuery{Root: "kb", Query: "Unsafe", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search returned %d results, want unsigned document excluded", len(results))
	}
	if _, err := store.Read(ctx, "kb:unsafe.md"); err == nil || !strings.Contains(err.Error(), "blocked by signature policy") {
		t.Fatalf("Read error = %v, want signature policy block", err)
	}
	roots, err := store.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if roots[0].Verification == nil || roots[0].Verification.Status != SignatureFailed {
		t.Fatalf("Roots verification = %#v, want failed", roots[0].Verification)
	}
}

func TestStorePolicyWarnSignatureDoesNotBlockReadOrIndex(t *testing.T) {
	t.Parallel()

	store, kbDir := newPolicyStoreWithOptions(t, map[string]RootPolicy{
		"kb": {
			Indexing:  true,
			Authoring: AuthoringManaged,
			Git: RootGitPolicy{
				Enabled:          true,
				VerifySignatures: VerificationWarn,
			},
		},
	}, nil, map[string]RootVerifier{"kb": fakeRootVerifier{
		files: map[string]SignatureVerification{
			"warning.md": {Status: SignatureFailed, Message: "unsigned but warn-only"},
		},
		root: SignatureVerification{Status: SignatureFailed, Message: "dirty root"},
	}})
	writeFile(t, filepath.Join(kbDir, "warning.md"), "# Warning\n\nWarn mode still loads.\n")

	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	results, err := store.Search(ctx, SearchQuery{Root: "kb", Query: "Warning", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search returned %d results, want warn-mode document included", len(results))
	}
	if _, err := store.Read(ctx, "kb:warning.md"); err != nil {
		t.Fatalf("Read warn-mode document: %v", err)
	}
}

func newPolicyStore(t *testing.T, policies map[string]RootPolicy, writers map[string]RootWriter) (*Store, string) {
	return newPolicyStoreWithOptions(t, policies, writers, nil)
}

func newPolicyStoreWithOptions(t *testing.T, policies map[string]RootPolicy, writers map[string]RootWriter, verifiers map[string]RootVerifier) (*Store, string) {
	t.Helper()

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
		RootPolicies:  policies,
		RootWriters:   writers,
		RootVerifiers: verifiers,
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store, kbDir
}
