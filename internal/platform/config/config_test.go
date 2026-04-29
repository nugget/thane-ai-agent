package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestLoad_CompanionConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
companion:
  enabled: true
  providers:
    nugget:
      tokens: ["secret"]
`), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if !cfg.Companion.Configured() {
		t.Fatal("expected companion config to be configured")
	}
	if got := cfg.Companion.TokenIndex()["secret"]; got != "nugget" {
		t.Fatalf("token index = %q, want nugget", got)
	}
}

// TestLoad_RetiredTopLevelPlatformIsRejected guards against silent
// misconfiguration when an operator carries a pre-v0.9.x config with a
// top-level platform: block. The subsystem behind that key was renamed
// to companion:; yaml.v3 would otherwise accept the document and leave
// Companion unconfigured. Load must fail fast with an actionable error
// pointing at the rename.
func TestLoad_RetiredTopLevelPlatformIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
platform:
  enabled: true
  providers:
    nugget:
      tokens: ["legacy-secret"]
`), 0600)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected Load to reject top-level platform: section")
	}
	if !strings.Contains(err.Error(), "platform:") || !strings.Contains(err.Error(), "companion:") {
		t.Errorf("error %q should mention both platform: and companion:", err)
	}
}

func TestAgentConfig_DefaultOrchestratorTools(t *testing.T) {
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

	want := []string{"thane_delegate", "recall_fact", "remember_fact", "save_contact", "lookup_contact", "owner_contact", "session_working_memory", "session_close", "archive_search"}
	if len(cfg.Agent.OrchestratorTools) != len(want) {
		t.Fatalf("orchestrator_tools length = %d, want %d; got %v", len(cfg.Agent.OrchestratorTools), len(want), cfg.Agent.OrchestratorTools)
	}
	for i, name := range want {
		if cfg.Agent.OrchestratorTools[i] != name {
			t.Errorf("orchestrator_tools[%d] = %q, want %q", i, cfg.Agent.OrchestratorTools[i], name)
		}
	}
}

