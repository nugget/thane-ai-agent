package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	cdav "github.com/nugget/thane-ai-agent/internal/carddav"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/metacognitive"
	"github.com/nugget/thane-ai-agent/internal/platform"
	"github.com/nugget/thane-ai-agent/internal/server/api"
	"github.com/nugget/thane-ai-agent/internal/server/web"
	"github.com/nugget/thane-ai-agent/internal/telemetry"
)

// initServers creates servers, infrastructure services, and background
// publisher loops. This covers the API server, checkpointer, OWU tracker,
// Ollama-compatible server, CardDAV, MQTT publishing, web dashboard, and
// the metacognitive loop.
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
	server.SetEventBus(a.eventBus)
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
	owuTracker, err := api.NewOWUTracker(s.ctx, a.loopRegistry, a.eventBus, a.loop, logger)
	if err != nil {
		return fmt.Errorf("create owu tracker: %w", err)
	}
	server.SetOWUTracker(owuTracker)

	// --- Ollama-compatible API server ---
	// Optional second HTTP server that speaks the Ollama wire protocol.
	// Home Assistant's Ollama integration connects here, allowing Thane
	// to serve as a drop-in replacement for a standalone Ollama instance.
	if cfg.OllamaAPI.Enabled {
		a.ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, cfg.OllamaAPI.Port, a.loop, logger)
		a.ollamaServer.SetOWUTracker(owuTracker)
	}

	// --- Platform provider endpoint ---
	// Optional: WebSocket endpoint for native platform apps (e.g. macOS)
	// to connect and register capabilities for bidirectional service dispatch.
	if cfg.Platform.Configured() {
		a.platformRegistry = platform.NewRegistry(logger)
		a.loop.Tools().EnablePlatformTools(a.platformRegistry.Call)
		handler := platform.NewHandler(cfg.Platform.TokenIndex(), a.platformRegistry, logger)
		server.SetPlatformHandler(handler)

		a.connMgr.Watch(s.ctx, connwatch.WatcherConfig{
			Name: "platform",
			Probe: func(_ context.Context) error {
				if a.platformRegistry.Count() == 0 {
					return fmt.Errorf("no providers connected")
				}
				return nil
			},
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  logger,
		})

		logger.Info("platform provider endpoint enabled")
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

		// Track the mqtt parent loop ID once it's spawned (deferred
		// worker runs after this wiring, so we use atomic.Value for
		// safe cross-goroutine access).
		var mqttParentID atomic.Value
		mqttParentID.Store("") // initialize with zero-value string
		wakeDeps := mqttWakeDeps{
			registry: a.loopRegistry,
			eventBus: a.eventBus,
			parentID: &mqttParentID,
		}

		// Wrap with the wake handler: wake-configured topics dispatch
		// agent conversations, everything else falls through to the
		// base handler above.
		mqttPub.SetMessageHandler(mqttWakeHandler(subStore, a.loop, baseMsgHandler, logger, wakeDeps))

		// Register MQTT wake subscription tools.
		mqttTools := mqtt.NewTools(subStore)
		a.loop.Tools().SetMQTTSubscriptionTools(mqttTools)

		// Defer MQTT connection, initial publish, and publisher loop
		// to StartWorkers. The publisher object and message handler are
		// already wired above; this just activates the network connection.
		a.deferWorker("mqtt-connect", func(ctx context.Context) error {
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

			mqttInterval := mqttPub.PublishInterval()
			parentID, err := a.loopRegistry.SpawnLoop(ctx, looppkg.Config{
				Name:         "mqtt",
				SleepMin:     mqttInterval,
				SleepMax:     mqttInterval,
				SleepDefault: mqttInterval,
				Jitter:       looppkg.Float64Ptr(0),
				Handler: func(ctx context.Context, _ any) error {
					mqttPub.PublishStates(ctx)
					return nil
				},
				Metadata: map[string]string{
					"subsystem": "mqtt",
					"category":  "publisher",
				},
			}, looppkg.Deps{
				Logger:   logger,
				EventBus: a.eventBus,
			})
			if err != nil {
				return fmt.Errorf("spawn mqtt loop: %w", err)
			}
			mqttParentID.Store(parentID)

			logger.Info("mqtt connected",
				"broker", cfg.MQTT.Broker,
				"device_name", cfg.MQTT.DeviceName,
				"interval", cfg.MQTT.PublishIntervalSec,
			)
			return nil
		})

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

		telInterval := time.Duration(cfg.MQTT.Telemetry.Interval) * time.Second
		telLoopCfg := looppkg.Config{
			Name:         "telemetry",
			SleepMin:     telInterval,
			SleepMax:     telInterval,
			SleepDefault: telInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Handler: func(ctx context.Context, _ any) error {
				return telPub.Publish(ctx)
			},
			Metadata: map[string]string{
				"subsystem": "mqtt",
				"category":  "telemetry",
			},
		}
		telLoopDeps := looppkg.Deps{
			Logger:   logger,
			EventBus: a.eventBus,
		}
		a.deferWorker("telemetry", func(ctx context.Context) error {
			if _, err := a.loopRegistry.SpawnLoop(ctx, telLoopCfg, telLoopDeps); err != nil {
				return fmt.Errorf("spawn telemetry loop: %w", err)
			}
			return nil
		})

		logger.Info("mqtt telemetry enabled",
			"interval", cfg.MQTT.Telemetry.Interval,
			"db_paths", len(dbPaths),
		)
	}

	// --- Web dashboard ---
	// Wire the web dashboard now that the loop registry exists.
	{
		webCfg := web.Config{
			LoopRegistry: a.loopRegistry,
			EventBus:     a.eventBus,
			SystemStatus: &systemStatusAdapter{connMgr: a.connMgr, modelRegistry: a.modelRegistry, router: a.rtr},
			Logger:       logger,
		}
		if a.indexDB != nil {
			webCfg.LogQuerier = &logQueryAdapter{db: a.indexDB}
			// Content querier is only useful when content retention is
			// enabled — without it every request detail lookup returns
			// empty, making the inspectable chips misleading.
			if cfg.Logging.RetainContent {
				webCfg.ContentQuerier = &contentQueryAdapter{db: a.indexDB}
			}
		}
		server.SetWebServer(web.NewWebServer(webCfg))
		logger.Info("cognition engine dashboard enabled", "url", fmt.Sprintf("http://localhost:%d/", cfg.Listen.Port))
	}

	// --- Metacognitive loop ---
	if cfg.Metacognitive.Enabled {
		metacogCfg, err := metacognitive.ParseConfig(cfg.Metacognitive)
		if err != nil {
			return fmt.Errorf("metacognitive config: %w", err)
		}
		a.metacogCfg = &metacogCfg

		// Resolve state file path: provenance store when configured,
		// workspace-relative otherwise. Uses filepath.Base to normalize
		// config values like "Thane/metacognitive.md" to flat layout.
		stateFileName := filepath.Base(metacogCfg.StateFile)
		var metacogStatePath string
		if a.provenanceStore != nil {
			metacogStatePath = a.provenanceStore.FilePath(stateFileName)
		} else {
			metacogStatePath = filepath.Join(cfg.Workspace.Path, metacogCfg.StateFile)
		}

		adapter := &loopAdapter{agentLoop: a.loop, router: a.rtr}
		loopSpec := metacognitive.BuildSpec(metacogCfg, metacognitive.Opts{
			WorkspacePath:   cfg.Workspace.Path,
			StateFilePath:   metacogStatePath,
			ProvenanceStore: a.provenanceStore,
			StateFileName:   stateFileName,
		})
		loopSpec.Setup = func(l *looppkg.Loop) {
			metacognitive.RegisterTools(a.loop.Tools(), l, metacogCfg, metacogStatePath, a.provenanceStore)
		}

		metacogDeps := looppkg.Deps{
			Runner:   adapter,
			Logger:   logger,
			EventBus: a.eventBus,
		}
		a.deferWorker("metacognitive", func(ctx context.Context) error {
			if _, err := a.loopRegistry.SpawnSpec(ctx, loopSpec, metacogDeps); err != nil {
				return fmt.Errorf("spawn metacognitive loop: %w", err)
			}
			return nil
		})

		logger.Info("metacognitive loop enabled",
			"state_file", cfg.Metacognitive.StateFile,
			"min_sleep", cfg.Metacognitive.MinSleep,
			"max_sleep", cfg.Metacognitive.MaxSleep,
			"supervisor_probability", cfg.Metacognitive.SupervisorProbability,
		)
	}

	return nil
}
