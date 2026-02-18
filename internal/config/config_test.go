package config

import (
	"os"
	"path/filepath"
	"strings"
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

	want := []string{"thane_delegate", "recall_fact", "remember_fact", "save_contact", "lookup_contact", "session_working_memory", "archive_search"}
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

func TestValidate_PersonDevicesUntrackedEntity(t *testing.T) {
	cfg := Default()
	cfg.Person.Track = []string{"person.alice"}
	cfg.Person.Devices = map[string][]DeviceMapping{
		"person.bob": {{MAC: "aa:bb:cc:dd:ee:ff"}}, // bob is not tracked
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for untracked entity in person.devices")
	}
	if !strings.Contains(err.Error(), "person.bob") {
		t.Errorf("error should mention person.bob, got: %v", err)
	}
}

func TestValidate_PersonDevicesEmptyMAC(t *testing.T) {
	cfg := Default()
	cfg.Person.Track = []string{"person.alice"}
	cfg.Person.Devices = map[string][]DeviceMapping{
		"person.alice": {{MAC: ""}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty MAC address")
	}
	if !strings.Contains(err.Error(), "mac must not be empty") {
		t.Errorf("error should mention empty mac, got: %v", err)
	}
}

func TestValidate_UnifiPollIntervalTooLow(t *testing.T) {
	cfg := Default()
	cfg.Unifi = UnifiConfig{
		URL:             "https://192.168.1.1",
		APIKey:          "test-key",
		PollIntervalSec: 5, // below minimum of 10
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for poll_interval below 10")
	}
	if !strings.Contains(err.Error(), "unifi.poll_interval") {
		t.Errorf("error should mention unifi.poll_interval, got: %v", err)
	}
}

func TestApplyDefaults_UnifiPollInterval(t *testing.T) {
	cfg := Default()
	if cfg.Unifi.PollIntervalSec != 30 {
		t.Errorf("expected default poll_interval 30, got %d", cfg.Unifi.PollIntervalSec)
	}
}

func TestApplyDefaults_DebugDumpDir(t *testing.T) {
	t.Run("sets default when dump enabled", func(t *testing.T) {
		cfg := Default()
		cfg.Debug.DumpSystemPrompt = true
		cfg.applyDefaults()

		if cfg.Debug.DumpDir != "./debug" {
			t.Errorf("expected default dump_dir './debug', got %q", cfg.Debug.DumpDir)
		}
	})

	t.Run("leaves empty when dump disabled", func(t *testing.T) {
		cfg := Default()
		cfg.Debug.DumpSystemPrompt = false
		cfg.applyDefaults()

		if cfg.Debug.DumpDir != "" {
			t.Errorf("expected empty dump_dir when dump disabled, got %q", cfg.Debug.DumpDir)
		}
	})

	t.Run("preserves custom dir", func(t *testing.T) {
		cfg := Default()
		cfg.Debug.DumpSystemPrompt = true
		cfg.Debug.DumpDir = "/tmp/thane-debug"
		cfg.applyDefaults()

		if cfg.Debug.DumpDir != "/tmp/thane-debug" {
			t.Errorf("expected custom dump_dir preserved, got %q", cfg.Debug.DumpDir)
		}
	})
}

func TestValidate_PersonDevicesValid(t *testing.T) {
	cfg := Default()
	cfg.Person.Track = []string{"person.alice"}
	cfg.Person.Devices = map[string][]DeviceMapping{
		"person.alice": {{MAC: "aa:bb:cc:dd:ee:ff"}},
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidate_SignalRateLimitNegative(t *testing.T) {
	cfg := Default()
	cfg.Signal = SignalConfig{
		Enabled:            true,
		Command:            "signal-cli",
		Account:            "+15551234567",
		RateLimitPerMinute: -1,
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative rate_limit_per_minute")
	}
	if !strings.Contains(err.Error(), "signal.rate_limit_per_minute") {
		t.Errorf("error should mention signal.rate_limit_per_minute, got: %v", err)
	}
}

func TestValidate_SignalValid(t *testing.T) {
	cfg := Default()
	cfg.Signal = SignalConfig{
		Enabled:            true,
		Command:            "signal-cli",
		Account:            "+15551234567",
		RateLimitPerMinute: 10,
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidate_SignalEnabledMissingCommand(t *testing.T) {
	cfg := Default()
	cfg.Signal = SignalConfig{
		Enabled: true,
		Account: "+15551234567",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing command")
	}
	if !strings.Contains(err.Error(), "signal.command") {
		t.Errorf("error should mention signal.command, got: %v", err)
	}
}

func TestValidate_SignalEnabledMissingAccount(t *testing.T) {
	cfg := Default()
	cfg.Signal = SignalConfig{
		Enabled: true,
		Command: "signal-cli",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing account")
	}
	if !strings.Contains(err.Error(), "signal.account") {
		t.Errorf("error should mention signal.account, got: %v", err)
	}
}

func TestValidate_SignalDisabledSkipsValidation(t *testing.T) {
	cfg := Default()
	cfg.Signal = SignalConfig{
		Enabled: false,
		Command: "", // would be insufficient if enabled
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("disabled signal should skip validation, got: %v", err)
	}
}

func TestSignalConfig_Configured(t *testing.T) {
	tests := []struct {
		name string
		cfg  SignalConfig
		want bool
	}{
		{"all set", SignalConfig{Enabled: true, Command: "signal-cli", Account: "+1"}, true},
		{"disabled", SignalConfig{Enabled: false, Command: "signal-cli", Account: "+1"}, false},
		{"no command", SignalConfig{Enabled: true, Command: "", Account: "+1"}, false},
		{"no account", SignalConfig{Enabled: true, Command: "signal-cli", Account: ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyDefaults_SignalRateLimit(t *testing.T) {
	cfg := Default()
	// Zero means unlimited — no default override so users can disable
	// rate limiting by omitting the field.
	if cfg.Signal.RateLimitPerMinute != 0 {
		t.Errorf("expected default rate_limit_per_minute 0 (unlimited), got %d", cfg.Signal.RateLimitPerMinute)
	}
}