func TestAgentConfig_CustomOrchestratorTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("agent:\n  delegation_required: true\n  orchestrator_tools:\n    - thane_delegate\n    - recall_fact\n"), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if len(cfg.Agent.OrchestratorTools) != 2 {
		t.Fatalf("orchestrator_tools length = %d, want 2; got %v", len(cfg.Agent.OrchestratorTools), cfg.Agent.OrchestratorTools)
	}
	if cfg.Agent.OrchestratorTools[0] != "thane_delegate" || cfg.Agent.OrchestratorTools[1] != "recall_fact" {
		t.Errorf("orchestrator_tools = %v, want [thane_delegate recall_fact]", cfg.Agent.OrchestratorTools)
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

	if len(cfg.Agent.OrchestratorTools) != 0 {
		t.Errorf("orchestrator_tools should be empty when delegation_required is false, got %v", cfg.Agent.OrchestratorTools)
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

func TestContentMaxLength_Default(t *testing.T) {
	cfg := Default()
	if got := cfg.Logging.ContentMaxLength(); got != 4096 {
		t.Errorf("ContentMaxLength() default = %d, want 4096", got)
	}
}

func TestContentMaxLength_Explicit(t *testing.T) {
	cfg := Default()
	n := 8192
	cfg.Logging.MaxContentLength = &n
	if got := cfg.Logging.ContentMaxLength(); got != 8192 {
		t.Errorf("ContentMaxLength() = %d, want 8192", got)
	}
}

func TestContentMaxLength_Zero(t *testing.T) {
	cfg := Default()
	n := 0
	cfg.Logging.MaxContentLength = &n
	if got := cfg.Logging.ContentMaxLength(); got != 0 {
		t.Errorf("ContentMaxLength() = %d, want 0 (unlimited)", got)
	}
}

func TestContentMaxLength_Negative(t *testing.T) {
	cfg := Default()
	n := -1
	cfg.Logging.MaxContentLength = &n
	if got := cfg.Logging.ContentMaxLength(); got != 4096 {
		t.Errorf("ContentMaxLength() = %d, want 4096 (clamped to default)", got)
	}
}

func TestApplyDefaults_EmbeddingsBaseURLUsesImplicitOllamaServer(t *testing.T) {
	cfg := &Config{
		Models: ModelsConfig{
			Resources: map[string]ModelServerConfig{
				"default": {URL: "http://ollama-primary:11434"},
			},
		},
	}

	cfg.applyDefaults()

	if got := cfg.Models.Resources["default"].Provider; got != "ollama" {
		t.Fatalf("models.resources.default.provider = %q, want %q", got, "ollama")
	}
	if got := cfg.Embeddings.BaseURL; got != "http://ollama-primary:11434" {
		t.Fatalf("Embeddings.BaseURL = %q, want %q", got, "http://ollama-primary:11434")
	}
}

func TestValidate_ModelResourceIdleTTLNegative(t *testing.T) {
	cfg := Default()
	cfg.Models.Resources = map[string]ModelServerConfig{
		"deepslate": {
			URL:            "http://127.0.0.1:1234",
			Provider:       "lmstudio",
			IdleTTLSeconds: -1,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative idle_ttl_seconds")
	}
	if !strings.Contains(err.Error(), "models.resources.deepslate.idle_ttl_seconds") {
		t.Fatalf("error = %v, want models.resources.deepslate.idle_ttl_seconds", err)
	}
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

func TestValidate_SignalInvalidRouting(t *testing.T) {
	cfg := Default()
	cfg.Signal = SignalConfig{
		Enabled: true,
		Command: "signal-cli",
		Account: "+15551234567",
		Routing: SignalRoutingConfig{
			QualityFloor: "11",
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid signal routing")
	}
	if !strings.Contains(err.Error(), "signal.routing") {
		t.Errorf("error should mention signal.routing, got: %v", err)
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

func TestValidate_CapabilityTagEmptyDescription(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"custom": {Description: "", Tools: []string{"get_state"}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty description")
	}
	if !strings.Contains(err.Error(), "capability_tags.custom.description") {
		t.Errorf("error should mention capability_tags.custom.description, got: %v", err)
	}
}

func TestValidate_CapabilityTagEmptyToolsAllowed(t *testing.T) {
	// Tags can exist purely to gate content (talents, KB articles via
	// tags_all/tags frontmatter, tag-context providers) without owning
	// any tools. Validation must allow that shape.
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"signal_channel": {Description: "Runtime gate for Signal-channel context.", Tools: nil},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("content-only tag rejected: %v", err)
	}
}

func TestValidate_BuiltinCapabilityOverlayMayOmitDescriptionAndTools(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"ha": {AlwaysActive: true},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error for builtin overlay: %v", err)
	}
}

func TestValidate_CapabilityTagValid(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"ha":  {Description: "Home Assistant", Tools: []string{"get_state"}, AlwaysActive: true},
		"web": {Description: "Web search", Tools: []string{"web_search"}},
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidate_CapabilityTagContentOnlyValid(t *testing.T) {
	// Companion of TestValidate_CapabilityTagEmptyToolsAllowed — a
	// purely content-gating tag (no tools, just a description) is a
	// valid first-class shape after the empty-tools requirement was
	// dropped.
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"docs": {
			Description: "Documentation context",
			Tools:       nil,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("content-only tag rejected: %v", err)
	}
}

func TestValidate_ChannelTagsUndefinedTag(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"signal": {Description: "Signal messaging", Tools: []string{"signal_send_reaction"}},
	}
	cfg.ChannelTags = map[string][]string{
		"signal": {"signal", "nonexistent"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for undefined capability tag reference")
	}
	if !strings.Contains(err.Error(), "channel_tags.signal") || !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention channel_tags.signal and nonexistent, got: %v", err)
	}
}

func TestValidate_ChannelTagsValid(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"signal": {Description: "Signal messaging", Tools: []string{"signal_send_reaction"}},
		"email":  {Description: "Email tools", Tools: []string{"email_send"}},
	}
	cfg.ChannelTags = map[string][]string{
		"signal": {"signal"},
		"email":  {"email"},
		"web":    {},
	}

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidate_ChannelTagsBuiltinTagValid(t *testing.T) {
	cfg := Default()
	cfg.ChannelTags = map[string][]string{
		"signal": {"ha", "web", "interactive"},
		"owu":    {"interactive", "owu"},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidate_ChannelTagsEmptyIsValid(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"ha": {Description: "Home Assistant", Tools: []string{"get_state"}},
	}
	// ChannelTags left nil

	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected validation error with nil ChannelTags: %v", err)
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

func TestApplyDefaults_Logging(t *testing.T) {
	cfg := Default()

	if cfg.Logging.Dir != nil {
		t.Errorf("Logging.Dir = %v, want nil (defaults via DirPath())", cfg.Logging.Dir)
	}
	if got := cfg.Logging.RootPath(); got != "logs" {
		t.Errorf("Logging.RootPath() = %q, want %q", got, "logs")
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "info")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "json")
	}
	if got := cfg.Logging.StdoutLevelValue(); got != "info" {
		t.Errorf("Logging.StdoutLevelValue() = %q, want %q", got, "info")
	}
	if got := cfg.Logging.StdoutFormatValue(); got != "json" {
		t.Errorf("Logging.StdoutFormatValue() = %q, want %q", got, "json")
	}
	if !cfg.Logging.DatasetEnabled("events") {
		t.Error("Logging.DatasetEnabled(events) = false, want true")
	}
	if cfg.Logging.DatasetEnabled("access") {
		t.Error("Logging.DatasetEnabled(access) = true, want false by default")
	}
}

func TestValidate_LoggingInvalidLevel(t *testing.T) {
	cfg := Default()
	cfg.Logging.Level = "bogus"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid logging.level")
	}
	if !strings.Contains(err.Error(), "logging.level") {
		t.Errorf("error %q should mention logging.level", err)
	}
}

func TestValidate_LoggingInvalidFormat(t *testing.T) {
	cfg := Default()
	cfg.Logging.Format = "xml"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid logging.format")
	}
	if !strings.Contains(err.Error(), "logging.format") {
		t.Errorf("error %q should mention logging.format", err)
	}
}

func TestValidate_DelegateNegativeToolTimeout(t *testing.T) {
	cfg := Default()
	cfg.Delegate.Profiles = map[string]DelegateProfileConfig{
		"general": {ToolTimeout: -1 * time.Second},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative tool_timeout")
	}
	if !strings.Contains(err.Error(), "tool_timeout") {
		t.Errorf("error %q should mention tool_timeout", err)
	}
}

func TestValidate_DelegateNegativeMaxDuration(t *testing.T) {
	cfg := Default()
	cfg.Delegate.Profiles = map[string]DelegateProfileConfig{
		"ha": {MaxDuration: -5 * time.Second},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative max_duration")
	}
	if !strings.Contains(err.Error(), "max_duration") {
		t.Errorf("error %q should mention max_duration", err)
	}
}

func TestValidate_DelegateNegativeMaxIter(t *testing.T) {
	cfg := Default()
	cfg.Delegate.Profiles = map[string]DelegateProfileConfig{
		"general": {MaxIter: -1},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative max_iter")
	}
	if !strings.Contains(err.Error(), "max_iter") {
		t.Errorf("error %q should mention max_iter", err)
	}
}

func TestValidate_DelegateZeroKeepsDefaults(t *testing.T) {
	cfg := Default()
	cfg.Delegate.Profiles = map[string]DelegateProfileConfig{
		"general": {ToolTimeout: 3 * time.Minute},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestLoggingConfig_RootPath(t *testing.T) {
	tests := []struct {
		name string
		root *string
		dir  *string
		want string
	}{
		{"nil defaults to logs", nil, nil, "logs"},
		{"explicit root empty disables", strPtr(""), strPtr("/var/log/thane"), ""},
		{"explicit root wins", strPtr("/srv/thane/logs"), strPtr("/var/log/thane"), "/srv/thane/logs"},
		{"legacy dir used when root unset", nil, strPtr("/var/log/thane"), "/var/log/thane"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lc := LoggingConfig{Root: tt.root, Dir: tt.dir}
			if got := lc.RootPath(); got != tt.want {
				t.Errorf("RootPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoggingConfig_StdoutAndDatasetDefaults(t *testing.T) {
	lc := LoggingConfig{
		Level:  "warn",
		Format: "text",
	}

	if !lc.StdoutEnabled() {
		t.Error("StdoutEnabled() = false, want true by default")
	}
	if got := lc.StdoutLevelValue(); got != "warn" {
		t.Errorf("StdoutLevelValue() = %q, want %q", got, "warn")
	}
	if got := lc.StdoutFormatValue(); got != "text" {
		t.Errorf("StdoutFormatValue() = %q, want %q", got, "text")
	}
	if !lc.DatasetEnabled("loops") {
		t.Error("DatasetEnabled(loops) = false, want true")
	}
	if !lc.DatasetEnabled("envelopes") {
		t.Error("DatasetEnabled(envelopes) = false, want true")
	}
}

func TestValidate_LoggingInvalidStdoutLevel(t *testing.T) {
	cfg := Default()
	cfg.Logging.Stdout.Level = "loud"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid logging.stdout.level")
	}
	if !strings.Contains(err.Error(), "logging.stdout.level") {
		t.Errorf("error %q should mention logging.stdout.level", err)
	}
}

func TestValidate_LoggingInvalidStdoutFormat(t *testing.T) {
	cfg := Default()
	cfg.Logging.Stdout.Format = "yaml"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid logging.stdout.format")
	}
	if !strings.Contains(err.Error(), "logging.stdout.format") {
		t.Errorf("error %q should mention logging.stdout.format", err)
	}
}

func TestLoad_DocumentRootConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
paths:
  kb: ./knowledge
doc_roots:
  kb:
    indexing: false
    authoring: read_only
    git:
      enabled: true
      sign_commits: false
      verify_signatures: required
      repo_path: ./knowledge
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	root := cfg.DocRoots["kb"]
	if root.Indexing == nil || *root.Indexing {
		t.Fatalf("DocRoots[kb].Indexing = %v, want false pointer", root.Indexing)
	}
	if root.Authoring != "read_only" {
		t.Fatalf("DocRoots[kb].Authoring = %q, want read_only", root.Authoring)
	}
	if !root.Git.Enabled || root.Git.VerifySignatures != "required" {
		t.Fatalf("DocRoots[kb].Git = %#v, want enabled required verification", root.Git)
	}
}

func TestValidate_DocumentRootConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		root DocumentRootConfig
		want string
	}{
		{
			name: "invalid authoring",
			root: DocumentRootConfig{Authoring: "free_for_all"},
			want: "authoring",
		},
		{
			name: "invalid verification",
			root: DocumentRootConfig{Git: DocumentRootGitConfig{VerifySignatures: "strict"}},
			want: "verify_signatures",
		},
		{
			name: "sign without git enabled",
			root: DocumentRootConfig{Git: DocumentRootGitConfig{SignCommits: true, SigningKey: "~/.ssh/id_ed25519"}},
			want: "git.enabled must be true",
		},
		{
			name: "sign without signing key",
			root: DocumentRootConfig{Git: DocumentRootGitConfig{Enabled: true, SignCommits: true}},
			want: "signing_key is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Default()
			cfg.DocRoots = map[string]DocumentRootConfig{"kb": tc.root}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate error = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestLoad_RootsBlockBareString covers the bare-string shorthand:
// `name: ~/path` desugars into Paths with default policy.
func TestLoad_RootsBlockBareString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  kb: ./knowledge
  scratchpad: ./scratch
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Paths["kb"] != "./knowledge" {
		t.Errorf("Paths[kb] = %q, want ./knowledge", cfg.Paths["kb"])
	}
	if cfg.Paths["scratchpad"] != "./scratch" {
		t.Errorf("Paths[scratchpad] = %q, want ./scratch", cfg.Paths["scratchpad"])
	}
	// Bare-string entries should not produce DocRoots rows
	// because they have no policy fields.
	if _, ok := cfg.DocRoots["kb"]; ok {
		t.Errorf("bare-string entry should not produce a DocRoots row: %#v", cfg.DocRoots)
	}
	// Roots is cleared after normalize.
	if cfg.Roots != nil {
		t.Errorf("Roots should be cleared after normalize, got %#v", cfg.Roots)
	}
}

// TestLoad_RootsBlockMixedForms verifies that bare-string and full
// mapping forms can coexist in the same roots: block.
func TestLoad_RootsBlockMixedForms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  kb: ./knowledge
  scratchpad:
    path: ./scratch
    authoring: managed
  secure:
    path: ./secure
    indexing: false
    git:
      enabled: true
      sign_commits: true
      verify_signatures: required
      signing_key: ./id_ed25519
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Paths["kb"] != "./knowledge" || cfg.Paths["scratchpad"] != "./scratch" || cfg.Paths["secure"] != "./secure" {
		t.Errorf("Paths = %#v, want all three roots", cfg.Paths)
	}
	if _, ok := cfg.DocRoots["kb"]; ok {
		t.Errorf("kb (bare string) should not have DocRoots entry")
	}
	if cfg.DocRoots["scratchpad"].Authoring != "managed" {
		t.Errorf("scratchpad authoring = %q, want managed", cfg.DocRoots["scratchpad"].Authoring)
	}
	secure := cfg.DocRoots["secure"]
	if secure.Indexing == nil || *secure.Indexing {
		t.Errorf("secure indexing = %v, want false pointer", secure.Indexing)
	}
	if !secure.Git.Enabled || !secure.Git.SignCommits || secure.Git.VerifySignatures != "required" || secure.Git.SigningKey != "./id_ed25519" {
		t.Errorf("secure.Git = %#v, want enabled+signed+required", secure.Git)
	}
}

// TestLoad_RootsBlockRejectsLegacyMix guards against silently
// accepting both shapes — the operator must pick one.
func TestLoad_RootsBlockRejectsLegacyMix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  kb: ./knowledge
paths:
  scratchpad: ./scratch
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load should error when both roots: and paths: are present")
	}
	if !strings.Contains(err.Error(), "roots:") || !strings.Contains(err.Error(), "paths:") {
		t.Fatalf("error = %v, want explanation that the two shapes can't coexist", err)
	}
}

// TestLoad_RootsBlockReservedCoreNamePolicyOnly verifies that core:
// can be declared in roots: solely to set policy; the path is
// ignored (the runtime always derives core from workspace.path).
func TestLoad_RootsBlockReservedCoreNamePolicyOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  core:
    path: /should/be/ignored
    authoring: managed
    git:
      enabled: true
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if _, ok := cfg.Paths["core"]; ok {
		t.Errorf("core path should not be set from roots: (it's reserved); Paths = %#v", cfg.Paths)
	}
	if !cfg.DocRoots["core"].Git.Enabled {
		t.Errorf("core policy should still apply even when path is ignored: %#v", cfg.DocRoots["core"])
	}
}

// TestLoad_LegacyShapeStillWorks verifies the legacy paths:+doc_roots:
// shape continues to load successfully (with a deprecation warning
// emitted to slog.Default — not asserted here, since tests don't
// capture default slog).
func TestLoad_LegacyShapeStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
paths:
  kb: ./knowledge
doc_roots:
  kb:
    authoring: read_only
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Paths["kb"] != "./knowledge" {
		t.Errorf("legacy Paths still expected; got %#v", cfg.Paths)
	}
	if cfg.DocRoots["kb"].Authoring != "read_only" {
		t.Errorf("legacy DocRoots still expected; got %#v", cfg.DocRoots)
	}
}

// TestNormalizeRoots_Programmatic exercises the normalize step on a
// hand-built Config (the Default+populate path some callers use).
func TestNormalizeRoots_Programmatic(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Roots: map[string]RootEntry{
			"kb": {Path: "/k"},
			"sp": {Path: "/s", Authoring: "managed"},
		},
	}
	if err := cfg.normalizeRoots(); err != nil {
		t.Fatalf("normalizeRoots: %v", err)
	}
	if cfg.Paths["kb"] != "/k" || cfg.Paths["sp"] != "/s" {
		t.Errorf("Paths = %#v", cfg.Paths)
	}
	if _, ok := cfg.DocRoots["kb"]; ok {
		t.Errorf("kb has no policy, should not produce DocRoots row")
	}
	if cfg.DocRoots["sp"].Authoring != "managed" {
		t.Errorf("sp.Authoring = %q, want managed", cfg.DocRoots["sp"].Authoring)
	}
	if cfg.Roots != nil {
		t.Errorf("Roots should be cleared after normalize")
	}
}

