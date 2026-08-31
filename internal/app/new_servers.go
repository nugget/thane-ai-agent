package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/integrations/unifi"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/identity"
	"github.com/nugget/thane-ai-agent/internal/platform/telemetry"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
	"github.com/nugget/thane-ai-agent/internal/server/api"
	cdav "github.com/nugget/thane-ai-agent/internal/server/carddav"
	"github.com/nugget/thane-ai-agent/internal/server/web"
	"github.com/nugget/thane-ai-agent/internal/state/companions"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// initServers creates servers, infrastructure services, and background
// publisher loops. This covers the API server, checkpointer, OWU tracker,
// Ollama-compatible server, OpenAI-compatible server, companion app
// endpoint, CardDAV, MQTT publishing, the web dashboard, and durable
// loop-definition services.
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
	if a.archivistDefinitionEnabled() {
		contactBackfill := archivist.NewContactDossierBackfill(
			a.contactStore,
			a.archiveStore,
			a.loopQueue,
			a.opStore,
			logger,
		)
		server.ConfigureContactDossierBackfill(contactBackfill.Run)
	}
	server.UseLoopDefinitionRegistry(a.loopDefinitionRegistry)
	if a.documentStore != nil {
		server.UseDocumentReader(a.documentStore.Read)
	}
	server.ConfigureLoopDefinitionView(a.loopDefinitionView)
	server.ConfigureLoopDefinitionPersistence(a.commitLoopDefinition, a.deletePersistedLoopDefinition)
	server.ConfigureLoopDefinitionLifecycle(
		a.persistLoopDefinitionPolicy,
		a.deletePersistedLoopDefinitionPolicy,
		a.reconcileLoopDefinition,
		a.launchLoopDefinition,
	)
	server.ConfigureChatLoopLauncher(a.launchLoop)
	server.SetEventBus(a.eventBus)
	server.UseLoopRegistry(a.loopRegistry)
	if a.sched != nil {
		server.UseScheduler(a.sched)
	}
	server.UseCapabilitySurface(a.capSurfaceGetter())
	server.UseIdentityEvidence(func(ctx context.Context) (identity.Evidence, error) {
		return identity.Observe(ctx, cfg.CoreRoot(), CoreSeedSigners(cfg))
	})
	if a.indexDB != nil {
		server.UseLogQuerier(&logQueryAdapter{db: a.indexDB})
	}
	if a.cfg.Unverified() {
		server.SetUnverified(true)
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

	// Billing transitions wake the core loop once per edge; wired here
	// because initServers runs after the message bus, loop registry, and
	// model runtime all exist.
	a.wireProviderBillingAttention()

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
		a.ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, cfg.OllamaAPI.Port, cfg.OllamaAPI.APIKey, a.loop, logger)
		a.ollamaServer.SetOWUTracker(owuTracker)
	}

	// --- OpenAI-compatible API server ---
	// Optional second HTTP server that serves the frozen OpenAI shim
	// (/v1/chat/completions, /v1/models) on its own port, keeping the
	// Thane-native /v1 API on the primary port free of foreign shapes.
	if cfg.OpenAIAPI.Enabled {
		a.openaiServer = api.NewOpenAIServer(cfg.OpenAIAPI.Address, cfg.OpenAIAPI.Port, a.server, logger)
	}

	// --- Companion app endpoint ---
	// Optional: WebSocket endpoint for native companion apps (e.g. macOS)
	// to connect and register capabilities for bidirectional service dispatch.
	// Observation ingestion remains registered when companion auth is disabled
	// so the documented route reports a structured 503 instead of disappearing.
	var observationAuthenticator companion.ObservationAuthenticator
	if cfg.Companion.Configured() {
		observationAuthenticator = companion.NewBearerObservationAuthenticator(
			cfg.Companion.TokenIndex(), a.companionDevices.ResolveObservationIdentity,
		)
	}
	server.SetCompanionObservationHandler(companion.NewObservationHandler(
		observationAuthenticator,
		a.companionDevices,
		logger,
	))
	if cfg.Companion.Configured() {
		a.companionRegistry = companion.NewRegistry(logger)

		// Calendar output is rendered in the household zone, not the
		// host's, and not UTC. Already validated by config.Validate, so a
		// load failure here can only mean the zone database is missing;
		// time.Local is the honest fallback.
		companionHome := time.Local
		if cfg.Timezone != "" {
			if loc, err := time.LoadLocation(cfg.Timezone); err == nil {
				companionHome = loc
			}
		}
		a.loop.Tools().SetTimezone(cfg.Timezone)

		// Legacy floor: the hand-coded macos_calendar_events tool keeps
		// working against older Macs that advertise only methods (no
		// authored tool defs).
		a.loop.Tools().EnableCompanionTools(a.companionRegistry.Call)

		// macOS-authoritative path: synthesize model-facing tools from the
		// definitions companion apps author in register_capabilities. The
		// registrar feeds the per-run dynamic tool overlay and rebuilds
		// whenever a companion connects, re-registers, or drops — so a
		// laptop popping on/off line surfaces and retracts its tools
		// mid-session. A Mac-authored tool shadows the legacy floor by name.
		registrar := tools.NewCompanionRegistrar(a.companionRegistry, companionHome, logger)
		a.loop.SetDynamicToolSource(registrar)

		// The mechanical calendar block (#1432): an in-memory snapshot of
		// every connected account's near-term calendar, refreshed on the
		// runner's own clock and rendered into Live State every turn.
		// Wall-clock truth deliberately does not ride the advertisement
		// rail — ambient evidence loses to request-matched offers by
		// design, and today's calendar must not lose a lottery on busy
		// turns. The registry's single change callback fans out to both
		// consumers: tool synthesis rebuilds, and the snapshot refreshes
		// so a Mac reconnecting after a night away repopulates without
		// waiting out the interval.
		calendarSnapshot := companion.NewCalendarSnapshot(a.companionRegistry, companionHome, logger)
		a.companionRegistry.SetOnChange(func() {
			registrar.Rebuild()
			calendarSnapshot.NudgeRefresh()
		})
		a.loop.RegisterAlwaysContextProvider(calendarSnapshot)
		a.deferWorker("companion-calendar-snapshot", func(ctx context.Context) error {
			go calendarSnapshot.Run(ctx)
			return nil
		})

		// On companion-tagged turns, render the joined device view
		// (#1437): every paired device from the durable inventory merged
		// with live connectivity, so an iPhone that locked stays visible
		// as an offline device with honest freshness instead of
		// vanishing. Uncached live state.
		deviceContext := companions.NewContextProvider(a.companionDevices, a.companionRegistry.List, logger)

		// Counterparty attribution (#1450): each account's configured
		// contact binding resolves at read time — never copied onto
		// device rows — so a trust-zone change on the contact reaches
		// every bound device instantly. Resolution fails closed: an
		// unknown or deleted contact degrades the device to
		// account-only attribution, loudly.
		contactBindings := make(map[string]uuid.UUID, len(cfg.Companion.Providers))
		for account, provider := range cfg.Companion.Providers {
			if provider.Contact == "" {
				continue
			}
			id, err := uuid.Parse(provider.Contact)
			if err != nil {
				// Config validation rejects non-UUIDs; defensive only.
				logger.Error("companion contact binding is not a UUID", "account", account)
				continue
			}
			contactBindings[account] = id
		}
		var contactResolver companions.ContactResolver
		if len(contactBindings) > 0 && a.contactStore != nil {
			contactResolver = func(_ context.Context, account string) (companions.ContactBinding, bool) {
				id, ok := contactBindings[account]
				if !ok {
					return companions.ContactBinding{}, false
				}
				contact, err := a.contactStore.Get(id)
				if err != nil || contact == nil {
					logger.Warn("companion contact binding did not resolve",
						"account", account,
						"contact_id", id.String(),
						"error", err,
					)
					return companions.ContactBinding{}, false
				}
				return companions.ContactBinding{
					ContactID: contact.ID.String(),
					Name:      contact.FormattedName,
					TrustZone: contact.TrustZone,
				}, true
			}
			deviceContext.SetContactResolver(contactResolver)
		}
		a.loop.RegisterTagContextProvider("companion", deviceContext)

		// Server-native observation tools (#1437 slice 4): answer from
		// the durable store, so they work while every device is
		// offline — and they attribute their answers to the bound
		// counterparty (#1450).
		a.loop.Tools().EnableCompanionObservationTools(a.companionDevices, contactResolver)

		handler := companion.NewHandler(cfg.Companion.TokenIndex(), a.companionRegistry, logger)
		// Durable inventory: authentication upserts the device record,
		// disconnect stamps timestamps without deleting it (#1437).
		// The LIFO closer drains queued inventory writes before the
		// memory store closes the database they land in.
		handler.SetDeviceRecorder(a.companionDevices)
		a.onClose("companion-device-recorder", handler.CloseDeviceRecorder)
		server.SetCompanionHandler(handler)

		// Deliberately NOT a connwatch watcher: companions are phones
		// and laptops that sleep, roam, and background their apps — zero
		// connected providers is a normal state, not an integration
		// failure (#1437). The old zero-providers probe degraded
		// /health and lit a red annunciator lamp every time the last
		// device went to sleep. Reachability now lives per-device in the
		// durable inventory and the joined context view above.
		logger.Info("companion app endpoint enabled")
	}

	// Counterparty enrichment of channel context (#1450): the contact
	// block a conversation renders about its counterparty joins live
	// presence and bound companion-device reachability. The two joins
	// are independent by design — presence needs only a person tracker
	// and rides the HA person binding, so it must not require companion
	// apps to be configured — and both degrade to absence when their
	// source is missing.
	// Canonical contact-UUID → bound companion accounts, shared by the
	// channel enrichment join and the fused whereabouts tool. Config
	// may spell a binding in any form uuid.Parse accepts; lookups
	// compare against contact.ID.String().
	accountsByContact := companionAccountsByContact(cfg.Companion)

	if s.contactLookup != nil {
		if s.personTracker != nil {
			tracker := s.personTracker
			s.contactLookup.presenceFor = func(entity string) *agent.CounterpartyPresence {
				snap, ok := tracker.Snapshot(entity)
				if !ok {
					return nil
				}
				return counterpartyPresenceView(snap, time.Now())
			}
		}
		if len(accountsByContact) > 0 && a.companionDevices != nil {
			s.contactLookup.devicesFor = func(ctx context.Context, contactID string) []agent.CounterpartyDevice {
				accounts := accountsByContact[contactID]
				if len(accounts) == 0 {
					return nil
				}
				owned := make(map[string]bool, len(accounts))
				for _, acct := range accounts {
					owned[acct] = true
				}
				devices, err := a.companionDevices.List(ctx)
				if err != nil {
					logger.Warn("counterparty device join failed", "error", err)
					return nil
				}
				live := make(map[[2]string]bool)
				if a.companionRegistry != nil {
					for _, info := range a.companionRegistry.List() {
						live[[2]string{info.Account, info.ClientID}] = true
					}
				}
				now := time.Now()
				var views []agent.CounterpartyDevice
				for _, d := range devices {
					if !owned[d.Account] || d.State != companions.DeviceStateActive {
						continue
					}
					availability := "offline"
					if live[[2]string{d.Account, d.ClientID}] {
						availability = "online"
					}
					view := agent.CounterpartyDevice{
						Name:         d.ClientName,
						Platform:     d.Platform,
						Availability: availability,
					}
					if !d.LastSeenAt.IsZero() {
						view.LastSeenAgo = promptfmt.FormatDeltaOnly(d.LastSeenAt, now)
					}
					views = append(views, view)
				}
				return views
			}
		}
	}

	// Fused counterparty view (#1450 slice C): contact_whereabouts
	// composes every whereabouts source the contact record roots.
	// Registered whenever contacts exist — a presence-only household
	// without companion apps still gets the fused answer.
	if a.contactStore != nil {
		deps := tools.CounterpartyToolDeps{
			Contacts:   a.contactStore,
			Companions: a.companionDevices,
		}
		if s.personTracker != nil {
			deps.Presence = s.personTracker.Snapshot
		}
		if len(accountsByContact) > 0 {
			deps.AccountsForContact = func(contactID string) []string {
				return accountsByContact[contactID]
			}
		}
		if a.companionRegistry != nil {
			registry := a.companionRegistry
			deps.LiveIdentities = func() map[[2]string]bool {
				live := make(map[[2]string]bool)
				for _, info := range registry.List() {
					live[[2]string{info.Account, info.ClientID}] = true
				}
				return live
			}
		}
		a.loop.Tools().EnableCounterpartyTools(deps)
	}

	// --- CardDAV server ---
	// Optional: exposes the contacts store as a CardDAV address book so
	// native contact apps (macOS Contacts.app, iOS, Thunderbird) can sync.
	if cfg.CardDAV.Configured() {
		carddavBackend := cdav.NewBackend(a.contactStore, a.contactBindingsConfigOwned, logger)
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

		// MQTT wakes ride the shared loopqueue chassis (#1033):
		// ingress enqueues per-target, the WakeOnEnqueue debounce
		// coalesces bursts, and the dispatcher replays records onto
		// the message bus. Stashed on the App so the loop-definition
		// worker can run the boot recovery sweep once targets resolve.
		a.mqttWakeDispatch = newMQTTWakeDispatcher(a.loopQueue, wakeDeps, logger)

		// Wrap with the wake handler: wake-configured topics dispatch
		// agent conversations, everything else falls through to the
		// base handler above.
		mqttPub.SetMessageHandler(mqttWakeHandler(
			subStore,
			baseMsgHandler,
			logger,
			a.mqttWakeDispatch,
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
					Icon:                "mdi:access-point",
				},
			})
		}

		a.mqttPub.RegisterSensors(apSensors)

		// Route room changes from person tracker to MQTT publishes.
		s.personTracker.OnRoomChange(func(entityID, room, provider, source string) {
			// This legacy MQTT sensor specifically represents UniFi AP
			// association. Other room providers must not masquerade as AP data.
			if !shouldPublishUnifiAPRoom(provider) {
				return
			}
			shortName := entityID
			if idx := strings.IndexByte(entityID, '.'); idx >= 0 {
				shortName = entityID[idx+1:]
			}
			suffix := shortName + "_ap"

			apName := source
			if room == "" {
				apName = ""
			}
			attrs, err := json.Marshal(map[string]string{
				"ap_name":      apName,
				"provider":     provider,
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
					"entity_id", entityID, "room", room,
					"room_provider", provider, "room_source", source)
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
		}

		a.mqttPub.RegisterSensors(telBuilder.StaticSensors())

		telPub := telemetry.NewPublisher(a.telemetryCollector(), a.mqttPub, telBuilder, logger)
		a.telemetryPublisher = telPub

		logger.Info("mqtt telemetry enabled",
			"interval", cfg.MQTT.Telemetry.Interval,
			"db_paths", len(a.telemetryDBPaths()),
		)
	}

	// --- Web dashboard ---
	// Serve the embedded dashboard's static assets and wire the request-content
	// source that backs the native API's /v1/requests endpoints. The web package
	// is static-file serving only now; running-loop state and all other JSON/SSE
	// live on the native /v1 surface, so the dashboard no longer takes the loop
	// registry or event bus.
	{
		webCfg := web.Config{Logger: logger}
		// /v1/requests content source: the live request store, with the
		// retained-content DB as a fallback when content retention is on.
		var requestReader api.RequestReader
		if a.liveRequestStore != nil {
			requestReader = a.liveRequestStore
		}
		if a.indexDB != nil && cfg.Logging.RetainContent {
			requestReader = &fallbackContentQuerier{
				primary:  a.liveRequestStore,
				fallback: &contentQueryAdapter{db: a.indexDB},
			}
		}
		if requestReader != nil {
			server.UseRequestReader(requestReader)
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
			// Mirror every definition's subscriptions into the
			// awareness registry and consciously drop orphaned rows
			// (including anything the retired tag-scoped tier left
			// behind). Non-fatal: the registry rows are a projection
			// of the specs, per-spec persists keep re-projecting, and
			// the next boot re-runs the full pass — a partial compile
			// degrades visibility, not correctness.
			if err := a.compileLoopSubscriptions(); err != nil {
				logger.Warn("loop subscription compile incomplete", "error", err)
			}
			// Core is auto-created synchronously during initStores —
			// before any deferred worker runs — so default-parenting
			// works for every loop the StartEnabledServices pass
			// below registers. Idempotent if anyone calls it again.
			if err := a.ensureCoreLoop(ctx); err != nil {
				return fmt.Errorf("ensure core loop: %w", err)
			}
			if a.cfg.Unverified() {
				// Service loops act on their own schedule for as long as
				// the process lives. An instance that cannot show who
				// authorized its config should not be starting unattended
				// work; an operator who wants a specific loop can launch
				// it deliberately.
				logger.Warn("not starting service loops: config is unverified",
					"resolution", "restart on a verified config, or launch a loop explicitly")
				return nil
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
			// Drain any queued-wake partitions left pending by a
			// crash while their debounce was armed (#1033/#1211).
			// Runs here — after StartEnabledServices — so wake
			// targets resolve. The subscription feeder also compiles
			// its initial wake index now that loop-owned rows are
			// mirrored.
			if a.mqttWakeDispatch != nil {
				a.mqttWakeDispatch.Sweep(ctx)
			}
			if a.subWakeFeeder != nil {
				a.subWakeFeeder.Rebuild()
				a.subWakeFeeder.Sweep(ctx)
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
	if len(a.docRootSyncers) > 0 {
		a.deferWorker("docroot-sync", func(ctx context.Context) error {
			for _, syncer := range a.docRootSyncers {
				go syncer.Run(ctx)
			}
			return nil
		})
	}
	if mqttConnectWorker != nil {
		a.deferWorker("mqtt-connect", mqttConnectWorker)
	}

	return nil
}

// shouldPublishUnifiAPRoom keeps the legacy AP sensor scoped to observation
// changes and withdrawals from the UniFi provider.
func shouldPublishUnifiAPRoom(provider string) bool {
	return provider == unifi.RoomProvider
}

// counterpartyPresenceView renders the contact-context presence join. The
// tracker clears rooms on not_home but can retain one across a direct home to
// named-zone transition, so room detail and its provenance are truthful only
// while the person state is home.
func counterpartyPresenceView(snap contacts.PersonSnapshot, now time.Time) *agent.CounterpartyPresence {
	view := &agent.CounterpartyPresence{State: snap.State}
	if !snap.Since.IsZero() {
		view.Since = promptfmt.FormatDeltaOnly(snap.Since, now)
	}
	if strings.EqualFold(snap.State, "home") {
		view.RoomConflict = snap.RoomConflict
		if !snap.RoomConflict && snap.Room != "" {
			view.Room = snap.Room
			view.RoomProvider = snap.RoomProvider
			view.RoomSource = snap.RoomSource
			if !snap.RoomSince.IsZero() {
				view.RoomSince = promptfmt.FormatDeltaOnly(snap.RoomSince, now)
			}
		}
	}
	return view
}

// companionAccountsByContact exposes counterparty bindings only while the
// companion source is configured. Provider entries may remain in disabled
// config, but they must not keep persisted companion data reachable through
// contact joins after the operator disables the integration.
func companionAccountsByContact(cfg config.CompanionConfig) map[string][]string {
	if !cfg.Configured() {
		return nil
	}
	accountsByContact := make(map[string][]string)
	for account, provider := range cfg.Providers {
		if provider.Contact == "" {
			continue
		}
		if id, err := uuid.Parse(provider.Contact); err == nil {
			canonicalID := id.String()
			accountsByContact[canonicalID] = append(accountsByContact[canonicalID], account)
		}
	}
	return accountsByContact
}
