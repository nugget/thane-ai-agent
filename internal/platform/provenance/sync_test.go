package provenance

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syncFixture holds the pieces shared by the sync tests: an out-of-tree
// allowed-signers anchor trusting thane's key, and thane's signer.
type syncFixture struct {
	signer *SSHFileSigner
	anchor string
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	signer := testSigner(t)
	anchorDir := t.TempDir()
	anchor := filepath.Join(anchorDir, "kb.allowed_signers")
	if err := os.WriteFile(anchor, []byte(AgentPrincipal+" "+signer.PublicKey()+"\n"), 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	return syncFixture{signer: signer, anchor: anchor}
}

// newSyncStore builds a provenance store verifying against the fixture's
// out-of-tree anchor, signed by the given signer (thane's by default).
func (f syncFixture) newSyncStore(t *testing.T, signer *SSHFileSigner) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewWithOptions(dir, signer, slog.Default(), Options{AllowedSignersPath: f.anchor})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return s
}

func writeStore(t *testing.T, s *Store, name, content string) {
	t.Helper()
	if err := s.Write(t.Context(), name, content, "commit "+name); err != nil {
		t.Fatalf("Write %s: %v", name, err)
	}
}

func headBranch(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "symbolic-ref", "--short", "HEAD"))
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

// cloneWorktree clones src into a fresh non-bare repo tracking the same
// default branch and returns its path. Used when the remote must accept new
// commits authored through a provenance store.
func cloneWorktree(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "remote")
	runGitAt(t, "clone", "--quiet", src, dst)
	return dst
}

// cloneBare clones src into a bare repo (which accepts pushes to any branch)
// and returns its path.
func cloneBare(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "remote.git")
	runGitAt(t, "clone", "--quiet", "--bare", src, dst)
	return dst
}

func runGitAt(t *testing.T, args ...string) {
	t.Helper()
	// runGit requires a -C dir; for clone the dir is implicit, so shell out
	// via runGit against the current directory using an explicit form.
	runGit(t, ".", args...)
}

// TestSyncClean: local and remote agree — the pass touches nothing.
func TestSyncClean(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	branch := headBranch(t, local.path)
	remote := cloneBare(t, local.path)

	before := headSHA(t, local.path)
	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncClean {
		t.Fatalf("Outcome = %q, want clean (detail %q)", res.Outcome, res.Detail)
	}
	if headSHA(t, local.path) != before {
		t.Fatalf("local head moved on a clean sync")
	}
}

// TestSyncFastForwardTrusted: remote is ahead by trusted commits — local
// fast-forwards to it.
func TestSyncFastForwardTrusted(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	base := headSHA(t, local.path)
	branch := headBranch(t, local.path)

	// Advance local two trusted commits, clone the remote at that tip, then
	// rewind local to base so the remote is two ahead.
	writeStore(t, local, "b.md", "b\n")
	writeStore(t, local, "c.md", "c\n")
	tip := headSHA(t, local.path)
	remote := cloneBare(t, local.path)
	runGit(t, local.path, "reset", "--hard", base)

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncFastForwarded {
		t.Fatalf("Outcome = %q, want fast_forwarded (detail %q)", res.Outcome, res.Detail)
	}
	if got := headSHA(t, local.path); got != tip {
		t.Fatalf("local head = %s, want %s (should have advanced to remote tip)", shorten(got), shorten(tip))
	}
}

