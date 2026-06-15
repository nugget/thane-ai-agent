package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/platform/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/telemetry"
	"github.com/nugget/thane-ai-agent/internal/server/api"
	cdav "github.com/nugget/thane-ai-agent/internal/server/carddav"
	"github.com/nugget/thane-ai-agent/internal/server/web"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// initServers creates servers, infrastructure services, and background
// publisher loops. This covers the API server, checkpointer, OWU tracker,
// Ollama-compatible server, CardDAV, MQTT publishing, the web dashboard,
// and durable loop-definition services.
func (a *App) initServers(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	// --- API server ---
	// The primary HTTP server exposing the OpenAI-compatible chat API,
	// health endpoint, router introspection, and the web UI.
	server := api.NewServer(
		cfg.Listen.Address,
		cfg.Listen.Port,
		a.loop,
		a.rtr,
		cfg.Pricing,
		a.modelRegistry,
		a.usageStore,
		a.persistModelRegistryPolicy,
		a.deletePersistedModelRegistryPolicy,
		a.persistModelRegistryResourcePolicy,
		a.deletePersistedModelRegistryResourcePolicy,
		logger,
	)
	server.SetMemoryStore(a.mem)
	server.SetArchiveStore(a.archiveStore)
	server.UseContactStore(a.contactStore)
	server.UseLoopDefinitionRegistry(a.loopDefinitionRegistry)
	server.ConfigureLoopDefinitionView(a.loopDefinitionView)
	server.ConfigureLoopDefinitionPersistence(a.persistLoopDefinition, a.deletePersistedLoopDefinition)
	server.ConfigureLoopDefinitionLifecycle(
		a.persistLoopDefinitionPolicy,
		a.deletePersistedLoopDefinitionPolicy,
		a.reconcileLoopDefinition,
		a.launchLoopDefinition,
	)
	server.ConfigureChatLoopLauncher(a.launchLoop)
	server.SetEventBus(a.eventBus)
	server.UseLoopRegistry(a.loopRegistry)
	if a.indexDB != nil {
		server.UseLogQuerier(&logQueryAdapter{db: a.indexDB})
	}
	server.SetConnManager(func() map[string]api.DependencyStatus {
		status := a.connMgr.Status()
		result := make(map[string]api.DependencyStatus, len(status))
		for name, st := range status {
			ds := api.DependencyStatus{
				Name:      st.Name,
				Ready:     st.Ready,
				LastError: st.LastError,
			}
			if !st.LastCheck.IsZero() {
				ds.LastCheck = st.LastCheck.Format(time.RFC3339)
			}
			result[name] = ds
		}
		return result
	})
	server.ConfigureAnthropicRateLimitSnapshotSource(func() *fleet.AnthropicRateLimitSnapshot {
		if a.modelRuntime == nil {
			return nil
		}
		return a.modelRuntime.AnthropicRateLimitSnapshot()
	})
	a.server = server

	// --- Checkpointer ---
	// Periodically snapshots application state (conversations, facts,
	// scheduled tasks) to enable crash recovery. Also creates a snapshot
	// on clean shutdown and before model failover. Shares thane.db.
	checkpointCfg := checkpoint.Config{
		PeriodicMessages: 50, // Snapshot every 50 messages
	}
	checkpointer, err := checkpoint.NewCheckpointer(a.mem.DB(), checkpointCfg, logger)
	if err != nil {
		return fmt.Errorf("create checkpointer: %w", err)
	}
	a.checkpointer = checkpointer

	// Wire up the data providers that the checkpointer snapshots.
	checkpointer.SetProviders(
		func() ([]checkpoint.Conversation, error) {
			convs := a.mem.GetAllConversations()
			result := make([]checkpoint.Conversation, len(convs))
			for i, c := range convs {
				msgs := make([]checkpoint.SourceMessage, len(c.Messages))
				for j, m := range c.Messages {
					msgs[j] = checkpoint.SourceMessage{
						Role:      m.Role,
						Content:   m.Content,
						Timestamp: m.Timestamp,
					}
				}
				conv, err := checkpoint.ConvertConversation(c.ID, c.CreatedAt, c.UpdatedAt, msgs)
				if err != nil {
					return nil, fmt.Errorf("convert conversation %s: %w", c.ID, err)
				}
				result[i] = conv
			}
			return result, nil
		},
		func() ([]checkpoint.Fact, error) {
			allFacts, err := a.factStore.GetAll()
			if err != nil {
				return nil, err
			}
			result := make([]checkpoint.Fact, len(allFacts))
			for i, f := range allFacts {
				result[i] = checkpoint.Fact{
					ID:         f.ID,
					Category:   string(f.Category),
					Key:        f.Key,
					Value:      f.Value,
					Source:     f.Source,
					CreatedAt:  f.CreatedAt,
					UpdatedAt:  f.UpdatedAt,
					Confidence: f.Confidence,
				}
			}
			return result, nil
		},
		func() ([]checkpoint.Task, error) {
			tasks, err := a.sched.GetAllTasks()
			if err != nil {
				return nil, err
			}
			result := make([]checkpoint.Task, len(tasks))
			for i, t := range tasks {
				result[i] = checkpoint.Task{
					ID:          checkpoint.ParseUUID(t.ID),
					Name:        t.Name,
					Description: "",
					Schedule:    t.Schedule.Cron,
					Action:      string(t.Payload.Kind),
					Enabled:     t.Enabled,
					CreatedAt:   t.CreatedAt,
				}
			}
			return result, nil
		},
	)
	server.SetCheckpointer(checkpointer)
	a.loop.SetFailoverHandler(checkpointer)
	logger.Info("checkpointing enabled", "periodic_messages", checkpointCfg.PeriodicMessages)

	checkpointer.LogStartupStatus()

	// --- OWU tracker ---
	// Registers a parent "owu" loop and lazily spawns per-conversation
	// children so that Open WebUI sessions appear on the dashboard.
	owuTracker, err := api.NewOWUTracker(
		s.ctx,
		a.loopRegistry,
		a.eventBus,
		&loopAdapter{agentLoop: a.loop, router: a.rtr, capSurface: a.capSurfaceGetter()},
		logger,
	)
	if err != nil {
		return fmt.Errorf("create owu tracker: %w", err)
	}
	owuTracker.UseConversationBindingWriter(a.mem.BindConversationChannel)
	server.SetOWUTracker(owuTracker)

	// --- Ollama-compatible API server ---
	// Optional second HTTP server that speaks the Ollama wire protocol.
	// Home Assistant's Ollama integration connects here, allowing Thane
	// to serve as a drop-in replacement for a standalone Ollama instance.
	if cfg.OllamaAPI.Enabled {
		a.ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, cfg.OllamaAPI.Port, a.loop, logger)
		a.ollamaServer.SetOWUTracker(owuTracker)
	}

	// --- Companion app endpoint ---
	// Optional: WebSocket endpoint for native companion apps (e.g. macOS)
	// to connect and register capabilities for bidirectional service dispatch.
	if cfg.Companion.Configured() {
		a.companionRegistry = companion.NewRegistry(logger)
		a.loop.Tools().EnableCompanionTools(a.companionRegistry.Call)
		handler := companion.NewHandler(cfg.Companion.TokenIndex(), a.companionRegistry, logger)
		server.SetCompanionHandler(handler)

		a.connMgr.Watch(s.ctx, connwatch.WatcherConfig{
			Name: "companion",
			Probe: func(_ context.Context) error {
				if a.companionRegistry.Count() == 0 {
					return fmt.Errorf("no providers connected")
				}
				return nil
			},
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  logger,
		})

		logger.Info("companion app endpoint enabled")
	}

	// --- CardDAV server ---
	// Optional: exposes the contacts store as a CardDAV address book so
	// native contact apps (macOS Contacts.app, iOS, Thunderbird) can sync.
	if cfg.CardDAV.Configured() {
		carddavBackend := cdav.NewBackend(a.contactStore, logger)
		a.carddavServer = cdav.NewServer(
			cfg.CardDAV.Listen,
			cfg.CardDAV.Username,
			cfg.CardDAV.Password,
			carddavBackend,
			logger,
		)
	}

	// --- MQTT publisher ---
	// Optional: publishes HA MQTT discovery messages and periodic sensor
	// state updates so Thane appears as a native HA device.
	var mqttConnectWorker func(context.Context) error
	if cfg.MQTT.Configured() {
		var err error
		a.mqttInstanceID, err = mqtt.LoadOrCreateInstanceID(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("load mqtt instance id: %w", err)
		}
		logger.Info("mqtt instance ID loaded", "instance_id", a.mqttInstanceID)

		// Timezone for midnight token counter reset.
		var tokenLoc *time.Location
		if cfg.Timezone != "" {
			tokenLoc, _ = time.LoadLocation(cfg.Timezone) // already validated
		}
		dailyTokens := mqtt.NewDailyTokens(tokenLoc)
		server.SetTokenObserver(dailyTokens)

		statsAdapter := &mqttStatsAdapter{
			model:  a.modelCatalog.DefaultModel,
			server: server,
		}

		// Auto-subscribe to the instance-specific callback topic when
		// actionable notifications are enabled. The topic follows the
		// existing baseTopic convention: thane/{device_name}/callbacks.
		// The subscription is appended to the user-configured list so
		// both ambient awareness topics and the callback topic are active.
		var callbackTopic string
		if a.notifCallbackDispatcher != nil {
			callbackTopic = "thane/" + cfg.MQTT.DeviceName + "/callbacks"
			found := false
			for _, sub := range cfg.MQTT.Subscriptions {
				if sub.Topic == callbackTopic {
					found = true
					break
				}
			}
			if !found {
				cfg.MQTT.Subscriptions = append(cfg.MQTT.Subscriptions, config.SubscriptionConfig{
					Topic: callbackTopic,
				})
			}
			logger.Info("notification callback topic configured", "topic", callbackTopic)
		}

		mqttPub := mqtt.New(cfg.MQTT, a.mqttInstanceID, dailyTokens, statsAdapter, logger)
		a.mqttPub = mqttPub

		// --- MQTT wake subscription store ---
		// Manages topic-to-LoopProfile mappings for wake-on-message.
		// Config-defined wake subscriptions are loaded from
		// cfg.MQTT.Subscriptions; runtime subscriptions persist in SQLite.
		subStore, err := mqtt.NewSubscriptionStore(a.mem.DB(), logger)
		if err != nil {
			return fmt.Errorf("create mqtt subscription store: %w", err)
		}
		if err := subStore.LoadConfig(cfg.MQTT.Subscriptions); err != nil {
			return fmt.Errorf("load mqtt wake subscriptions: %w", err)
		}
		// Expose the store so the loop-definition-services deferred
		// worker can VerifyTargets against the live registry once
		// every loop has been hydrated.
		a.mqttSubStore = subStore

		// Wire dynamic topic discovery: on every broker (re-)connect the
		// publisher merges store topics into the SUBSCRIBE packet.
		mqttPub.SetDynamicTopics(subStore.Topics)

		// Wire live subscribe: when a runtime subscription is added via
		// tool, immediately send a SUBSCRIBE to the broker so the topic
		// is active without waiting for reconnect.
		subStore.SetSubscribeHook(func(topics []string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := mqttPub.SubscribeTopics(ctx, topics); err != nil {
				logger.Warn("failed to live-subscribe new wake topic",
					"topics", topics, "error", err)
			}
		})

		// Build the base message handler: routes the instance callback
		// topic to the notification dispatcher, everything else gets
		// default debug logging.
		baseMsgHandler := mqtt.MessageHandler(func(topic string, payload []byte) {
			logger.Debug("mqtt message received", "topic", topic, "size", len(payload))
		})
		if a.notifCallbackDispatcher != nil {
			dispatcher := a.notifCallbackDispatcher // capture for closure
			cbTopic := callbackTopic                // capture for closure
			debugFallback := baseMsgHandler         // capture for closure
			baseMsgHandler = func(topic string, payload []byte) {
				if topic == cbTopic {
					dispatcher.Handle(topic, payload)
					return
				}
				debugFallback(topic, payload)
			}
		}

		// Track the mqtt parent loop ID once the definition runtime
		// starts the publisher loop. The wake handler can populate this
		// lazily from the loop registry when the first message arrives.
		var mqttParentID atomic.Value
		mqttParentID.Store("") // initialize with zero-value string
		wakeDeps := mqttWakeDeps{
			registry:   a.loopRegistry,
			messageBus: a.messageBus,
			eventBus:   a.eventBus,
			parentID:   &mqttParentID,
		}

		// Wrap with the wake handler: wake-configured topics dispatch
		// agent conversations, everything else falls through to the
		// base handler above.
		mqttPub.SetMessageHandler(mqttWakeHandler(
			subStore,
			baseMsgHandler,
			logger,
			wakeDeps,
		))

		// Register MQTT wake subscription tools via the provider.
		// loopRegistry doubles as the LoopResolver so wake_loop
		// arguments are verified against live loops at add time.
		a.loop.Tools().RegisterProvider(mqtt.NewWakeTools(mqtt.NewTools(subStore, a.loopRegistry)))

		// Defer MQTT connection to StartWorkers. The publisher object,
		// tooling, and message handler are already wired above; this just
		// activates the network connection.
		mqttConnectWorker = func(ctx context.Context) error {
			// Pass the long-lived server context as the lifecycle context
			// for the MQTT ConnectionManager. A short-lived context here
			// would kill the connection as soon as it expired (#572).
			// The initial connection await has its own internal timeout.
			if err := mqttPub.Connect(ctx); err != nil {
				logger.Error("mqtt publisher connection failed", "error", err)
				return nil // non-fatal: system works without MQTT
			}

			// Register with connwatch after a successful Connect so the
			// health probe doesn't fire before the publisher is ready.
			a.connMgr.Watch(ctx, connwatch.WatcherConfig{
				Name: "mqtt",
				Probe: func(pCtx context.Context) error {
					awaitCtx, awaitCancel := context.WithTimeout(pCtx, 2*time.Second)
					defer awaitCancel()
					return mqttPub.AwaitConnection(awaitCtx)
				},
				Backoff: connwatch.DefaultBackoffConfig(),
				Logger:  logger,
			})

			// Publish immediately on connect, then let the loop handle the schedule.
			mqttPub.PublishStates(ctx)

			logger.Info("mqtt connected",
				"broker", cfg.MQTT.Broker,
				"device_name", cfg.MQTT.DeviceName,
				"interval", cfg.MQTT.PublishIntervalSec,
			)
			return nil
		}

		logger.Info("mqtt publishing enabled",
			"broker", cfg.MQTT.Broker,
			"device_name", cfg.MQTT.DeviceName,
			"interval", cfg.MQTT.PublishIntervalSec,
		)
	} else {
		logger.Info("mqtt publishing disabled (not configured)")
	}

	// --- MQTT AP presence sensors ---
	// When both MQTT and UniFi room presence are active, register a
	// per-person AP sensor with the MQTT publisher and observe room
	// changes so state is published only when the AP actually changes.
	if a.mqttPub != nil && s.personTracker != nil && cfg.Unifi.Configured() {
		var apSensors []mqtt.DynamicSensor
		mqttInstanceID := a.mqttInstanceID
		for _, entityID := range cfg.Person.Track {
			shortName := entityID
			if idx := strings.IndexByte(entityID, '.'); idx >= 0 {
				shortName = entityID[idx+1:]
			}
			suffix := shortName + "_ap"

			apSensors = append(apSensors, mqtt.DynamicSensor{
				EntitySuffix: suffix,
				Config: mqtt.SensorConfig{
					Name:                contacts.TitleCase(shortName) + " AP",
					ObjectID:            a.mqttPub.ObjectIDPrefix() + suffix,
					HasEntityName:       true,
					UniqueID:            mqttInstanceID + "_" + suffix,
					StateTopic:          a.mqttPub.StateTopic(suffix),
					JsonAttributesTopic: a.mqttPub.AttributesTopic(suffix),
					AvailabilityTopic:   a.mqttPub.AvailabilityTopic(),
					Device:              a.mqttPub.Device(),
					Icon:                "mdi:access-point",
				},
			})
		}

		a.mqttPub.RegisterSensors(apSensors)

		// Route room changes from person tracker to MQTT publishes.
		s.personTracker.OnRoomChange(func(entityID, room, source string) {
			shortName := entityID
			if idx := strings.IndexByte(entityID, '.'); idx >= 0 {
				shortName = entityID[idx+1:]
			}
			suffix := shortName + "_ap"

			attrs, err := json.Marshal(map[string]string{
				"ap_name":      source,
				"last_changed": time.Now().Format(time.RFC3339),
			})
			if err != nil {
				logger.Warn("mqtt AP attributes marshal failed",
					"entity_id", entityID, "error", err)
				return
			}

			pubCtx, pubCancel := context.WithTimeout(s.ctx, 5*time.Second)
			defer pubCancel()

			if err := a.mqttPub.PublishDynamicState(pubCtx, suffix, room, attrs); err != nil {
				logger.Warn("mqtt AP presence publish failed",
					"entity_id", entityID, "room", room, "error", err)
			} else {
				logger.Debug("mqtt AP presence published",
					"entity_id", entityID, "room", room, "source", source)
			}
		})

		logger.Info("mqtt AP presence sensors registered", "count", len(apSensors))
	}

	// --- MQTT telemetry ---
	// When enabled, a dedicated loop collects operational metrics
	// (DB sizes, token usage, loop states, sessions, request perf,
	// attachments) and publishes them as native HA sensors.
	if a.mqttPub != nil && cfg.MQTT.Telemetry.Enabled {
		mqttInstanceID := a.mqttInstanceID
		telBuilder := &telemetry.SensorBuilder{
			InstanceID:        mqttInstanceID,
			Prefix:            a.mqttPub.ObjectIDPrefix(),
			StateTopicFn:      a.mqttPub.StateTopic,
			AttributesTopicFn: a.mqttPub.AttributesTopic,
			AvailabilityTopic: a.mqttPub.AvailabilityTopic(),
			Device:            a.mqttPub.Device(),
		}

		a.mqttPub.RegisterSensors(telBuilder.StaticSensors())

		dbPaths := map[string]string{
			"main":  filepath.Join(cfg.DataDir, "thane.db"),
			"usage": filepath.Join(cfg.DataDir, "usage.db"),
		}
		if logDir := cfg.Logging.DirPath(); logDir != "" {
			dbPaths["logs"] = filepath.Join(logDir, "logs.db")
		}
		if cfg.Attachments.StoreDir != "" {
			dbPaths["attachments"] = filepath.Join(cfg.DataDir, "attachments.db")
		}

		telSources := telemetry.Sources{
			LoopRegistry: a.loopRegistry,
			UsageStore:   a.usageStore,
			ArchiveStore: a.archiveStore,
			LogsDB:       a.indexDB,
			DBPaths:      dbPaths,
			Logger:       logger,
		}
		if a.attachmentStore != nil {
			telSources.AttachmentSource = a.attachmentStore
		}

		telCollector := telemetry.NewCollector(telSources)
		telPub := telemetry.NewPublisher(telCollector, a.mqttPub, telBuilder, logger)
		a.telemetryPublisher = telPub

		logger.Info("mqtt telemetry enabled",
			"interval", cfg.MQTT.Telemetry.Interval,
			"db_paths", len(dbPaths),
		)
	}

	// --- Web dashboard ---
	// Wire the web dashboard's static UI and read-only status endpoints.
	// Running-loop state now lives on the native API's /v1/loops* surface,
	// so the dashboard no longer takes the loop registry or event bus.
	{
		webCfg := web.Config{
			SystemStatus: &systemStatusAdapter{
				connMgr:       a.connMgr,
				modelRegistry: a.modelRegistry,
				modelRuntime:  a.modelRuntime,
				router:        a.rtr,
				capSurface:    a.capSurfaceGetter(),
			},
			Logger: logger,
		}
		if a.liveRequestStore != nil {
			webCfg.ContentQuerier = a.liveRequestStore
		}
		if a.indexDB != nil {
			webCfg.LogQuerier = &logQueryAdapter{db: a.indexDB}
			if cfg.Logging.RetainContent {
				webCfg.ContentQuerier = &fallbackContentQuerier{
					primary:  a.liveRequestStore,
					fallback: &contentQueryAdapter{db: a.indexDB},
				}
			}
		}
		server.SetWebServer(web.NewWebServer(webCfg))
		logger.Info("cognition engine dashboard enabled", "url", fmt.Sprintf("http://localhost:%d/", cfg.Listen.Port))
	}

	// --- Loop definition services ---
	// Durable loop service definitions are bootstrapped from the
	// immutable+overlay definition registry. Built-in services like
	// metacognitive, pollers, watchers, and MQTT publishers participate
	// as first-class definitions via runtime spec hydration.
	if a.loopDefinitionRuntime != nil {
		a.deferWorker("loop-definition-services", func(ctx context.Context) error {
			if err := a.migrateLegacyScopeTagSubscriptions(); err != nil {
				// Promoted from Warn to hard error: a failed
				// migration leaves subscription state partial —
				// spec.Subscriptions populated, legacy
				// scope_tag metadata still present, watchlist
				// rows un-wiped. The next startup re-runs the
				// migration. If an operator edits subscriptions
				// between failed-migration restarts (via
				// watch_entity / update_entity_subscriptions),
				// mergeLegacySubscriptions re-adds the entities
				// they removed because step 1 unions
				// def.Spec.Subscriptions with the legacy rows
				// that are still in the watchlist. Failing the
				// startup loud forces operator attention while
				// the migration is still a one-shot upgrade
				// path; the migration is idempotent in the
				// happy case, so a clean restart after fixing
				// the underlying issue (DB connectivity,
				// permissions, schema drift) resumes cleanly.
				return fmt.Errorf("legacy scope_tag migration failed — subscription state may be partial; fix underlying error and restart: %w", err)
			}
			// Core is auto-created synchronously during initStores —
			// before any deferred worker runs — so default-parenting
			// works for every loop the StartEnabledServices pass
			// below registers. Idempotent if anyone calls it again.
			if err := a.ensureCoreLoop(ctx); err != nil {
				return fmt.Errorf("ensure core loop: %w", err)
			}
			result, err := a.loopDefinitionRuntime.StartEnabledServices(ctx)
			if err != nil {
				return err
			}
			if result.Started > 0 || result.SkippedInactive > 0 || result.SkippedPaused > 0 || result.SkippedIneligible > 0 || result.SkippedExisting > 0 || result.SkippedNonService > 0 {
				logger.Info("loop definition services reconciled",
					"started", result.Started,
					"skipped_inactive", result.SkippedInactive,
					"skipped_paused", result.SkippedPaused,
					"skipped_ineligible", result.SkippedIneligible,
					"skipped_existing", result.SkippedExisting,
					"skipped_non_service", result.SkippedNonService,
				)
			}
			// Now that the durable definition snapshot is registered,
			// fail loud on any config-defined MQTT wake subscription
			// that names a loop nobody actually registered. Runtime
			// adds already do this at Add() time; this closes the gap
			// on YAML entries that loaded before any loop existed.
			if a.mqttSubStore != nil && a.loopRegistry != nil {
				if err := a.mqttSubStore.VerifyTargets(a.loopRegistry); err != nil {
					return fmt.Errorf("verify mqtt wake subscription targets: %w", err)
				}
			}
			return nil
		})
		a.deferWorker("loop-definition-schedule", func(ctx context.Context) error {
			return a.loopDefinitionRuntime.StartScheduleWatcher(ctx)
		})
	}
	if mqttConnectWorker != nil {
		a.deferWorker("mqtt-connect", mqttConnectWorker)
	}

	return nil
}