// TestLoad_RootsBlockRejectsNullShorthand guards against `kb:` (null
// scalar) silently becoming an empty path. yaml.v3 doesn't invoke
// UnmarshalYAML for a null map value, so the null is caught by the
// normalize-time empty-path check rather than the scalar guard.
func TestLoad_RootsBlockRejectsNullShorthand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  kb:
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load should error on null roots entry")
	}
	if !strings.Contains(err.Error(), "roots.kb.path") {
		t.Fatalf("error = %v, want empty-path or scalar-tag message", err)
	}
}

// TestLoad_RootsBlockRejectsNonStringScalar guards against typos like
// `kb: 42` or `kb: true` that would otherwise become path strings.
func TestLoad_RootsBlockRejectsNonStringScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  kb: 42
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load should error on non-string scalar shorthand")
	}
	if !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("error = %v, want non-string scalar message", err)
	}
}

// TestLoad_RootsBlockRejectsMappingWithoutPath guards against a
// non-core entry whose mapping omits path: — easy to do when
// templating policy without a path.
func TestLoad_RootsBlockRejectsMappingWithoutPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
roots:
  kb:
    authoring: managed
`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load should error when a non-core root mapping has no path")
	}
	if !strings.Contains(err.Error(), "roots.kb.path") {
		t.Fatalf("error = %v, want roots.kb.path message", err)
	}
}

// TestNormalizeRoots_RejectsEmptyPathProgrammatic mirrors the
// mapping-without-path case for callers that bypass YAML.
func TestNormalizeRoots_RejectsEmptyPathProgrammatic(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Roots: map[string]RootEntry{
			"kb": {Authoring: "managed"},
		},
	}
	err := cfg.normalizeRoots()
	if err == nil {
		t.Fatal("normalizeRoots should error on empty path for non-core root")
	}
	if !strings.Contains(err.Error(), "roots.kb.path") {
		t.Fatalf("error = %v, want roots.kb.path message", err)
	}
}

// TestNormalizeRoots_DetectsCanonicalCollision verifies that two keys
// that canonicalize to the same trimmed name (e.g. `kb` and `kb:`)
// are rejected rather than silently overwriting each other.
func TestNormalizeRoots_DetectsCanonicalCollision(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Roots: map[string]RootEntry{
			"kb":  {Path: "/a"},
			"kb:": {Path: "/b"},
		},
	}
	err := cfg.normalizeRoots()
	if err == nil {
		t.Fatal("normalizeRoots should error on canonical-name collision")
	}
	if !strings.Contains(err.Error(), "canonicalize") {
		t.Fatalf("error = %v, want canonicalize collision message", err)
	}
}

// TestNormalizeRoots_CoreReservedAcceptsPolicyOnly confirms that
// declaring core: with policy fields and no path is the supported
// shape (no warning, no error).
func TestNormalizeRoots_CoreReservedAcceptsPolicyOnly(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Roots: map[string]RootEntry{
			"core": {Authoring: "managed"},
		},
	}
	if err := cfg.normalizeRoots(); err != nil {
		t.Fatalf("normalizeRoots: %v", err)
	}
	if _, ok := cfg.Paths["core"]; ok {
		t.Errorf("core path must not be populated; Paths = %#v", cfg.Paths)
	}
	if cfg.DocRoots["core"].Authoring != "managed" {
		t.Errorf("core policy not applied: %#v", cfg.DocRoots["core"])
	}
}
