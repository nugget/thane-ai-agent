package forge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFollowReallyClonesWorkingTree exercises the unstubbed path
// against a local git remote. Every other follow test stubs the
// syncer, which would keep passing if the real wiring were wrong — and
// "the tool said ok but no directory appeared" is precisely the bug
// being fixed, so at least one test has to look at the disk.
func TestFollowReallyClonesWorkingTree(t *testing.T) {
	t.Parallel()

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// A real upstream with one commit on main.
	upstream := t.TempDir()
	git(upstream, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(upstream, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	git(upstream, "add", "README.md")
	git(upstream, "commit", "-qm", "initial")

	provider := &mockProvider{
		name: "test",
		getRepositoryResult: &Repository{
			FullName:      "owner/repo",
			DefaultBranch: "main",
			URL:           "https://example.invalid/owner/repo",
			CloneURL:      upstream, // a local path is a valid git remote
		},
	}
	tools := newTestTools(provider, "owner")
	tools.subscriptions = newTestSubscriptionStore(t)
	enablePollingForTest(tools.service)

	dest := filepath.Join(t.TempDir(), "checkout")
	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"branch":         "main",
		"local_checkout": dest,
		"wake_loop":      map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}

	// The working tree must exist with real content, not just a path.
	content, err := os.ReadFile(filepath.Join(dest, "README.md"))
	if err != nil {
		t.Fatalf("checkout has no working tree: %v", err)
	}
	if string(content) != "hello\n" {
		t.Errorf("README.md = %q, want the upstream content", content)
	}

	var resp struct {
		SubscriptionID string `json:"subscription_id"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := tools.subscriptions.Get(resp.SubscriptionID)
	if err != nil {
		t.Fatalf("subscription not persisted: %v", err)
	}
	if stored.LastSyncedSHA == "" {
		t.Error("LastSyncedSHA empty; the first poll would re-report the whole history as new")
	}
}
