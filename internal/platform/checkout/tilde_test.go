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
		// The defect is a literal "~" path COMPONENT, not a tilde
		// character anywhere in the string: a home directory may
		// legitimately contain one, and flagging that would fail this
		// test for the wrong reason on a machine where it is true.
		if hasTildeComponent(got) {
			t.Errorf("%s = %q contains an unexpanded ~ path component", name, got)
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

// hasTildeComponent reports whether any element of path is exactly "~",
// which is what an unexpanded tilde leaves behind.
func hasTildeComponent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "~" {
			return true
		}
	}
	return false
}

// TestAbsPathRefusesUnexpandedTilde covers the fallback that would
// otherwise recreate the bug quietly. ExpandHome returns its input
// unchanged when the home directory cannot be determined, and
// filepath.Abs would then produce "<cwd>/~/..." — a plausible-looking
// wrong answer on the one path where nothing is watching.
func TestAbsPathRefusesUnexpandedTilde(t *testing.T) {
	// os.UserHomeDir reads $HOME on Unix; emptying it makes expansion
	// fail the way it would on a process with no home.
	t.Setenv("HOME", "")

	if _, err := absPath("~/repo"); err == nil {
		t.Error("absPath() accepted a tilde it could not expand; filepath.Abs would have mangled it into <cwd>/~/repo")
	}

	// A path needing no expansion still resolves with no home set.
	got, err := absPath("/tmp/repo")
	if err != nil {
		t.Fatalf("absPath(absolute) = %v, want it to resolve without a home directory", err)
	}
	if got != "/tmp/repo" {
		t.Errorf("absPath() = %q, want %q", got, "/tmp/repo")
	}
}
