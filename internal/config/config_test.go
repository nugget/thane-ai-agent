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

func TestValidate_CapabilityTagEmptyTools(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"custom": {Description: "Web search", Tools: nil},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty tools")
	}
	if !strings.Contains(err.Error(), "capability_tags.custom.tools") {
		t.Errorf("error should mention capability_tags.custom.tools, got: %v", err)
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

func TestValidate_CapabilityTagNoTools(t *testing.T) {
	cfg := Default()
	cfg.CapabilityTags = map[string]CapabilityTagConfig{
		"docs": {
			Description: "Documentation context",
			Tools:       nil, // tools are required
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for tag with no tools")
	}
	if !strings.Contains(err.Error(), "capability_tags.docs.tools") {
		t.Errorf("error should mention capability_tags.docs.tools, got: %v", err)
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

func TestApplyDefaults_LoggingMigration(t *testing.T) {
	tests := []struct {
		name      string
		logLevel  string
		logFormat string
		wantLevel string
		wantFmt   string
	}{
		{
			name:      "legacy log_level migrates",
			logLevel:  "debug",
			wantLevel: "debug",
			wantFmt:   "json", // new default
		},
		{
			name:      "legacy log_format migrates",
			logFormat: "text",
			wantLevel: "info",
			wantFmt:   "text",
		},
		{
			name:      "both legacy fields migrate",
			logLevel:  "warn",
			logFormat: "text",
			wantLevel: "warn",
			wantFmt:   "text",
		},
		{
			name:      "no legacy fields uses new defaults",
			wantLevel: "info",
			wantFmt:   "json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				LogLevel:  tt.logLevel,
				LogFormat: tt.logFormat,
			}
			cfg.applyDefaults()

			if cfg.Logging.Level != tt.wantLevel {
				t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, tt.wantLevel)
			}
			if cfg.Logging.Format != tt.wantFmt {
				t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, tt.wantFmt)
			}
		})
	}
}

func TestApplyDefaults_LoggingNewFieldsOverrideLegacy(t *testing.T) {
	// When both new and legacy fields are set, new fields win.
	cfg := &Config{
		LogLevel:  "debug",
		LogFormat: "text",
		Logging: LoggingConfig{
			Level:  "warn",
			Format: "json",
		},
	}
	cfg.applyDefaults()

	if cfg.Logging.Level != "warn" {
		t.Errorf("Logging.Level = %q, want %q (new field should win)", cfg.Logging.Level, "warn")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %q, want %q (new field should win)", cfg.Logging.Format, "json")
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

func TestConfig_DeprecatedFieldsUsed(t *testing.T) {
	cfg := &Config{LogLevel: "debug"}
	lvl, fmt := cfg.DeprecatedFieldsUsed()
	if !lvl {
		t.Error("expected level=true when LogLevel is set")
	}
	if fmt {
		t.Error("expected format=false when LogFormat is empty")
	}
}

func TestConfig_DeprecatedFieldsUsed_FreshConfig(t *testing.T) {
	// A config with no legacy fields should NOT trigger deprecation warnings,
	// even after applyDefaults has run.
	cfg := Default()
	lvl, format := cfg.DeprecatedFieldsUsed()
	if lvl {
		t.Error("expected level=false on fresh config, got true")
	}
	if format {
		t.Error("expected format=false on fresh config, got true")
	}
}

func strPtr(s string) *string { return &s }