// TestSyncBlockedLaundering is the load-bearing security test: a range
// A(trusted)-X(untrusted)-T(trusted) whose TIP verifies but whose middle
// commit is signed by a key absent from the anchor. Range verification (not
// tip verification) must block, and local must not move.
func TestSyncBlockedLaundering(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n") // C1, trusted
	branch := headBranch(t, local.path)
	base := headSHA(t, local.path)

	remote := cloneWorktree(t, local.path)

	// C2 signed by an attacker key NOT in the anchor (a "U" verdict).
	attacker := testSigner(t)
	attackerStore, err := NewWithOptions(remote, attacker, slog.Default(), Options{AllowedSignersPath: f.anchor})
	if err != nil {
		t.Fatalf("attacker store: %v", err)
	}
	writeStore(t, attackerStore, "x.md", "x\n")

	// C3 signed by thane (trusted) on top — so the tip verifies.
	thaneRemote, err := NewWithOptions(remote, f.signer, slog.Default(), Options{AllowedSignersPath: f.anchor})
	if err != nil {
		t.Fatalf("thane remote store: %v", err)
	}
	writeStore(t, thaneRemote, "t.md", "t\n")

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncBlocked {
		t.Fatalf("Outcome = %q, want blocked (detail %q)", res.Outcome, res.Detail)
	}
	if !strings.Contains(res.Detail, "not trusted") {
		t.Fatalf("Detail = %q, want an untrusted-commit reason", res.Detail)
	}
	if headSHA(t, local.path) != base {
		t.Fatalf("local head moved despite a blocked laundering attempt")
	}
}

// TestSyncFastForwardNoVerify shows the mode is honored: with
// RequireVerify=false, an untrusted range still fast-forwards. (The
// operability layer sets RequireVerify=true for signed roots; this documents
// the engine mechanism.)
func TestSyncFastForwardNoVerify(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	branch := headBranch(t, local.path)

	remote := cloneWorktree(t, local.path)
	attacker := testSigner(t)
	attackerStore, err := NewWithOptions(remote, attacker, slog.Default(), Options{AllowedSignersPath: f.anchor})
	if err != nil {
		t.Fatalf("attacker store: %v", err)
	}
	writeStore(t, attackerStore, "x.md", "x\n")
	tip := headSHA(t, remote)

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeFetch, RequireVerify: false})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncFastForwarded {
		t.Fatalf("Outcome = %q, want fast_forwarded with verify off (detail %q)", res.Outcome, res.Detail)
	}
	if got := headSHA(t, local.path); got != tip {
		t.Fatalf("local head = %s, want %s", shorten(got), shorten(tip))
	}
}

// TestSyncBlockedDirtyWorktree: a behind local with uncommitted tracked
// changes refuses the fast-forward rather than clobbering them.
func TestSyncBlockedDirtyWorktree(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	base := headSHA(t, local.path)
	branch := headBranch(t, local.path)
	writeStore(t, local, "b.md", "b\n")
	remote := cloneBare(t, local.path)
	runGit(t, local.path, "reset", "--hard", base)

	// Dirty a tracked file.
	if err := os.WriteFile(filepath.Join(local.path, "a.md"), []byte("a dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty write: %v", err)
	}

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncBlocked {
		t.Fatalf("Outcome = %q, want blocked (detail %q)", res.Outcome, res.Detail)
	}
	if headSHA(t, local.path) != base {
		t.Fatalf("local head moved despite a dirty worktree")
	}
}

// TestSyncDiverged: both sides have unique commits — refuse with no effects.
func TestSyncDiverged(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	branch := headBranch(t, local.path)

	remote := cloneWorktree(t, local.path)
	remoteStore, err := NewWithOptions(remote, f.signer, slog.Default(), Options{AllowedSignersPath: f.anchor})
	if err != nil {
		t.Fatalf("remote store: %v", err)
	}
	writeStore(t, remoteStore, "r.md", "r\n") // remote-only commit
	writeStore(t, local, "l.md", "l\n")       // local-only commit
	localTip := headSHA(t, local.path)

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncDiverged {
		t.Fatalf("Outcome = %q, want diverged (detail %q)", res.Outcome, res.Detail)
	}
	if headSHA(t, local.path) != localTip {
		t.Fatalf("local head moved on a diverged sync")
	}
}

