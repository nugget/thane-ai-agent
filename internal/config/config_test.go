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
	// When no config exists anywhere, should error
	// (Save and restore CWD to avoid finding the repo's config.yaml)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

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
