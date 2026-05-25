package app

import (
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	mqtt "github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/integrations/media"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const (
	unifiPollerDefinitionName     = "unifi-poller"
	haStateWatcherDefinitionName  = "ha-state-watcher"
	emailPollerDefinitionName     = "email-poller"
	forgeSubPollerDefinitionName  = "forge-subscription-poller"
	mediaFeedPollerDefinitionName = "media-feed-poller"
	mqttPublisherDefinitionName   = "mqtt"
	telemetryDefinitionName       = "telemetry"
)

// mqttDefaultHandlerName is the in-app alias for the mqtt package's
// built-in default-handler loop name. Aliased here so the builtin
// spec literal reads cleanly alongside the other naming constants.
var mqttDefaultHandlerName = mqtt.DefaultHandlerLoopName

func builtInServiceDefinitionSpecs(cfg *config.Config) []looppkg.Spec {
	if cfg == nil {
		return nil
	}

	var specs []looppkg.Spec

	if cfg.Unifi.Configured() && len(cfg.Person.Track) > 0 {
		pollInterval := time.Duration(cfg.Unifi.PollIntervalSec) * time.Second
		specs = append(specs, looppkg.Spec{
			Name:         unifiPollerDefinitionName,
			Enabled:      true,
			Task:         "Poll UniFi device locations and update room presence state.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Metadata: map[string]string{
				"subsystem": "unifi",
				"category":  "poller",
			},
		})
	}

	if cfg.HomeAssistant.Configured() {
		specs = append(specs, looppkg.Spec{
			Name:       haStateWatcherDefinitionName,
			Enabled:    true,
			Task:       "Watch Home Assistant state_changed events and feed ambient awareness state.",
			Operation:  looppkg.OperationService,
			Completion: looppkg.CompletionNone,
			Metadata: map[string]string{
				"subsystem": "homeassistant",
				"category":  "watcher",
			},
		})
	}

	if cfg.Email.Configured() && cfg.Email.PollIntervalSec > 0 {
		// Default landing zone for new-mail wakes when an operator
		// hasn't pointed the poller at a custom handler. Event-driven
		// so the loop sits idle on wakeCh until the poller delivers
		// an event-source envelope. Profile mirrors the routing the
		// retired emailPollTurnBuilder used to stamp on every wake —
		// triage benefits from the cloud-eligible tier with a
		// non-trivial quality floor; the per-iteration tags carried
		// on the envelope (owner / trusted / household / known /
		// stranger) let the model adapt depth without forking the
		// route.
		specs = append(specs, looppkg.Spec{
			Name:       email.DefaultHandlerLoopName,
			Enabled:    true,
			Task:       "Triage incoming email wakes. Each event carries a sender trust-zone tag — owner/trusted/household/known/stranger — use it to adapt: owners get direct responses, trusted senders get reviewed action, strangers get a low-cost classify-and-defer pass. Read with email_read when a message warrants a deeper look, reply via email_reply or send a fresh message via email_send, file or trash with email_move when handled, and notify the owner about anything that genuinely needs attention.",
			Operation:  looppkg.OperationEventDriven,
			Completion: looppkg.CompletionNone,
			// "email" stays in the loop's permanent tag set so the
			// email_* tools are loadable regardless of which
			// per-wake sender-trust tag (owner/trusted/etc.) is
			// active. Without this, the per-wake tags become a
			// non-empty InitialTags set that enables tag filtering,
			// and the email-tagged tools the handler is told to use
			// get filtered out.
			Tags: []string{"email"},
			Profile: router.LoopProfile{
				Mission:      "email_triage",
				LocalOnly:    "false",
				QualityFloor: 5,
				ExtraHints:   map[string]string{"source": "email_poll"},
			},
			Metadata: map[string]string{
				"subsystem": "email",
				"category":  "default_handler",
			},
		})

		pollInterval := time.Duration(cfg.Email.PollIntervalSec) * time.Second
		specs = append(specs, looppkg.Spec{
			Name:         emailPollerDefinitionName,
			Enabled:      true,
			Task:         "Poll configured email accounts for new inbound mail and dispatch event-source wakes to the configured handler loop.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Metadata: map[string]string{
				"subsystem": "email",
				"category":  "poller",
			},
		})
	}

	if cfg.Forge.Configured() && cfg.Forge.SubscriptionCheckInterval > 0 {
		pollInterval := time.Duration(cfg.Forge.SubscriptionCheckInterval) * time.Second
		specs = append(specs, looppkg.Spec{
			Name:         forgeSubPollerDefinitionName,
			Enabled:      true,
			Task:         "Poll followed code forge repositories for release and commit events.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Tags:         []string{"forge"},
			Metadata: map[string]string{
				"subsystem": "forge",
				"category":  "poller",
			},
		})
	}

	if cfg.Media.FeedCheckInterval > 0 {
		// Default landing zone for media feed wakes when an operator
		// hasn't pointed media_follow's wake_loop at a custom handler.
		// Event-driven so the loop sits idle on wakeCh until the
		// feed poller delivers an event-source envelope.
		specs = append(specs, looppkg.Spec{
			Name:       media.DefaultHandlerLoopName,
			Enabled:    true,
			Task:       "Triage media feed wake events: inspect each entry's trust_zone metadata, fetch and analyze worthwhile content with media_transcript and media_save_analysis, then notify the owner about anything noteworthy.",
			Operation:  looppkg.OperationEventDriven,
			Completion: looppkg.CompletionNone,
			// Match the routing the retired mediaFeedTurnBuilder used to
			// stamp on every feed turn. Without LocalOnly=false the
			// agent runtime's per-turn default (local_only=true) wins,
			// which would regress feed analysis to local models —
			// transcript summarization and triage benefit from the
			// cloud-eligible tier when it's available.
			Profile: router.LoopProfile{
				Mission:      "media_triage",
				LocalOnly:    "false",
				QualityFloor: 5,
				ExtraHints:   map[string]string{"source": "media_feed"},
			},
			Metadata: map[string]string{
				"subsystem": "media",
				"category":  "default_handler",
			},
		})

		pollInterval := time.Duration(cfg.Media.FeedCheckInterval) * time.Second
		specs = append(specs, looppkg.Spec{
			Name:         mediaFeedPollerDefinitionName,
			Enabled:      true,
			Task:         "Poll followed media feeds for new entries and dispatch analysis when needed.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Metadata: map[string]string{
				"subsystem": "media",
				"category":  "poller",
			},
		})
	}

	if cfg.MQTT.Configured() {
		// Default landing zone for mqtt_wake subscriptions that don't
		// declare a custom wake_loop. Event-driven so the loop sits
		// idle on wakeCh until an MQTT message arrives, then runs one
		// agent turn against the rendered notification context. The
		// model decides what to do based on the topic + payload — no
		// hardcoded routing, no inline-Profile spawn.
		specs = append(specs, looppkg.Spec{
			Name:       mqttDefaultHandlerName,
			Enabled:    true,
			Task:       "Triage MQTT wake events: inspect topic and payload, then act or escalate.",
			Operation:  looppkg.OperationEventDriven,
			Completion: looppkg.CompletionNone,
			Profile: router.LoopProfile{
				Mission:    "mqtt_handler",
				ExtraHints: map[string]string{"source": "mqtt_wake"},
			},
			Metadata: map[string]string{
				"subsystem": "mqtt",
				"category":  "default_handler",
			},
		})

		mqttInterval := time.Duration(cfg.MQTT.PublishIntervalSec) * time.Second
		specs = append(specs, looppkg.Spec{
			Name:         mqttPublisherDefinitionName,
			Enabled:      true,
			Task:         "Publish MQTT state and discovery updates on the configured cadence.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     mqttInterval,
			SleepMax:     mqttInterval,
			SleepDefault: mqttInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Metadata: map[string]string{
				"subsystem": "mqtt",
				"category":  "publisher",
			},
		})
	}

	if cfg.MQTT.Configured() && cfg.MQTT.Telemetry.Enabled {
		telInterval := time.Duration(cfg.MQTT.Telemetry.Interval) * time.Second
		specs = append(specs, looppkg.Spec{
			Name:         telemetryDefinitionName,
			Enabled:      true,
			Task:         "Collect runtime telemetry and publish it through MQTT sensors.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     telInterval,
			SleepMax:     telInterval,
			SleepDefault: telInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Metadata: map[string]string{
				"subsystem": "mqtt",
				"category":  "telemetry",
			},
		})
	}

	return specs
}

func appendMissingDefinition(base []looppkg.Spec, seen map[string]struct{}, spec looppkg.Spec) []looppkg.Spec {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return base
	}
	if _, exists := seen[name]; exists {
		return base
	}
	seen[name] = struct{}{}
	return append(base, spec)
}
