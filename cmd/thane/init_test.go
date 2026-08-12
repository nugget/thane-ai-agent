package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// clearUmask sets the process umask to 0 so file permission assertions are
// deterministic. It restores the original umask when the test completes.
func clearUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestRunInit_FreshDirectory(t *testing.T) {
	requireGit(t)
	clearUmask(t)
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := runInit(&buf, dir, initOptions{SelfSigned: true}); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	out := buf.String()

	// Verify directory structure.
	for _, sub := range []string{"core", "db", filepath.Join("core", "talents")} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("expected directory %s: %v", sub, err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}

	// Verify the workspace-root reference copies exist. They carry
	// .example names because the runtime never reads them — the live
	// config is core/config.yaml and the live persona core/persona.md.
	cfgInfo, err := os.Stat(filepath.Join(dir, "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml not created: %v", err)
	}
	if got := cfgInfo.Mode().Perm(); got != 0o644 {
		t.Errorf("config.example.yaml permissions = %o, want 0644", got)
	}

	personaInfo, err := os.Stat(filepath.Join(dir, "persona.example.md"))
	if err != nil {
		t.Fatalf("persona.example.md not created: %v", err)
	}
	if got := personaInfo.Mode().Perm(); got != 0o644 {
		t.Errorf("persona.example.md permissions = %o, want 0644", got)
	}

	// Verify at least one talent file was deployed. They live inside core,
	// so they arrive through its birth commit rather than as loose files.
	entries, err := os.ReadDir(filepath.Join(dir, "core", "talents"))
	if err != nil {
		t.Fatalf("read talents dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("no talent files deployed")
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Errorf("stat talent %s: %v", e.Name(), err)
			continue
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("talent %s permissions = %o, want 0644", e.Name(), got)
		}
	}

	// Verify output contains the created marker for each file.
	if !strings.Contains(out, "✓") {
		t.Error("output missing ✓ marker for created files")
	}
	if !strings.Contains(out, "config.example.yaml") {
		t.Error("output missing config.example.yaml")
	}
	if !strings.Contains(out, "persona.example.md") {
		t.Error("output missing persona.example.md")
	}
	if !strings.Contains(out, "core identity") {
		t.Error("output missing core identity")
	}

	// The closing guidance must point at the files the runtime reads —
	// the workspace-root copies are references, and telling an operator
	// to edit them is telling them to edit files nothing loads.
	if !strings.Contains(out, "core/config.yaml") {
		t.Error("closing guidance should point edits at core/config.yaml")
	}
	if !strings.Contains(out, "core/persona.md") {
		t.Error("closing guidance should point persona authoring at core/persona.md")
	}
	if !strings.Contains(out, "docs/operating/getting-started.md") {
		t.Error("closing guidance should reference docs/operating/getting-started.md")
	}

	for _, rel := range []string{
		"core/config.yaml",
		"core/identity/signing_ed25519",
		"core/identity/signing_ed25519.pub",
		"core/ca/channel_root.key",
		"core/ca/channel_root.crt",
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("%s not created: %v", rel, err)
		}
	}

	for _, rel := range []string{"core/identity/signing_ed25519", "core/ca/channel_root.key"} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s permissions = %o, want 0600", rel, got)
		}
	}

	// Archive skeleton (#937) — every fresh install gets the directory
	// tree, the orientation README, the per-source README, and a
	// placeholder schema file.
	for _, rel := range []string{
		"archive",
		"archive/interactions",
		"archive/sources/thane",
		"archive/meta/schema",
	} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("archive skeleton: %s missing: %v", rel, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("archive skeleton: %s is not a directory", rel)
		}
	}
	for _, rel := range []string{
		"archive/README.md",
		"archive/sources/thane/README.md",
		"archive/meta/schema/interactions.v1.json",
	} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("archive skeleton: %s missing: %v", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("archive skeleton: %s is empty", rel)
		}
	}
}

// TestRunInit_ArchiveBootstrapIdempotent verifies that re-running init
// over an existing archive skeleton leaves every file untouched (same
// mtime, same content) — the writeIfMissing path uses O_EXCL.
func TestRunInit_ArchiveBootstrapIdempotent(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	var buf bytes.Buffer

	if err := runInit(&buf, dir, initOptions{SelfSigned: true}); err != nil {
		t.Fatalf("first runInit: %v", err)
	}

	readmePath := filepath.Join(dir, "archive", "README.md")
	origInfo, err := os.Stat(readmePath)
	if err != nil {
		t.Fatalf("stat archive/README.md: %v", err)
	}
	origContent, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read archive/README.md: %v", err)
	}

	buf.Reset()
	if err := runInit(&buf, dir, initOptions{SelfSigned: true}); err != nil {
		t.Fatalf("second runInit: %v", err)
	}

	newInfo, err := os.Stat(readmePath)
	if err != nil {
		t.Fatalf("stat archive/README.md after second run: %v", err)
	}
	newContent, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read archive/README.md after second run: %v", err)
	}
	if !origInfo.ModTime().Equal(newInfo.ModTime()) {
		t.Errorf("archive/README.md mtime changed (%v → %v) — bootstrap should be idempotent",
			origInfo.ModTime(), newInfo.ModTime())
	}
	if string(origContent) != string(newContent) {
		t.Errorf("archive/README.md content changed across runs")
	}
	if !strings.Contains(buf.String(), "archive/README.md (exists, skipping)") {
		t.Errorf("second run should report skip for existing README, got:\n%s", buf.String())
	}
}

