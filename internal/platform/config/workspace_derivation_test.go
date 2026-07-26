package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCoreConfig(t *testing.T, body string) (workspace, path string) {
	t.Helper()
	workspace = t.TempDir()
	coreDir := filepath.Join(workspace, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path = filepath.Join(coreDir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return workspace, path
}

func TestLoadDerivesWorkspaceFromCoreLocation(t *testing.T) {
	workspace, path := writeCoreConfig(t, "listen:\n  port: 8080\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workspace.Path != workspace {
		t.Fatalf("Workspace.Path = %q, want the config's own workspace %q", cfg.Workspace.Path, workspace)
	}
	if cfg.LoadedFrom() != path {
		t.Fatalf("LoadedFrom() = %q, want %q", cfg.LoadedFrom(), path)
	}
}

func TestConfigPathForWorkspace(t *testing.T) {
	got, err := ConfigPathForWorkspace("/srv/thane")
	if err != nil {
		t.Fatalf("ConfigPathForWorkspace: %v", err)
	}
	if want := filepath.Join("/srv/thane", "core", "config.yaml"); got != want {
		t.Fatalf("ConfigPathForWorkspace() = %q, want %q", got, want)
	}
}

func TestExpandWorkspaceDefaultsAndExpands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got, err := ExpandWorkspace("")
	if err != nil {
		t.Fatalf("ExpandWorkspace: %v", err)
	}
	if want := filepath.Join(home, "Thane"); got != want {
		t.Fatalf("ExpandWorkspace(\"\") = %q, want %q", got, want)
	}
	if got, err = ExpandWorkspace("~/Elsewhere"); err != nil || got != filepath.Join(home, "Elsewhere") {
		t.Fatalf("ExpandWorkspace(\"~/Elsewhere\") = %q, %v", got, err)
	}
}

func TestLoadRejectsDeclaredWorkspacePath(t *testing.T) {
	// Silently ignoring it would be worse than refusing: an operator who
	// sets it believes they pointed the instance somewhere, and it would
	// run from a different directory without saying so.
	_, path := writeCoreConfig(t, "listen:\n  port: 8080\nworkspace:\n  path: /somewhere/else\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("a declared workspace.path must be rejected")
	}
	for _, want := range []string{"derived", "-workspace"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q: %v", want, err)
		}
	}
}

func TestLoadKeepsTheRestOfTheWorkspaceBlock(t *testing.T) {
	workspace, path := writeCoreConfig(t,
		"listen:\n  port: 8080\nworkspace:\n  read_only_dirs:\n    - /srv/reference\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("read_only_dirs is still real config: %v", err)
	}
	if cfg.Workspace.Path != workspace {
		t.Fatalf("Workspace.Path = %q, want the derived %q", cfg.Workspace.Path, workspace)
	}
	if len(cfg.Workspace.ReadOnlyDirs) != 1 || cfg.Workspace.ReadOnlyDirs[0] != "/srv/reference" {
		t.Fatalf("ReadOnlyDirs = %v, want it preserved", cfg.Workspace.ReadOnlyDirs)
	}
}

func TestLoadWithWorkspaceFallsBackOnlyOutsideCore(t *testing.T) {
	// Outside a core there is nothing to derive from, so the flag is the
	// only source left.
	dir := t.TempDir()
	loose := filepath.Join(dir, "rescue.yaml")
	if err := os.WriteFile(loose, []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadWithWorkspace(loose, "/srv/thane")
	if err != nil {
		t.Fatalf("LoadWithWorkspace: %v", err)
	}
	if cfg.Workspace.Path != "/srv/thane" {
		t.Fatalf("Workspace.Path = %q, want the fallback", cfg.Workspace.Path)
	}

	// Inside a core the derived value wins, or the fallback would
	// reintroduce the drift that retiring workspace.path removed.
	workspace, corePath := writeCoreConfig(t, "listen:\n  port: 8080\n")
	cfg, err = LoadWithWorkspace(corePath, "/srv/somewhere-else")
	if err != nil {
		t.Fatalf("LoadWithWorkspace: %v", err)
	}
	if cfg.Workspace.Path != workspace {
		t.Fatalf("Workspace.Path = %q, want the derived %q to win over the fallback", cfg.Workspace.Path, workspace)
	}
}
