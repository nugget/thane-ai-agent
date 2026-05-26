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

	if err := runInit(&buf, dir); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	out := buf.String()

	// Verify directory structure.
	for _, sub := range []string{"core", "db", "talents"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("expected directory %s: %v", sub, err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}

	// Verify config.yaml exists with restricted permissions.
	cfgInfo, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml not created: %v", err)
	}
	if got := cfgInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("config.yaml permissions = %o, want 0600", got)
	}

	// Verify persona.md exists with standard permissions.
	personaInfo, err := os.Stat(filepath.Join(dir, "persona.md"))
	if err != nil {
		t.Fatalf("persona.md not created: %v", err)
	}
	if got := personaInfo.Mode().Perm(); got != 0o644 {
		t.Errorf("persona.md permissions = %o, want 0644", got)
	}

	// Verify at least one talent file was deployed.
	entries, err := os.ReadDir(filepath.Join(dir, "talents"))
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
	if !strings.Contains(out, "config.yaml") {
		t.Error("output missing config.yaml")
	}
	if !strings.Contains(out, "persona.md") {
		t.Error("output missing persona.md")
	}
	if !strings.Contains(out, "core identity") {
		t.Error("output missing core identity")
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

	if err := runInit(&buf, dir); err != nil {
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
	if err := runInit(&buf, dir); err != nil {
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
	if err := runInit(&buf, dir); err != nil {
		t.Fatalf("first runInit failed: %v", err)
	}

	// Record original config content.
	origConfig, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}

	// Write a sentinel into config.yaml so we can verify it isn't overwritten.
	sentinel := []byte("# sentinel — do not overwrite\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), sentinel, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Second run: should skip existing files.
	buf.Reset()
	if err := runInit(&buf, dir); err != nil {
		t.Fatalf("second runInit failed: %v", err)
	}

	out := buf.String()

	// Verify skip marker appears.
	if !strings.Contains(out, "exists, skipping") {
		t.Error("output missing 'exists, skipping' for pre-existing files")
	}

	// Verify config.yaml was NOT overwritten.
	got, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml after second run: %v", err)
	}
	if !bytes.Equal(got, sentinel) {
		t.Errorf("config.yaml was overwritten: got %d bytes (original was %d)", len(got), len(origConfig))
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
