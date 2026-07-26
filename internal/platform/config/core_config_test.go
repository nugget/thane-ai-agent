package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSearchPathsPrefersCore(t *testing.T) {
	paths := DefaultSearchPaths()
	if len(paths) < 2 {
		t.Fatalf("search paths = %v, want at least two entries", paths)
	}
	if paths[0] != filepath.Join("core", "config.yaml") {
		t.Fatalf("first search path = %q, want core/config.yaml", paths[0])
	}
	// The legacy working-directory config must still be reachable, and
	// must come after every core-relative candidate.
	legacy := -1
	lastCore := -1
	for i, p := range paths {
		if p == "config.yaml" {
			legacy = i
		}
		if strings.Contains(p, filepath.Join("core", "config.yaml")) {
			lastCore = i
		}
	}
	if legacy < 0 {
		t.Fatalf("legacy ./config.yaml missing from search paths: %v", paths)
	}
	if lastCore > legacy {
		t.Fatalf("core-relative path at %d comes after legacy at %d: %v", lastCore, legacy, paths)
	}
}

func TestFindConfigPrefersCoreOverLegacy(t *testing.T) {
	dir := t.TempDir()
	coreDir := filepath.Join(dir, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacy := filepath.Join(dir, "config.yaml")
	core := filepath.Join(coreDir, "config.yaml")
	for _, p := range []string{legacy, core} {
		if err := os.WriteFile(p, []byte("listen:\n  port: 8080\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	original := searchPathsFunc
	searchPathsFunc = func() []string { return []string{core, legacy} }
	t.Cleanup(func() { searchPathsFunc = original })

	found, err := FindConfig("")
	if err != nil {
		t.Fatalf("FindConfig: %v", err)
	}
	if found != core {
		t.Fatalf("FindConfig() = %q, want the core config %q", found, core)
	}
}

func TestCoreConfigPath(t *testing.T) {
	cfg := &Config{}
	if got := cfg.CoreConfigPath(); got != "" {
		t.Fatalf("CoreConfigPath() = %q with no workspace, want empty", got)
	}
	cfg.Workspace.Path = "/srv/thane"
	if got, want := cfg.CoreConfigPath(), filepath.Join("/srv/thane", "core", "config.yaml"); got != want {
		t.Fatalf("CoreConfigPath() = %q, want %q", got, want)
	}
}

func TestWarnConfigOutsideCoreDetectsCompetingConfig(t *testing.T) {
	// The split-brain shape: a config in core exists but is not the one
	// in use, so edits to it silently do nothing.
	dir := t.TempDir()
	coreDir := filepath.Join(dir, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	corePath := filepath.Join(coreDir, "config.yaml")
	if err := os.WriteFile(corePath, []byte("listen:\n  port: 8080\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := &Config{loadedFrom: filepath.Join(dir, "config.yaml")}
	cfg.Workspace.Path = dir

	// The warning path must not panic or mutate config; it is advisory
	// until the authority phase turns it into a refusal.
	cfg.warnConfigOutsideCore()

	if cfg.LoadedFrom() != filepath.Join(dir, "config.yaml") {
		t.Fatalf("LoadedFrom() = %q, want the legacy path", cfg.LoadedFrom())
	}
}

func TestLoadRecordsSourcePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("listen:\n  port: 8080\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LoadedFrom() != path {
		t.Fatalf("LoadedFrom() = %q, want %q", cfg.LoadedFrom(), path)
	}
}
