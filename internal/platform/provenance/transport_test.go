package provenance

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a fresh non-bare git repo on branch "main" with one commit
// and returns its path. Commits are unsigned — the transport layer never
// verifies signatures, so these tests exercise fetch/ahead-behind mechanics
// without the signing machinery.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "-c", "init.defaultBranch=main", "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	commit(t, dir, "seed")
	return dir
}

// cloneRepo makes a non-bare clone of src on branch "main" and returns its
// path. The clone shares src's base history, so later commits on each side
// model a real fork.
func cloneRepo(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, "clone")
	out, err := exec.Command("git", "clone", "--quiet", "--branch", "main", src, dst).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	runGit(t, dst, "config", "user.name", "Test")
	runGit(t, dst, "config", "user.email", "test@example.com")
	runGit(t, dst, "config", "commit.gpgsign", "false")
	return dst
}

// commit writes a unique file named after msg and commits it.
func commit(t *testing.T, dir, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, msg+".txt"), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", msg, err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-m", msg)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
	}
	return stdout.String()
}

func TestFetchAndAheadBehind(t *testing.T) {
	// remote starts one commit ahead of local's shared base; local then
	// forks its own commit, so after a fetch the two have diverged by one
	// commit each. Every ahead/behind combination is covered from this one
	// topology by inspecting before and after the local fork.
	base := initRepo(t)
	local := cloneRepo(t, base)
	remote := cloneRepo(t, base)

	s := &Store{path: local, logger: slog.Default()}
	ctx := t.Context()

	ab := func(stage string, wantAhead, wantBehind int) {
		t.Helper()
		ahead, behind, err := s.AheadBehind(ctx, "main")
		if err != nil {
			t.Fatalf("AheadBehind (%s): %v", stage, err)
		}
		if ahead != wantAhead || behind != wantBehind {
			t.Fatalf("%s: ahead=%d behind=%d, want %d/%d", stage, ahead, behind, wantAhead, wantBehind)
		}
	}

	// In sync: local and remote both sit at the shared base commit.
	if err := s.Fetch(ctx, FetchOptions{RemoteURL: remote, Branch: "main"}); err != nil {
		t.Fatalf("Fetch (in sync): %v", err)
	}
	ab("in sync", 0, 0)

	// Behind: remote advances by two commits, local stays put.
	commit(t, remote, "r1")
	commit(t, remote, "r2")
	if err := s.Fetch(ctx, FetchOptions{RemoteURL: remote, Branch: "main"}); err != nil {
		t.Fatalf("Fetch (behind): %v", err)
	}
	ab("behind", 0, 2)

	// Diverged: local adds its own commit on top of the shared base. It is
	// now one ahead (its commit) and two behind (the remote's two).
	commit(t, local, "l1")
	ab("diverged", 1, 2)
}

func TestFetchRejectsBadArgs(t *testing.T) {
	s := &Store{path: t.TempDir(), logger: slog.Default()}
	ctx := context.Background()

	tests := []struct {
		name string
		opts FetchOptions
	}{
		{"empty url", FetchOptions{RemoteURL: "", Branch: "main"}},
		{"option-like url", FetchOptions{RemoteURL: "--upload-pack=evil", Branch: "main"}},
		{"empty branch", FetchOptions{RemoteURL: "https://example.com/r.git", Branch: ""}},
		{"option-like branch", FetchOptions{RemoteURL: "https://example.com/r.git", Branch: "-x"}},
		{"revspec branch", FetchOptions{RemoteURL: "https://example.com/r.git", Branch: "main..other"}},
		{"tilde branch", FetchOptions{RemoteURL: "https://example.com/r.git", Branch: "HEAD~1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Fetch(ctx, tt.opts); err == nil {
				t.Fatalf("Fetch(%+v) = nil, want error", tt.opts)
			}
		})
	}
}

func TestAheadBehindRejectsBadBranch(t *testing.T) {
	s := &Store{path: t.TempDir(), logger: slog.Default()}
	for _, branch := range []string{"-x", "main..other", "HEAD~1", "a b"} {
		if _, _, err := s.AheadBehind(context.Background(), branch); err == nil {
			t.Fatalf("AheadBehind(%q) = nil, want error", branch)
		}
	}
}

func TestBuildSSHCommand(t *testing.T) {
	const base = "ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o IdentityAgent=none"
	tests := []struct {
		name       string
		key        string
		knownHosts string
		want       string
	}{
		{
			name:       "key and known_hosts",
			key:        "/keys/transport",
			knownHosts: "/ssh/known_hosts",
			want:       base + " -i '/keys/transport' -o IdentitiesOnly=yes -o UserKnownHostsFile='/ssh/known_hosts'",
		},
		{
			name:       "no key",
			knownHosts: "/ssh/known_hosts",
			want:       base + " -o UserKnownHostsFile='/ssh/known_hosts'",
		},
		{
			name: "no known_hosts",
			key:  "/keys/transport",
			want: base + " -i '/keys/transport' -o IdentitiesOnly=yes",
		},
		{
			name:       "path with spaces",
			key:        "/my keys/id ed25519",
			knownHosts: "/a b/known",
			want:       base + " -i '/my keys/id ed25519' -o IdentitiesOnly=yes -o UserKnownHostsFile='/a b/known'",
		},
		{
			name: "path with single quote",
			key:  "/keys/o'brien",
			want: base + ` -i '/keys/o'\''brien' -o IdentitiesOnly=yes`,
		},
		{
			name: "neither",
			want: base,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildSSHCommand(tt.key, tt.knownHosts); got != tt.want {
				t.Fatalf("BuildSSHCommand(%q, %q)\n got: %s\nwant: %s", tt.key, tt.knownHosts, got, tt.want)
			}
		})
	}
}