// TestSyncPush: a bidirectional local lead is pushed; fetch-only is not.
func TestSyncPush(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	branch := headBranch(t, local.path)
	remote := cloneBare(t, local.path)
	remoteBase := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/"+branch))

	writeStore(t, local, "b.md", "b\n") // local now one ahead
	localTip := headSHA(t, local.path)

	// Fetch-only: no push.
	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeFetch, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync fetch-only: %v", err)
	}
	if res.Outcome != SyncClean {
		t.Fatalf("fetch-only Outcome = %q, want clean", res.Outcome)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/"+branch)); got != remoteBase {
		t.Fatalf("remote advanced under fetch-only mode: %s", shorten(got))
	}

	// Bidirectional: push.
	res, err = local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync bidirectional: %v", err)
	}
	if res.Outcome != SyncPushed {
		t.Fatalf("bidirectional Outcome = %q, want pushed (detail %q)", res.Outcome, res.Detail)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/"+branch)); got != localTip {
		t.Fatalf("remote head = %s, want %s after push", shorten(got), shorten(localTip))
	}
}

// TestSyncBlockedDetachedHead: a detached HEAD refuses to sync.
func TestSyncBlockedDetachedHead(t *testing.T) {
	f := newSyncFixture(t)
	local := f.newSyncStore(t, f.signer)
	writeStore(t, local, "a.md", "a\n")
	branch := headBranch(t, local.path)
	remote := cloneBare(t, local.path)
	runGit(t, local.path, "checkout", "--detach")

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncBlocked {
		t.Fatalf("Outcome = %q, want blocked on detached HEAD (detail %q)", res.Outcome, res.Detail)
	}
}

