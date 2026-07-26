package coreintegrity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitCommitAll(t *testing.T, dir, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", message}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func newCore(t *testing.T) (workspace, core string) {
	t.Helper()
	workspace = t.TempDir()
	core = filepath.Join(workspace, "core")
	if err := os.MkdirAll(core, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return workspace, core
}

func run(t *testing.T, workspace string) Report {
	t.Helper()
	report, err := Run(context.Background(), workspace, Options{ConfigFileName: "config.yaml"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

func checkByName(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not present in report: %+v", name, report.Checks)
	return Check{}
}

func TestHealthyCorePassesEveryCheck(t *testing.T) {
	workspace, core := newCore(t)
	gitInit(t, core)
	if err := os.WriteFile(filepath.Join(core, ".gitignore"), []byte("*.key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(core, "config.yaml"), []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitCommitAll(t, core, "core baseline")

	report := run(t, workspace)
	if !report.OK() {
		t.Fatalf("healthy core should pass every check, got failures: %+v", report.Failures())
	}
}

func TestMissingCoreReportsInitAndSkipsDownstream(t *testing.T) {
	workspace := t.TempDir()

	report := run(t, workspace)
	dir := checkByName(t, report, "core_directory")
	if dir.Status != StatusFail || !strings.Contains(dir.Fix, "thane init") {
		t.Fatalf("core_directory = %+v, want a failure suggesting thane init", dir)
	}
	// One broken prerequisite must not surface as several independent
	// failures; the downstream checks report as unverified instead.
	for _, name := range []string{"core_repository", "key_material_excluded", "config_committed", "core_clean"} {
		if got := checkByName(t, report, name); got.Status == StatusFail {
			t.Fatalf("%s reported a failure that is really a consequence of the missing core: %+v", name, got)
		}
	}
}

// TestStagedButNeverCommittedCore reproduces the production state that
// motivated this report: a repository that looks version-controlled from
// the filesystem, with files staged and no commit behind them.
func TestStagedButNeverCommittedCore(t *testing.T) {
	workspace, core := newCore(t)
	gitInit(t, core)
	if err := os.WriteFile(filepath.Join(core, "config.yaml"), []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd := exec.Command("git", "-C", core, "add", "config.yaml")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	report := run(t, workspace)
	if got := checkByName(t, report, "core_repository"); got.Status != StatusPass {
		t.Fatalf("a repo with no commits is still a repo: %+v", got)
	}
	history := checkByName(t, report, "core_history")
	if history.Status != StatusFail || !strings.Contains(history.Fix, "commit -S") {
		t.Fatalf("core_history = %+v, want a failure suggesting a signed baseline commit", history)
	}
	if got := checkByName(t, report, "config_committed"); got.Status != StatusSkipped {
		t.Fatalf("config_committed = %+v, want skipped when there is no history to check against", got)
	}
}

// TestTrackedPrivateKeyIsAFailure covers the other half of the
// production state: key material staged in a root that is about to gain
// permanent history.
func TestTrackedPrivateKeyIsAFailure(t *testing.T) {
	workspace, core := newCore(t)
	gitInit(t, core)
	caDir := filepath.Join(core, "ca")
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "channel_root.key"), []byte("PRIVATE\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(core, "config.yaml"), []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitCommitAll(t, core, "core baseline")

	got := checkByName(t, run(t, workspace), "key_material_excluded")
	if got.Status != StatusFail {
		t.Fatalf("a tracked private key must fail: %+v", got)
	}
	if !strings.Contains(got.Detail, "channel_root.key") {
		t.Fatalf("detail should name the offending file: %q", got.Detail)
	}
	if !strings.Contains(got.Fix, "rm --cached") || !strings.Contains(got.Fix, ".gitignore") {
		t.Fatalf("fix should untrack the key and exclude it going forward: %q", got.Fix)
	}
}

func TestMissingGitignoreIsAFailureEvenWithNoKeyTracked(t *testing.T) {
	workspace, core := newCore(t)
	gitInit(t, core)
	if err := os.WriteFile(filepath.Join(core, "config.yaml"), []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitCommitAll(t, core, "core baseline")

	got := checkByName(t, run(t, workspace), "key_material_excluded")
	if got.Status != StatusFail || !strings.Contains(got.Fix, ".gitignore") {
		t.Fatalf("key_material_excluded = %+v, want a failure asking for a .gitignore", got)
	}
}

func TestUncommittedChangesFailCoreClean(t *testing.T) {
	workspace, core := newCore(t)
	gitInit(t, core)
	if err := os.WriteFile(filepath.Join(core, ".gitignore"), []byte("*.key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configPath := filepath.Join(core, "config.yaml")
	if err := os.WriteFile(configPath, []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitCommitAll(t, core, "core baseline")
	if err := os.WriteFile(configPath, []byte("listen:\n  port: 9090\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := checkByName(t, run(t, workspace), "core_clean")
	if got.Status != StatusFail {
		t.Fatalf("an edited tracked file must fail core_clean: %+v", got)
	}
	if !strings.Contains(got.Detail, "differs from what is signed") {
		t.Fatalf("detail should explain the consequence: %q", got.Detail)
	}
}

func TestUncommittedConfigIsNotCommitted(t *testing.T) {
	workspace, core := newCore(t)
	gitInit(t, core)
	if err := os.WriteFile(filepath.Join(core, ".gitignore"), []byte("*.key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitCommitAll(t, core, "core baseline")
	if err := os.WriteFile(filepath.Join(core, "config.yaml"), []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := checkByName(t, run(t, workspace), "config_committed")
	if got.Status != StatusFail || !strings.Contains(got.Fix, "commit -S") {
		t.Fatalf("config_committed = %+v, want a failure suggesting a signed commit", got)
	}
}

func TestReportOKTreatsSkippedAsNotPassing(t *testing.T) {
	report := Report{Checks: []Check{
		{Name: "a", Status: StatusPass},
		{Name: "b", Status: StatusSkipped},
	}}
	if report.OK() {
		t.Fatal("a skipped check means the requirement went unverified, so the report is not OK")
	}
	if len(report.Failures()) != 1 {
		t.Fatalf("Failures() = %+v, want the skipped check", report.Failures())
	}
}
