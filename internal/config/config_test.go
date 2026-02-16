package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindConfig_Explicit(t *testing.T) {
	// Create a temp config file
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	os.WriteFile(path, []byte("listen:\n  port: 9999\n"), 0600)

	got, err := FindConfig(path)
	if err != nil {
		t.Fatalf("FindConfig(%q) error: %v", path, err)
	}
	if got != path {
		t.Errorf("FindConfig(%q) = %q, want %q", path, got, path)
	}
}

func TestFindConfig_ExplicitMissing(t *testing.T) {
	_, err := FindConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("FindConfig with missing explicit path should error")
	}
}

func TestFindConfig_SearchPath(t *testing.T) {
	// When no config exists anywhere, should error.
	// Override searchPathsFunc to avoid finding real config files
	// on developer/deploy machines (~/Thane/config.yaml,
	// /usr/local/etc/thane/config.yaml, etc.).
	dir := t.TempDir()
	orig := searchPathsFunc
	searchPathsFunc = func() []string {
		return []string{filepath.Join(dir, "config.yaml")}
	}
	defer func() { searchPathsFunc = orig }()

	_, err := FindConfig("")
	if err == nil {
		t.Fatal("FindConfig(\"\") with no config files should error")
	}
}

func TestFindConfig_CWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("listen:\n  port: 8080\n"), 0600)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	got, err := FindConfig("")
	if err != nil {
		t.Fatalf("FindConfig(\"\") error: %v", err)
	}
	if got != "config.yaml" {
		t.Errorf("FindConfig(\"\") = %q, want %q", got, "config.yaml")
	}
}

func TestLoad_ExpandsEnvVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("homeassistant:\n  token: ${THANE_TEST_TOKEN}\n"), 0600)
	os.Setenv("THANE_TEST_TOKEN", "secret123")
	defer os.Unsetenv("THANE_TEST_TOKEN")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.HomeAssistant.Token != "secret123" {
		t.Errorf("token = %q, want %q", cfg.HomeAssistant.Token, "secret123")
	}
}

func TestLoad_InlineSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("anthropic:\n  api_key: sk-ant-test-key\n"), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Anthropic.APIKey != "sk-ant-test-key" {
		t.Errorf("api_key = %q, want %q", cfg.Anthropic.APIKey, "sk-ant-test-key")
	}
}

func TestAgentConfig_DefaultIter0Tools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("agent:\n  delegation_required: true\n"), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if !cfg.Agent.DelegationRequired {
		t.Fatal("expected delegation_required to be true")
	}

	want := []string{"thane_delegate", "recall_fact", "remember_fact", "session_working_memory", "archive_search"}
	if len(cfg.Agent.Iter0Tools) != len(want) {
		t.Fatalf("iter0_tools length = %d, want %d; got %v", len(cfg.Agent.Iter0Tools), len(want), cfg.Agent.Iter0Tools)
	}
	for i, name := range want {
		if cfg.Agent.Iter0Tools[i] != name {
			t.Errorf("iter0_tools[%d] = %q, want %q", i, cfg.Agent.Iter0Tools[i], name)
		}
	}
}

func TestAgentConfig_CustomIter0Tools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("agent:\n  delegation_required: true\n  iter0_tools:\n    - thane_delegate\n    - recall_fact\n"), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if len(cfg.Agent.Iter0Tools) != 2 {
		t.Fatalf("iter0_tools length = %d, want 2; got %v", len(cfg.Agent.Iter0Tools), cfg.Agent.Iter0Tools)
	}
	if cfg.Agent.Iter0Tools[0] != "thane_delegate" || cfg.Agent.Iter0Tools[1] != "recall_fact" {
		t.Errorf("iter0_tools = %v, want [thane_delegate recall_fact]", cfg.Agent.Iter0Tools)
	}
}

func TestAgentConfig_NoDefaultsWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("agent:\n  delegation_required: false\n"), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if len(cfg.Agent.Iter0Tools) != 0 {
		t.Errorf("iter0_tools should be empty when delegation_required is false, got %v", cfg.Agent.Iter0Tools)
	}
}