func TestRunInit_SkipsExistingFiles(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	var buf bytes.Buffer

	// First run: create everything.
	if err := runInit(&buf, dir, initOptions{SelfSigned: true}); err != nil {
		t.Fatalf("first runInit failed: %v", err)
	}

	// Record original reference-config content.
	origConfig, err := os.ReadFile(filepath.Join(dir, "config.example.yaml"))
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}

	// Write a sentinel so we can verify the file isn't overwritten.
	sentinel := []byte("# sentinel — do not overwrite\n")
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second run: should skip existing files.
	buf.Reset()
	if err := runInit(&buf, dir, initOptions{SelfSigned: true}); err != nil {
		t.Fatalf("second runInit failed: %v", err)
	}

	out := buf.String()

	// Verify skip marker appears.
	if !strings.Contains(out, "exists, skipping") {
		t.Error("output missing 'exists, skipping' for pre-existing files")
	}

	// Verify config.example.yaml was NOT overwritten.
	got, err := os.ReadFile(filepath.Join(dir, "config.example.yaml"))
	if err != nil {
		t.Fatalf("read config.example.yaml after second run: %v", err)
	}
	if !bytes.Equal(got, sentinel) {
		t.Errorf("config.example.yaml was overwritten: got %d bytes (original was %d)", len(got), len(origConfig))
	}
}

func TestWriteIfMissing(t *testing.T) {
	clearUmask(t)
	tests := []struct {
		name       string
		preExist   bool
		mode       os.FileMode
		wantMarker string
	}{
		{
			name:       "creates new file with 0600",
			preExist:   false,
			mode:       0o600,
			wantMarker: "✓",
		},
		{
			name:       "creates new file with 0644",
			preExist:   false,
			mode:       0o644,
			wantMarker: "✓",
		},
		{
			name:       "skips existing file",
			preExist:   true,
			mode:       0o644,
			wantMarker: "exists, skipping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "testfile")
			data := []byte("hello world")

			if tt.preExist {
				if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
					t.Fatalf("setup pre-existing file: %v", err)
				}
			}

			var buf bytes.Buffer
			if err := writeIfMissing(&buf, path, data, tt.mode); err != nil {
				t.Fatalf("writeIfMissing: %v", err)
			}

			out := buf.String()
			if !strings.Contains(out, tt.wantMarker) {
				t.Errorf("output = %q, want marker %q", out, tt.wantMarker)
			}

			if tt.preExist {
				// Verify content was not overwritten.
				got, _ := os.ReadFile(path)
				if string(got) != "original" {
					t.Errorf("pre-existing file was overwritten: got %q", got)
				}
			} else {
				// Verify content and permissions.
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read written file: %v", err)
				}
				if !bytes.Equal(got, data) {
					t.Errorf("content = %q, want %q", got, data)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat written file: %v", err)
				}
				if perm := info.Mode().Perm(); perm != tt.mode {
					t.Errorf("permissions = %o, want %o", perm, tt.mode)
				}
			}
		})
	}
}

func TestWriteIfMissing_CreateError(t *testing.T) {
	// Try to create a file under a path that is a regular file, not a
	// directory. OpenFile should fail with a non-ErrExist error which
	// writeIfMissing must surface.
	dir := t.TempDir()
	parent := filepath.Join(dir, "blocker")
	if err := os.WriteFile(parent, []byte("i am a file"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	badPath := filepath.Join(parent, "file.txt")

	var buf bytes.Buffer
	err := writeIfMissing(&buf, badPath, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("expected error for create failure, got nil")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Errorf("error = %q, want it to mention 'create'", err)
	}
}

// TestInitFlagErrorsGoToStderr holds init to run()'s contract: terminal output
// belongs on stderr. Usage text mixed into stdout would interleave with the
// progress report a caller may be parsing, and would do it precisely when
// something already went wrong.
func TestInitFlagErrorsGoToStderr(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if err := runInitCommand(&stdout, &stderr, []string{"-no-such-flag"}); err == nil {
		t.Fatal("an unknown flag should fail")
	}
	if stdout.Len() != 0 {
		t.Fatalf("flag failure wrote to stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no-such-flag") {
		t.Fatalf("stderr should carry the flag failure, got:\n%s", stderr.String())
	}
}

// TestInitDeploysTalentsIntoCoresBirthCommit pins where talents live and when
// they become attested.
//
// They arrive as part of the birth commit rather than being written to disk
// first, so a fresh instance never has a moment where its behaviour
// definitions exist unsigned — and when an operator key founds the instance,
// that same key covers them.
func TestInitDeploysTalentsIntoCoresBirthCommit(t *testing.T) {
	t.Parallel()
	requireGit(t)
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := runInit(&buf, dir, initOptions{SelfSigned: true}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "core", "talents"))
	if err != nil {
		t.Fatalf("talents should live inside core: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no talents deployed into core")
	}
	if _, err := os.Stat(filepath.Join(dir, "talents")); !os.IsNotExist(err) {
		t.Fatalf("talents must not also be deployed beside core: %v", err)
	}

	// The whole point: core is clean afterwards, so the instance can start.
	out, err := exec.Command("git", "-C", filepath.Join(dir, "core"), "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("core should be clean after init, got:\n%s", out)
	}
}
