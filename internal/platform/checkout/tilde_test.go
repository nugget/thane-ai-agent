package checkout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveRootExpandsTilde covers the papercut that produced a real
// nested-nonsense checkout path in production: forge_repo_follow was
// given "~/Thane/repos/x" and recorded
// "/Users/aimee/Thane/~/Thane/repos/x".
//
// filepath.Abs does not expand ~; it prepends the working directory,
// so the tilde survives as a literal path component and the directory
// is created without complaint. Nothing errors — the checkout just
// lands somewhere nobody named. Paths reaching this surface come from
// model tool arguments and operator config, both of which write ~ by
// habit.
func TestResolveRootExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	root, err := ResolveRoot("~/repo", "~/repo")
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}

	for name, got := range map[string]string{"RepoPath": root.RepoPath, "WorktreePath": root.WorktreePath} {
		if strings.Contains(got, "~") {
			t.Errorf("%s = %q still contains a literal tilde", name, got)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("%s = %q is not absolute", name, got)
		}
		if want := filepath.Join(home, "repo"); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestResolveRootLeavesOtherPathsAlone keeps the expansion from
// touching paths that never asked for it.
func TestResolveRootLeavesOtherPathsAlone(t *testing.T) {
	dir := t.TempDir()
	root, err := ResolveRoot(dir, dir)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	// t.TempDir can hand back a symlinked path; compare after
	// evaluating both sides rather than assuming equality.
	wantEval, _ := filepath.EvalSymlinks(dir)
	gotEval, _ := filepath.EvalSymlinks(root.RepoPath)
	if gotEval != wantEval {
		t.Errorf("RepoPath = %q, want %q", root.RepoPath, dir)
	}
}