// newInTreeStore builds a signing store that verifies against its own in-tree
// .allowed_signers (no external anchor) — the default trust model for a
// remote-synced root. ensureRepo bootstraps the file with the signer's key, so
// the signer's own commits verify as trusted.
func newInTreeStore(t *testing.T, signer *SSHFileSigner) *Store {
	t.Helper()
	s, err := New(t.TempDir(), signer, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestSyncInTreeFastForward proves a trusted fast-forward works with the
// in-tree .allowed_signers and no configured out-of-tree anchor.
func TestSyncInTreeFastForward(t *testing.T) {
	thane := testSigner(t)
	local := newInTreeStore(t, thane)
	writeStore(t, local, "a.md", "a\n")
	base := headSHA(t, local.path)
	branch := headBranch(t, local.path)

	writeStore(t, local, "b.md", "b\n")
	writeStore(t, local, "c.md", "c\n")
	tip := headSHA(t, local.path)
	remote := cloneBare(t, local.path)
	runGit(t, local.path, "reset", "--hard", base)

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncFastForwarded {
		t.Fatalf("Outcome = %q, want fast_forwarded (detail %q)", res.Outcome, res.Detail)
	}
	if got := headSHA(t, local.path); got != tip {
		t.Fatalf("local head = %s, want %s", shorten(got), shorten(tip))
	}
}

// TestSyncInTreeBlockedLaundering is the load-bearing test for the in-tree
// trust model: a laundered range A(trusted)-X(untrusted)-T(trusted) is blocked
// exactly as with an out-of-tree anchor. Verification runs against HEAD's
// in-tree .allowed_signers — a fetch never rewrites the worktree before the
// range is checked — so the attacker's middle commit is judged against a trust
// set that does not contain its key, and local never moves.
func TestSyncInTreeBlockedLaundering(t *testing.T) {
	thane := testSigner(t)
	local := newInTreeStore(t, thane)
	writeStore(t, local, "a.md", "a\n") // C1, thane-signed
	branch := headBranch(t, local.path)
	base := headSHA(t, local.path)

	remote := cloneWorktree(t, local.path)

	// C2 signed by an attacker key absent from the in-tree .allowed_signers.
	attacker := testSigner(t)
	attackerStore, err := New(remote, attacker, slog.Default())
	if err != nil {
		t.Fatalf("attacker store: %v", err)
	}
	writeStore(t, attackerStore, "x.md", "x\n")

	// C3 signed by thane (trusted) on top — so the tip verifies.
	thaneRemote, err := New(remote, thane, slog.Default())
	if err != nil {
		t.Fatalf("thane remote store: %v", err)
	}
	writeStore(t, thaneRemote, "t.md", "t\n")

	res, err := local.Sync(t.Context(), SyncRequest{RemoteURL: remote, Branch: branch, Mode: SyncModeBidirectional, RequireVerify: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Outcome != SyncBlocked {
		t.Fatalf("Outcome = %q, want blocked (detail %q)", res.Outcome, res.Detail)
	}
	if !strings.Contains(res.Detail, "not trusted") {
		t.Fatalf("Detail = %q, want an untrusted-commit reason", res.Detail)
	}
	if headSHA(t, local.path) != base {
		t.Fatalf("local head moved despite a blocked in-tree laundering attempt")
	}
}

func TestAssertAnchorPlacement(t *testing.T) {
	repo := t.TempDir()

	// Empty anchor → permitted (in-tree .allowed_signers).
	s := &Store{path: repo, allowedSignersPath: "", logger: slog.Default()}
	if err := s.assertAnchorPlacement(); err != nil {
		t.Fatalf("empty (in-tree) anchor rejected: %v", err)
	}

	// A configured anchor inside the tree → refuse.
	s = &Store{path: repo, allowedSignersPath: filepath.Join(repo, "sub", "anchor"), logger: slog.Default()}
	if err := s.assertAnchorPlacement(); err == nil {
		t.Fatal("in-tree configured anchor accepted, want error")
	}

	// A configured anchor outside the tree → ok.
	outside := filepath.Join(t.TempDir(), "anchor")
	s = &Store{path: repo, allowedSignersPath: outside, logger: slog.Default()}
	if err := s.assertAnchorPlacement(); err != nil {
		t.Fatalf("out-of-tree anchor rejected: %v", err)
	}
}

// TestAnchorContainmentSymlinkLeaf exercises the case resolveReal exists for:
// an anchor whose leaf does not exist yet and whose path reaches the repo
// through a symlink. resolveReal must resolve the symlinked ancestor so the
// containment check still catches it — the class of macOS /var → /private/var
// resolution bug the walk-to-existing-ancestor logic guards against.
func TestAnchorContainmentSymlinkLeaf(t *testing.T) {
	base := t.TempDir()
	realRepo := filepath.Join(base, "realrepo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatalf("mkdir realRepo: %v", err)
	}
	linkRepo := filepath.Join(base, "linkrepo")
	if err := os.Symlink(realRepo, linkRepo); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	// Anchor reached through the symlink, with a not-yet-created leaf: the
	// symlinked parent must resolve to realRepo so this is caught as in-tree.
	s := &Store{path: realRepo, allowedSignersPath: filepath.Join(linkRepo, ".allowed_signers"), logger: slog.Default()}
	if err := s.assertAnchorPlacement(); err == nil {
		t.Fatal("anchor reaching the repo through a symlink (non-existent leaf) accepted; want in-tree rejection")
	}

	// A genuinely out-of-tree anchor with a not-yet-created leaf under a
	// not-yet-created parent is still accepted.
	s = &Store{path: realRepo, allowedSignersPath: filepath.Join(base, "elsewhere", "sub", "anchor"), logger: slog.Default()}
	if err := s.assertAnchorPlacement(); err != nil {
		t.Fatalf("out-of-tree anchor with non-existent leaf rejected: %v", err)
	}
}

func TestIsWithin(t *testing.T) {
	sep := string(filepath.Separator)
	tests := []struct {
		parent, child string
		want          bool
	}{
		{sep + "a" + sep + "repo", sep + "a" + sep + "repo" + sep + ".allowed_signers", true},
		{sep + "a" + sep + "repo", sep + "a" + sep + "repo", true},
		{sep + "a" + sep + "repo", sep + "a" + sep + "anchor", false},
		{sep + "a" + sep + "repo", sep + "a" + sep + "repo-2" + sep + "x", false}, // sibling prefix
		{sep + "a" + sep + "repo", sep + "a", false},                              // parent of parent
	}
	for _, tt := range tests {
		if got := isWithin(tt.parent, tt.child); got != tt.want {
			t.Errorf("isWithin(%q, %q) = %v, want %v", tt.parent, tt.child, got, tt.want)
		}
	}
}
