package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// fakeRootVerifier is a copy of the documents-package internal fake,
// scoped to this test file so the app package can exercise verifier
// integration without depending on test fixtures from another package.
type fakeRootVerifier struct {
	files map[string]documents.SignatureVerification
}

func (v fakeRootVerifier) Verify(_ context.Context, filename string) (documents.SignatureVerification, error) {
	result, ok := v.files[filename]
	if !ok {
		result = documents.SignatureVerification{Status: documents.SignatureFailed, Message: "untrusted test document"}
	}
	if result.Status == documents.SignatureTrusted {
		return result, nil
	}
	return result, errors.New(result.Message)
}

func (v fakeRootVerifier) VerifyRoot(_ context.Context) (documents.SignatureVerification, error) {
	return documents.SignatureVerification{Status: documents.SignatureTrusted}, nil
}

// newVerifyTestStore wires a documents.Store with one root ("kb")
// rooted at kbDir, the supplied policy, and the supplied verifier.
// The store has no writers — verifyStartupReads only needs read-side
// verification.
func newVerifyTestStore(t *testing.T, kbDir string, policy documents.RootPolicy, verifier documents.RootVerifier) *documents.Store {
	t.Helper()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := documents.NewStoreWithOptions(db, map[string]string{"kb": kbDir}, nil, documents.StoreOptions{
		RootPolicies:  map[string]documents.RootPolicy{"kb": policy},
		RootVerifiers: map[string]documents.RootVerifier{"kb": verifier},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestVerifyStartupReads_RequiredBlocksUnsignedInjectFile confirms
// that an inject-file living inside a managed root with
// verify_signatures: required fails startup when the verifier says
// the file isn't trusted. This is the core #788 guarantee — a model
// that asks for `core:config.yaml` should not be able to bypass the
// gate by being injected via the runtime startup path either.
func TestVerifyStartupReads_RequiredBlocksUnsignedInjectFile(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	injectPath := filepath.Join(kbDir, "mission.md")
	writeTestFile(t, injectPath, "# Mission\n")

	verifier := fakeRootVerifier{
		files: map[string]documents.SignatureVerification{
			"mission.md": {Status: documents.SignatureFailed, Message: "tampered inject content"},
		},
	}
	store := newVerifyTestStore(t, kbDir, documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git:       documents.RootGitPolicy{Enabled: true, VerifySignatures: documents.VerificationRequired},
	}, verifier)

	a := &App{cfg: &config.Config{}}
	err := a.verifyStartupReads(context.Background(), store, []string{injectPath})
	if err == nil {
		t.Fatal("verifyStartupReads should block unsigned inject file under required mode")
	}
	if !strings.Contains(err.Error(), "inject_files verification") {
		t.Fatalf("error = %v, want inject_files verification wrapper", err)
	}
}

// TestVerifyStartupReads_WarnDoesNotBlock confirms that warn mode
// allows startup to proceed even when verification fails. The doc
// store logs the warning internally; verifyStartupReads sees nil.
func TestVerifyStartupReads_WarnDoesNotBlock(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	injectPath := filepath.Join(kbDir, "mission.md")
	writeTestFile(t, injectPath, "# Mission\n")

	verifier := fakeRootVerifier{
		files: map[string]documents.SignatureVerification{
			"mission.md": {Status: documents.SignatureFailed, Message: "warn-mode untrusted"},
		},
	}
	store := newVerifyTestStore(t, kbDir, documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git:       documents.RootGitPolicy{Enabled: true, VerifySignatures: documents.VerificationWarn},
	}, verifier)

	a := &App{cfg: &config.Config{}}
	if err := a.verifyStartupReads(context.Background(), store, []string{injectPath}); err != nil {
		t.Fatalf("warn mode should not block startup; got %v", err)
	}
}

// TestVerifyStartupReads_NoneSkipsVerification confirms that when
// the policy is none, the verifier is not consulted at all and any
// inject-file content loads regardless of signing state.
func TestVerifyStartupReads_NoneSkipsVerification(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	injectPath := filepath.Join(kbDir, "mission.md")
	writeTestFile(t, injectPath, "# Mission\n")

	// Verifier is configured with "this would fail" but the none
	// policy short-circuits before the verifier is called.
	verifier := fakeRootVerifier{
		files: map[string]documents.SignatureVerification{
			"mission.md": {Status: documents.SignatureFailed, Message: "should not be consulted"},
		},
	}
	store := newVerifyTestStore(t, kbDir, documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git:       documents.RootGitPolicy{Enabled: true, VerifySignatures: documents.VerificationNone},
	}, verifier)

	a := &App{cfg: &config.Config{}}
	if err := a.verifyStartupReads(context.Background(), store, []string{injectPath}); err != nil {
		t.Fatalf("none mode should not block; got %v", err)
	}
}

// TestVerifyStartupReads_OutsideAnyRootIsPassthrough confirms that
// inject-files living outside every managed root pass through
// without verification. This preserves the legitimate operator
// workflow of injecting files from arbitrary disk locations.
func TestVerifyStartupReads_OutsideAnyRootIsPassthrough(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	outside := filepath.Join(rootDir, "outside.md")
	writeTestFile(t, outside, "# Outside\n")

	verifier := fakeRootVerifier{}
	store := newVerifyTestStore(t, kbDir, documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git:       documents.RootGitPolicy{Enabled: true, VerifySignatures: documents.VerificationRequired},
	}, verifier)

	a := &App{cfg: &config.Config{}}
	if err := a.verifyStartupReads(context.Background(), store, []string{outside}); err != nil {
		t.Fatalf("paths outside any managed root should be passthrough; got %v", err)
	}
}

// TestVerifyStartupReads_BlocksUnsignedTalent confirms that talent
// markdown files inside a managed required-mode root are checked the
// same way as inject-files. A loader that bypasses the doc store can
// otherwise serve untrusted behavioral guidance.
func TestVerifyStartupReads_BlocksUnsignedTalent(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	talentDir := filepath.Join(kbDir, "talents")
	talentFile := filepath.Join(talentDir, "rogue.md")
	writeTestFile(t, talentFile, "---\ntags: [evil]\n---\nrogue talent\n")

	// fakeRootVerifier defaults unknown filenames to SignatureFailed,
	// so the rogue talent gets blocked without a per-file entry.
	verifier := fakeRootVerifier{}
	store := newVerifyTestStore(t, kbDir, documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git:       documents.RootGitPolicy{Enabled: true, VerifySignatures: documents.VerificationRequired},
	}, verifier)

	a := &App{cfg: &config.Config{TalentsDir: talentDir}}
	err := a.verifyStartupReads(context.Background(), store, nil)
	if err == nil {
		t.Fatal("verifyStartupReads should block unsigned talent under required mode")
	}
	if !strings.Contains(err.Error(), "talents verification") {
		t.Fatalf("error = %v, want talents verification wrapper", err)
	}
}

// TestVerifyStartupReads_NilStoreIsNoop guards the early-return
// path: a workspace without managed document roots simply has no
// store, and verifyStartupReads should not crash or error.
func TestVerifyStartupReads_NilStoreIsNoop(t *testing.T) {
	t.Parallel()

	a := &App{cfg: &config.Config{}}
	if err := a.verifyStartupReads(context.Background(), nil, []string{"/anywhere/mission.md"}); err != nil {
		t.Fatalf("nil store should be no-op; got %v", err)
	}
}
