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

func TestLoadAcceptsMatchingDeclaredWorkspace(t *testing.T) {
	workspace := t.TempDir()
	coreDir := filepath.Join(workspace, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(coreDir, "config.yaml")
	body := "listen:\n  port: 8080\nworkspace:\n  path: " + workspace + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with a matching declaration should succeed: %v", err)
	}
	if cfg.Workspace.Path != workspace {
		t.Fatalf("Workspace.Path = %q, want %q", cfg.Workspace.Path, workspace)
	}
}

func TestLoadRejectsConflictingDeclaredWorkspace(t *testing.T) {
	// A config states its workspace by sitting in it. A declaration that
	// disagrees would point roots, state, and identity somewhere other
	// than where the file lives — so the disagreement is surfaced rather
	// than silently resolved either way.
	workspace := t.TempDir()
	elsewhere := t.TempDir()
	coreDir := filepath.Join(workspace, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(coreDir, "config.yaml")
	body := "listen:\n  port: 8080\nworkspace:\n  path: " + elsewhere + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load should reject a workspace.path that contradicts the config location")
	}
	if !strings.Contains(err.Error(), "derived from the config location") {
		t.Fatalf("error should explain the derivation: %v", err)
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
