package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestRepositoryGitToolsReadHistoryWithinBoundRoot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repos", "thanecode")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	gitForTest(t, repo, "init", "-b", "main")
	writeRepositoryTestFile(t, repo, "main.go", "package main\n\nfunc value() int { return 1 }\n")
	gitForTest(t, repo, "add", "main.go")
	gitForTest(t, repo, "commit", "-m", "initial")
	first := strings.TrimSpace(gitForTest(t, repo, "rev-parse", "HEAD"))
	writeRepositoryTestFile(t, repo, "main.go", "package main\n\nfunc value() int { return 2 }\n")
	writeRepositoryTestFile(t, repo, "large.txt", strings.Repeat("large bounded diff line\n", 2000))
	gitForTest(t, repo, "add", "main.go", "large.txt")
	gitForTest(t, repo, "commit", "-m", "update value")
	second := strings.TrimSpace(gitForTest(t, repo, "rev-parse", "HEAD"))

	resolver := paths.New(map[string]string{"core": filepath.Join(workspace, "core")})
	if err := resolver.Register(paths.Root{
		Name: "thanecode", Path: repo, Kind: paths.RootKindRepository, ReadOnly: true, Owner: "sub-a",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ft := NewFileTools(workspace, nil)
	ft.SetResolver(resolver)
	ctx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingRepositoryRoot: "thanecode",
	})

	logRaw, err := ft.RepositoryGitLog(ctx, "", first, second, "main.go", 10)
	if err != nil {
		t.Fatalf("RepositoryGitLog: %v", err)
	}
	var logResult repositoryGitLogResult
	if err := json.Unmarshal([]byte(logRaw), &logResult); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if logResult.Root != "thanecode" || logResult.From != first || logResult.To != second || len(logResult.Commits) != 1 {
		t.Fatalf("log result = %+v", logResult)
	}
	if logResult.Commits[0].Subject != "update value" || logResult.Commits[0].Age == "" {
		t.Fatalf("log commit = %+v", logResult.Commits[0])
	}
	if strings.Contains(logRaw, "authored_at") {
		t.Fatalf("log result exposes an absolute authored timestamp: %s", logRaw)
	}

	diffRaw, err := ft.RepositoryGitDiff(ctx, "", first, second, "main.go", "patch")
	if err != nil {
		t.Fatalf("RepositoryGitDiff: %v", err)
	}
	var diffResult repositoryGitBodyResult
	if err := json.Unmarshal([]byte(diffRaw), &diffResult); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
	if diffResult.Format != "patch" || !strings.Contains(diffResult.Body, "return 2") {
		t.Fatalf("diff result = %+v", diffResult)
	}
	largeDiffRaw, err := ft.RepositoryGitDiff(ctx, "", first, second, "large.txt", "patch")
	if err != nil {
		t.Fatalf("RepositoryGitDiff large fallback: %v", err)
	}
	var largeDiffResult repositoryGitBodyResult
	if err := json.Unmarshal([]byte(largeDiffRaw), &largeDiffResult); err != nil {
		t.Fatalf("decode large diff: %v", err)
	}
	if largeDiffResult.Format != "stat" || largeDiffResult.Note == "" || !strings.Contains(largeDiffResult.Body, "large.txt") {
		t.Fatalf("large diff did not fall back to bounded stat output: %+v", largeDiffResult)
	}

	showRaw, err := ft.RepositoryGitShow(ctx, "", second, "main.go")
	if err != nil {
		t.Fatalf("RepositoryGitShow: %v", err)
	}
	var showResult repositoryGitBodyResult
	if err := json.Unmarshal([]byte(showRaw), &showResult); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if showResult.Revision != second || !strings.Contains(showResult.Body, "update value") {
		t.Fatalf("show result = %+v", showResult)
	}
	if !strings.Contains(showResult.Body, "Authored: -") || strings.Contains(showResult.Body, "AuthorDate:") || strings.Contains(showResult.Body, "CommitDate:") {
		t.Fatalf("show result does not use delta-only commit time: %+v", showResult)
	}

	blameRaw, err := ft.RepositoryGitBlame(ctx, "", second, "main.go", 3, 3)
	if err != nil {
		t.Fatalf("RepositoryGitBlame: %v", err)
	}
	var blameResult repositoryGitBlameResult
	if err := json.Unmarshal([]byte(blameRaw), &blameResult); err != nil {
		t.Fatalf("decode blame: %v", err)
	}
	if len(blameResult.Lines) != 1 || blameResult.Lines[0].Line != 3 || !strings.Contains(blameResult.Lines[0].Content, "return 2") {
		t.Fatalf("blame result = %+v", blameResult)
	}
}

func TestRepositoryGitToolsEnforceRootAndSelectorBoundaries(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	resolver := paths.New(map[string]string{"core": workspace})
	for _, root := range []paths.Root{
		{Name: "thanecode", Path: filepath.Join(workspace, "thanecode"), Kind: paths.RootKindRepository, ReadOnly: true, Owner: "sub-a"},
		{Name: "othercode", Path: filepath.Join(workspace, "othercode"), Kind: paths.RootKindRepository, ReadOnly: true, Owner: "sub-b"},
	} {
		if err := os.MkdirAll(root.Path, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		gitForTest(t, root.Path, "init", "-b", "main")
		writeRepositoryTestFile(t, root.Path, "README.md", root.Name+"\n")
		gitForTest(t, root.Path, "add", "README.md")
		gitForTest(t, root.Path, "commit", "-m", "initial")
		if err := resolver.Register(root); err != nil {
			t.Fatalf("Register(%s): %v", root.Name, err)
		}
	}
	if err := resolver.Register(paths.Root{Name: "missing", Path: filepath.Join(workspace, "missing"), Kind: paths.RootKindRepository, ReadOnly: true, Owner: "sub-missing"}); err != nil {
		t.Fatalf("Register(missing): %v", err)
	}
	ft := NewFileTools(workspace, nil)
	ft.SetResolver(resolver)
	ctx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingRepositoryRoot: "thanecode",
	})

	if _, err := ft.RepositoryGitLog(ctx, "othercode", "", "HEAD", "", 1); err == nil || !strings.Contains(err.Error(), "bound to root") {
		t.Fatalf("different root error = %v", err)
	}
	if _, err := ft.RepositoryGitLog(context.Background(), "", "", "HEAD", "", 1); err == nil || !strings.Contains(err.Error(), "root is required") {
		t.Fatalf("unbound root error = %v", err)
	}
	if _, err := ft.RepositoryGitShow(ctx, "", "--help", ""); err == nil || !strings.Contains(err.Error(), "does not name a commit") {
		t.Fatalf("option-like revision error = %v", err)
	}
	if _, err := ft.RepositoryGitBlame(ctx, "", "HEAD", "../secret", 0, 0); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("path escape error = %v", err)
	}
	if _, err := ft.RepositoryGitLog(context.Background(), "missing", "", "HEAD", "", 1); err == nil || strings.Contains(err.Error(), workspace) {
		t.Fatalf("missing repository error leaked checkout path: %v", err)
	}
}

func gitForTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{
		"-C", repo,
		"-c", "user.name=Repository Test",
		"-c", "user.email=repository-test@example.invalid",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", commandArgs...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeRepositoryTestFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
}
