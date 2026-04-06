package config

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/forge"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/search"
)

// ExampleConfig returns a *Config populated with realistic example values
// for every field. It is the single source of truth for generated config
// documentation — callers should not hard-code example values elsewhere.
//
// Required sections (listen, homeassistant, models, etc.) contain real
// placeholder values. Optional sections contain example values that the
// generator emits as commented-out YAML blocks so new users can see the
// full configuration surface without accidentally enabling services they
// haven't configured.
//
// When new fields are added to [Config] or any sub-struct, ExampleConfig
// must be updated accordingly so the generated config stays complete.
// The generator (go generate ./internal/config) re-runs automatically
// on any change to this file or to the Config struct definitions.
func ExampleConfig() *Config {
	// Pointer helpers — needed for optional *T fields.
	logDir := "logs"
	compress := true
	retentionDays := 7
	maxContent := 4096
	archiveDays := 90
	sessionIdle := 30

	return &Config{
		// ── Required / always-shown sections ──────────────────────────────

		Listen: ListenConfig{
			Port: 8080,
		},

		OllamaAPI: OllamaAPIConfig{
			Enabled: true,
			Port:    11434,
		},

		HomeAssistant: HomeAssistantConfig{
			URL:   "https://your-homeassistant.local:8123",
			Token: "your-long-lived-access-token",
			Subscribe: SubscribeConfig{
				EntityGlobs:        []string{"person.*", "binary_sensor.*door*"},
				RateLimitPerMinute: 10,
			},
		},

		Models: ModelsConfig{
			Default:    "qwen2.5:72b",
			LocalFirst: true,
			Resources: map[string]ModelServerConfig{
				"default": {
					URL:      "http://your-primary-ollama-server:11434",
					Provider: "ollama",
				},
				"edge": {
					URL:      "http://your-edge-ollama-server:11434",
					Provider: "ollama",
				},
			},
			RecoveryModel: "qwen3:4b",
			Available: []ModelConfig{
				{
					Name:          "qwen3:4b",
					Resource:      "edge",
					SupportsTools: true,
					ContextWindow: 32768,
					Speed:         9,
					Quality:       5,
					CostTier:      0,
					MinComplexity: "simple",
				},
				{
					Name:          "qwen2.5:72b",
					Resource:      "default",
					SupportsTools: true,
					ContextWindow: 131072,
					Speed:         3,
					Quality:       9,
					CostTier:      0,
					MinComplexity: "moderate",
				},
			},
		},

		DataDir:    "./db",
		TalentsDir: "./talents",

		Paths: map[string]string{
			"generated":  "./generated",
			"kb":         "./knowledge",
			"scratchpad": "./scratchpad",
		},

		ShellExec: ShellExecConfig{
			Enabled: false,
			DeniedPatterns: []string{
				"rm -rf /",
				"rm -rf /*",
				"mkfs",
				"dd if=",
				":(){:|:&};:",
				"chmod -R 777 /",
				"> /dev/sda",
			},
			AllowedPrefixes:   []string{},
			DefaultTimeoutSec: 30,
		},

		Workspace: WorkspaceConfig{
			Path: "",
		},

		Embeddings: EmbeddingsConfig{
			Enabled: false,
			Model:   "nomic-embed-text",
		},

		Logging: LoggingConfig{
			Dir:                &logDir,
			Level:              "info",
			Format:             "json",
			Compress:           &compress,
			RetentionDays:      &retentionDays,
			RetainContent:      false,
			MaxContentLength:   &maxContent,
			ContentArchiveDays: &archiveDays,
		},

		// ── Optional sections (emitted as commented-out YAML) ─────────────

		Anthropic: AnthropicConfig{
			APIKey: "sk-ant-your-api-key",
		},

		MQTT: MQTTConfig{
			Broker:             "mqtts://your-broker:8883",
			Username:           "thane",
			Password:           "your-mqtt-password",
			DiscoveryPrefix:    "homeassistant",
			DeviceName:         "thane-ai-agent",
			PublishIntervalSec: 60,
			Subscriptions: []SubscriptionConfig{
				{Topic: "homeassistant/+/+/state"},
				{Topic: "frigate/events"},
				{
					Topic: "automation/wake/security",
					Wake: &router.LoopProfile{
						QualityFloor:     "7",
						Mission:          "automation",
						LocalOnly:        "false",
						DelegationGating: "disabled",
						InitialTags:      []string{"homeassistant"},
						Instructions:     "Evaluate the security event and decide if action is needed.",
					},
				},
			},
			Telemetry: TelemetryConfig{
				Enabled:  true,
				Interval: 60,
			},
		},

		Person: PersonConfig{
			Track: []string{"person.alice", "person.bob"},
			Devices: map[string][]DeviceMapping{
				"person.alice": {
					{MAC: "AA:BB:CC:DD:EE:FF"},
					{MAC: "11:22:33:44:55:66"},
				},
			},
			APRooms: map[string]string{
				"ap-office":  "office",
				"ap-bedroom": "bedroom",
			},
		},

		Unifi: UnifiConfig{
			URL:             "https://192.168.1.1",
			APIKey:          "your-unifi-api-key",
			PollIntervalSec: 30,
		},

		Signal: SignalConfig{
			Enabled:            true,
			Command:            "signal-cli",
			Account:            "+15551234567",
			Args:               []string{},
			RateLimitPerMinute: 10,
			SessionIdleMinutes: 30,
			HandleTimeout:      10 * time.Minute,
			Routing: SignalRoutingConfig{
				QualityFloor:     "6",
				Mission:          "conversation",
				DelegationGating: "disabled",
			},
		},

		CardDAV: CardDAVConfig{
			Enabled:  true,
			Listen:   []string{"127.0.0.1:8843"},
			Username: "thane",
			Password: "your-carddav-password",
		},

		Identity: IdentityConfig{
			ContactName:      "Thane",
			OwnerContactName: "Aimee",
		},

		Attachments: AttachmentsConfig{
			StoreDir: "~/Thane/generated/attachments",
			Vision: VisionConfig{
				Enabled: true,
				Model:   "llava:latest",
				Prompt:  "",
				Timeout: "30s",
			},
		},

		Provenance: ProvenanceConfig{
			Path:       "~/Thane/core",
			SigningKey: "~/.ssh/id_ed25519",
		},

		Loops: LoopsConfig{
			Definitions: []looppkg.Spec{
				{
					Name:       "office_watch",
					Enabled:    true,
					Task:       "Watch the office and report noteworthy changes or trends.",
					Operation:  looppkg.OperationService,
					Completion: looppkg.CompletionNone,
					Conditions: looppkg.Conditions{
						Schedule: &looppkg.ScheduleCondition{
							Timezone: "America/Chicago",
							Windows: []looppkg.ScheduleWindow{{
								Days:  []string{"mon", "tue", "wed", "thu", "fri"},
								Start: "08:30",
								End:   "18:00",
							}},
						},
					},
					Profile: router.LoopProfile{
						Mission:          "background",
						DelegationGating: "disabled",
						InitialTags:      []string{"homeassistant"},
						Instructions:     "Be concise and focus on high-signal observations.",
					},
					SleepMin:     2 * time.Minute,
					SleepMax:     10 * time.Minute,
					SleepDefault: 5 * time.Minute,
					Jitter:       looppkg.Float64Ptr(0.2),
					Metadata: map[string]string{
						"category": "observer",
					},
				},
			},
		},

		Forge: forge.Config{
			Accounts: []forge.AccountConfig{
				{
					Name:     "github",
					Provider: "github",
					URL:      "https://api.github.com",
					Token:    "ghp_your-token",
					Owner:    "your-username",
				},
			},
		},

		Email: email.Config{
			Accounts: []email.AccountConfig{
				{
					Name: "primary",
					IMAP: email.IMAPConfig{
						Host:     "imap.example.com",
						Port:     993,
						Username: "thane@example.com",
						Password: "your-email-password",
						TLS:      true,
					},
					SMTP: email.SMTPConfig{
						Host:     "smtp.example.com",
						Port:     587,
						Username: "thane@example.com",
						Password: "your-email-password",
					},
					DefaultFrom: "Thane <thane@example.com>",
				},
			},
		},

		Archive: ArchiveConfig{
			MetadataModel:      "qwen2.5-coder:32b",
			SummarizeInterval:  300,
			SummarizeTimeout:   60,
			SessionIdleMinutes: &sessionIdle,
		},

		Extraction: ExtractionConfig{
			Enabled:        false,
			Model:          "",
			MinMessages:    2,
			TimeoutSeconds: 30,
		},

		Episodic: EpisodicConfig{
			DailyDir:      "~/Thane/generated/daily",
			LookbackDays:  2,
			HistoryTokens: 4000,
		},

		Search: SearchConfig{
			Default: "searxng",
			SearXNG: search.SearXNGConfig{
				URL: "http://your-searxng:8080",
			},
			Brave: search.BraveConfig{
				APIKey: "your-brave-api-key",
			},
		},

		Media: MediaConfig{
			SubtitleLanguage:   "en",
			MaxTranscriptChars: 50000,
			FeedCheckInterval:  3600,
			MaxFeeds:           50,
			Analysis: AnalysisConfig{
				DefaultOutputPath: "~/Thane/generated/media",
			},
		},

		Metacognitive: MetacognitiveConfig{
			Enabled:               false,
			MinSleep:              "2m",
			MaxSleep:              "30m",
			DefaultSleep:          "10m",
			Jitter:                0.2,
			SupervisorProbability: 0.1,
			Router:                MetacognitiveRouterConfig{QualityFloor: 3},
			SupervisorRouter:      MetacognitiveRouterConfig{QualityFloor: 8},
		},

		Agent: AgentConfig{
			DelegationRequired: false,
		},

		Delegate: DelegateConfig{
			Profiles: map[string]DelegateProfileConfig{
				"general": {
					ToolTimeout: 3 * time.Minute,
					MaxDuration: 5 * time.Minute,
					MaxIter:     15,
					MaxTokens:   25000,
				},
			},
		},

		MCP: MCPConfig{
			Servers: []MCPServerConfig{
				{
					Name:      "my-mcp-server",
					Transport: "stdio",
					Command:   "npx",
					Args:      []string{"-y", "@modelcontextprotocol/server-example"},
				},
			},
		},

		Prewarm: PrewarmConfig{
			Enabled:  false,
			MaxFacts: 10,
			Archive: ArchivePrewarmConfig{
				Enabled:    false,
				MaxResults: 3,
				MaxBytes:   4000,
			},
		},

		StateWindow: StateWindowConfig{
			MaxEntries:    50,
			MaxAgeMinutes: 30,
		},

		CapabilityTags: map[string]CapabilityTagConfig{
			"ha": {
				Description:  "Home Assistant tools and sensors",
				AlwaysActive: true,
			},
		},

		ChannelTags: map[string][]string{
			"signal": {"ha", "search"},
			"email":  {"forge"},
		},

		Timezone: "America/Chicago",

		ExtraPath: []string{
			"$HOME/.local/bin",
			"/usr/local/go/bin",
		},

		Pricing: map[string]PricingEntry{
			"claude-opus-4-20250514": {
				InputPerMillion:  15.0,
				OutputPerMillion: 75.0,
			},
		},

		Debug: DebugConfig{
			DemoLoops: false,
		},
	}
}
