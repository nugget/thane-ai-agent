package checkout

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMirrorSyncInitializesWithoutPersistingRemote(t *testing.T) {
	source := newMirrorSourceRepo(t)
	commitMirrorFile(t, source, "README.md", "one\n", "initial")
	remoteHead := mirrorHead(t, source)
	worktree := filepath.Join(t.TempDir(), "mirror")

	mirror, err := OpenMirror(MirrorSpec{Name: "thane", WorktreePath: worktree, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("OpenMirror: %v", err)
	}
	res, err := mirror.Sync(t.Context(), MirrorSyncRequest{RemoteURL: source, Branch: "main"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if res.PreviousHead != "" {
		t.Fatalf("PreviousHead = %q, want empty for first sync", res.PreviousHead)
	}
	if res.RemoteHead != remoteHead || !res.Changed {
		t.Fatalf("result = %+v, want changed sync to %s", res, remoteHead)
	}
	if got := mirrorHead(t, worktree); got != remoteHead {
		t.Fatalf("mirror HEAD = %s, want %s", got, remoteHead)
	}
	if got := readMirrorFile(t, worktree, "README.md"); got != "one\n" {
		t.Fatalf("README.md = %q, want source content", got)
	}
	if remoteURL := mirrorGitOptional(t, worktree, "config", "--get", "remote.origin.url"); remoteURL != "" {
		t.Fatalf("remote.origin.url = %q, want no persisted remote", remoteURL)
	}
}

func TestMirrorSyncResetsAndCleansLocalWorktree(t *testing.T) {
	source := newMirrorSourceRepo(t)
	commitMirrorFile(t, source, "README.md", "one\n", "initial")
	worktree := filepath.Join(t.TempDir(), "mirror")
	mirror, err := OpenMirror(MirrorSpec{Name: "thane", WorktreePath: worktree})
	if err != nil {
		t.Fatalf("OpenMirror: %v", err)
	}
	if _, err := mirror.Sync(t.Context(), MirrorSyncRequest{RemoteURL: source, Branch: "main"}); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty README.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatalf("write scratch.txt: %v", err)
	}
	commitMirrorFile(t, source, "README.md", "two\n", "advance")
	remoteHead := mirrorHead(t, source)

	res, err := mirror.Sync(t.Context(), MirrorSyncRequest{RemoteURL: source, Branch: "main"})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.RemoteHead != remoteHead || !res.Changed {
		t.Fatalf("result = %+v, want changed sync to %s", res, remoteHead)
	}
	if got := readMirrorFile(t, worktree, "README.md"); got != "two\n" {
		t.Fatalf("README.md = %q, want reset source content", got)
	}
	if _, err := os.Stat(filepath.Join(worktree, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("scratch.txt still exists after mirror clean; stat err=%v", err)
	}
}

func TestMirrorSyncAcceptsRemoteRewrite(t *testing.T) {
	source := newMirrorSourceRepo(t)
	commitMirrorFile(t, source, "README.md", "one\n", "initial")
	baseHead := mirrorHead(t, source)
	commitMirrorFile(t, source, "README.md", "two\n", "advance")
	advancedHead := mirrorHead(t, source)

	worktree := filepath.Join(t.TempDir(), "mirror")
	mirror, err := OpenMirror(MirrorSpec{Name: "thane", WorktreePath: worktree})
	if err != nil {
		t.Fatalf("OpenMirror: %v", err)
	}
	if _, err := mirror.Sync(t.Context(), MirrorSyncRequest{RemoteURL: source, Branch: "main"}); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}
	if got := mirrorHead(t, worktree); got != advancedHead {
		t.Fatalf("initial mirror HEAD = %s, want %s", got, advancedHead)
	}

	runMirrorGit(t, source, "reset", "--hard", baseHead)
	res, err := mirror.Sync(t.Context(), MirrorSyncRequest{RemoteURL: source, Branch: "main"})
	if err != nil {
		t.Fatalf("Sync after rewrite: %v", err)
	}
	if res.PreviousHead != advancedHead || res.RemoteHead != baseHead || !res.Changed {
		t.Fatalf("rewrite result = %+v, want previous=%s remote=%s changed", res, advancedHead, baseHead)
	}
	if got := mirrorHead(t, worktree); got != baseHead {
		t.Fatalf("mirror HEAD after rewrite = %s, want %s", got, baseHead)
	}
	if got := readMirrorFile(t, worktree, "README.md"); got != "one\n" {
		t.Fatalf("README.md after rewrite = %q, want base content", got)
	}
}

func TestMirrorSyncRejectsNonEmptyNonRepo(t *testing.T) {
	source := newMirrorSourceRepo(t)
	commitMirrorFile(t, source, "README.md", "one\n", "initial")
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "local.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	mirror, err := OpenMirror(MirrorSpec{Name: "thane", WorktreePath: worktree})
	if err != nil {
		t.Fatalf("OpenMirror: %v", err)
	}
	_, err = mirror.Sync(t.Context(), MirrorSyncRequest{RemoteURL: source, Branch: "main"})
	if err == nil {
		t.Fatal("Sync returned nil, want non-empty non-repo error")
	}
	if !strings.Contains(err.Error(), "not an empty directory or git checkout") {
		t.Fatalf("error = %v, want non-empty directory message", err)
	}
}

func newMirrorSourceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runMirrorGit(t, dir, "init")
	runMirrorGit(t, dir, "checkout", "-b", "main")
	runMirrorGit(t, dir, "config", "user.name", "Test")
	runMirrorGit(t, dir, "config", "user.email", "test@example.com")
	runMirrorGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func commitMirrorFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runMirrorGit(t, dir, "add", "-A")
	runMirrorGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-m", msg)
}

func mirrorHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runMirrorGit(t, dir, "rev-parse", "HEAD"))
}

func readMirrorFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func mirrorGitOptional(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

func runMirrorGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git -C %s %s: %v\nstdout: %s\nstderr: %s", dir, strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}
