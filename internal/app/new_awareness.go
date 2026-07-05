package app

import (
	"context"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/contextfmt"
	"github.com/nugget/thane-ai-agent/internal/integrations/unifi"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// initAwareness sets up context providers, entity watchlist, state change
// window, person tracker, UniFi room presence, and the HA state watcher.
// These components give the agent ambient awareness of the environment.
func (a *App) initAwareness(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	// --- Context providers ---
	// Dynamic system prompt injection. Always-on providers add context
	// based on current state before each main-loop LLM call. Delegate
	// loops can opt out of always-on providers by setting
	// Launch.SuppressAlwaysContext = true.
	contactLookup := &contactNameLookup{store: a.contactStore, logger: logger}
	a.loop.RegisterAlwaysContextProvider(agent.NewChannelProvider(contactLookup))
	a.loop.UseContactLookup(contactLookup)
	// Self-context: inject the running loop's own canonical row each iteration
	// so it is self-aware — id/state/parent/intent/cadence/effective-tags —
	// without a loop_status tool call (#1106 B3). One provider serves every
	// non-container loop; it resolves the current loop from the iteration's
	// loop_id.
	a.loop.RegisterAlwaysContextProvider(agent.NewLoopSelfContextProvider(a.loopViewByID))
	a.loop.RegisterAlwaysContextProvider(agent.NewChannelOverviewProvider(agent.ChannelOverviewConfig{
		Loops:  &channelLoopAdapter{registry: a.loopRegistry},
		Phones: &contactPhoneResolver{store: a.contactStore},
		Hints:  tools.HintsFromContext,
		Logger: logger,
	}))

	// Notification history — injects recent sends (fire-and-forget and
	// actionable with HITL status) so agents avoid duplicate notifications.
	if a.notifRecords != nil {
		a.loop.RegisterAlwaysContextProvider(notifications.NewHistoryProvider(notifications.HistoryProviderConfig{
			Records: a.notifRecords,
			Logger:  logger,
		}))
	}

	episodicProvider := memory.NewEpisodicProvider(a.archiveStore, logger, memory.EpisodicConfig{
		Timezone:      cfg.Timezone,
		DailyDir:      cfg.Episodic.DailyDir,
		LookbackDays:  cfg.Episodic.LookbackDays,
		HistoryTokens: cfg.Episodic.HistoryTokens,
	})
	a.loop.RegisterAlwaysContextProvider(episodicProvider)

	wmProvider := memory.NewWorkingMemoryProvider(a.wmStore, tools.ConversationIDFromContext)
	a.loop.RegisterAlwaysContextProvider(wmProvider)

	// Message-channel older-sessions catalog. Gated on the
	// message_channel capability tag, asserted by Signal (and future
	// Matrix/iMessage) inbound bridges. Verbatim history is NOT
	// injected here — stored history already reaches the model as
	// role-native messages (#1160). Output sits in CONTINUITY CONTEXT
	// (uncached) per docs/prompt-caching.md — the delta timestamps
	// tick every turn so it's intrinsically uncacheable, but the
	// cached prefix above stays warm.
	messageChannelProvider := memory.NewMessageChannelProvider(
		a.archiveStore,
		tools.ConversationIDFromContext,
		memory.MessageChannelProviderConfig{},
		logger,
	)
	a.loop.RegisterTagContextProvider("message_channel", messageChannelProvider)

	// --- Entity watchlist ---
	// Allows the agent to dynamically add HA entities to a watched list
	// whose live state is injected into context each turn. Persisted in
	// SQLite so the watchlist survives restarts. Shares thane.db. The
	// store itself is constructed earlier in initStores; here we
	// register the runtime context providers that surface its rows
	// into prompts.
	watchlistStore := a.watchlistStore

	// Person-entity ingestion floor: the tracked person entities are
	// system-owned ingest subscriptions, re-seeded from person.track on
	// every boot so config edits win. Expressing the floor as registry
	// rows (instead of a hardcoded append onto the ingestion filter)
	// makes list_entity_subscriptions tell the whole truth about what
	// is being watched and ingested (#1209).
	if watchlistStore != nil {
		floor := make([]looppkg.EntitySubscription, 0, len(cfg.Person.Track))
		for _, entityID := range cfg.Person.Track {
			floor = append(floor, looppkg.EntitySubscription{
				EntityID: entityID,
				Mode:     looppkg.SubscriptionModeIngest,
			})
		}
		if err := watchlistStore.ReplaceOwner(awareness.OwnerSystem, floor); err != nil {
			// The degraded ingest-filter fallback below still feeds the
			// person tracker directly, so a failed seed loses registry
			// visibility, not presence tracking.
			logger.Warn("failed to seed the person-entity ingestion floor", "error", err)
		}
	}

	// Declared here so the state-window section below can wire itself
	// in as the transition-log retention source once it exists.
	var (
		watchlistProvider *awareness.WatchlistProvider
		loopSubProvider   *awareness.LoopSubscriptionProvider
	)
	if a.ha != nil {
		watchlistProvider = awareness.NewWatchlistProvider(watchlistStore, a.ha, logger)
		watchlistProvider.SetRegistryClient(a.ha)
		a.loop.RegisterAlwaysContextProvider(watchlistProvider)

		// One always-on provider walks the loop registry's ancestor
		// chain on each iteration to assemble effective subscriptions
		// for the current loop. Per-tag watchlist providers are gone —
		// the structural parent/child binding from container loops
		// replaces the scope_tag indirection.
		loopSubProvider = awareness.NewLoopSubscriptionProvider(a.loopRegistry, watchlistStore, a.ha, logger)
		loopSubProvider.SetRegistryClient(a.ha)
		a.loop.RegisterAlwaysContextProvider(loopSubProvider)

		logger.Info("entity watchlist context enabled")
	}

	watchlistCfg := awareness.WatchlistToolsConfig{
		Store:  watchlistStore,
		Logger: logger,
		// Owner-addressed mutations route through the same
		// spec-mutation path watch_entity uses, so a loop-owned add
		// persists the spec, patches the live loop, and re-mirrors
		// the registry in one movement.
		LoopMutator: a.mutateLoopSubscriptions,
		// The watcher is constructed later in this function; the
		// rebuild hook binds through the App field so mutations that
		// land before the watcher exists are simply no-ops.
		OnIngestChange: func() {
			if a.ingestFilterRebuild != nil {
				a.ingestFilterRebuild()
			}
		},
	}
	// Only set Registry when the HA client is present. a.ha is a concrete
	// *homeassistant.Client, so assigning a nil one into the interface
	// field would make a non-nil interface wrapping a nil pointer — the
	// preview would then dereference it instead of skipping cleanly.
	if a.ha != nil {
		watchlistCfg.Registry = a.ha
	}
	a.loop.Tools().RegisterProvider(awareness.NewWatchlistTools(watchlistCfg))

	if a.ha != nil {
		a.loop.Tools().RegisterProvider(awareness.NewAreaActivityTools(awareness.AreaActivityToolsConfig{
			Client: a.ha,
			Logger: logger,
		}))
		a.loop.Tools().RegisterProvider(awareness.NewDeviceSnapshotTools(awareness.DeviceSnapshotToolsConfig{
			Client: a.ha,
			Logger: logger,
		}))
		a.loop.Tools().RegisterProvider(awareness.NewEntityTrendTools(awareness.EntityTrendToolsConfig{
			Client: a.ha,
			Logger: logger,
		}))
		a.loop.Tools().RegisterProvider(awareness.NewHomeSnapshotTools(awareness.HomeSnapshotToolsConfig{
			Client: a.ha,
			Logger: logger,
		}))
	}

	// --- State change window ---
	// Maintains a rolling buffer of recent HA state changes, injected
	// into the system prompt on every agent run for ambient awareness.
	// contextfmt.SemanticState routes transitions through the canonical
	// class-aware projection so the window reads closed→open for a
	// garage_door, not off→on — injected here because contextfmt imports
	// the homeassistant package and the provider cannot import it back.
	stateWindowProvider := homeassistant.NewStateWindowProvider(
		cfg.StateWindow.MaxEntries,
		time.Duration(cfg.StateWindow.MaxAgeMinutes)*time.Minute,
		contextfmt.SemanticState,
		logger,
	)
	a.loop.RegisterAlwaysContextProvider(stateWindowProvider)

	// The window's per-entity retention rings back the subscription
	// transition logs (#1210): declared logs render from here, and
	// their targets reach the rings through derived capture in the
	// ingestion filter below.
	if watchlistProvider != nil {
		watchlistProvider.SetTransitionSource(stateWindowProvider)
	}
	if loopSubProvider != nil {
		loopSubProvider.SetTransitionSource(stateWindowProvider)
	}

	// --- Person tracker ---
	// Tracks configured household members' presence state and injects
	// it into the system prompt, eliminating tool calls for "who is
	// home?" queries. State updates arrive via the state watcher.
	//
	// Initialization from HA state happens in the connwatch OnReady
	// callback on each reconnect. However, if HA was already connected
	// before this tracker was constructed, OnReady would have skipped
	// initialization (personTracker was nil). Catch up immediately
	// after construction when HA is available. Initialize is idempotent,
	// so a redundant call from OnReady is harmless.
	if len(cfg.Person.Track) > 0 {
		s.personTracker = contacts.NewPresenceTracker(cfg.Person.Track, cfg.Timezone, logger)
		a.loop.RegisterAlwaysContextProvider(s.personTracker)

		// Configure device MAC addresses from config.
		for entityID, devices := range cfg.Person.Devices {
			macs := make([]string, len(devices))
			for i, d := range devices {
				macs[i] = strings.ToLower(d.MAC)
			}
			s.personTracker.SetDeviceMACs(entityID, macs)
		}

		logger.Info("person tracking enabled", "entities", cfg.Person.Track)

		if a.ha != nil {
			initCtx, initCancel := context.WithTimeout(s.ctx, 10*time.Second)
			if err := s.personTracker.Initialize(initCtx, a.ha); err != nil {
				logger.Warn("person tracker initial sync incomplete", "error", err)
			}
			initCancel()
		}
	}

	// --- UniFi room presence ---
	// Optional: polls UniFi controller for wireless client associations
	// and pushes room-level presence into the person tracker. Requires
	// both person.track and unifi config to be set.
	if cfg.Unifi.Configured() && s.personTracker != nil {
		unifiClient := unifi.NewClient(cfg.Unifi.URL, cfg.Unifi.APIKey, logger)

		// Build MAC -> entity_id mapping from config.
		deviceOwners := make(map[string]string)
		for entityID, devices := range cfg.Person.Devices {
			for _, d := range devices {
				deviceOwners[strings.ToLower(d.MAC)] = entityID
			}
		}

		pollInterval := time.Duration(cfg.Unifi.PollIntervalSec) * time.Second
		poller := unifi.NewPoller(unifi.PollerConfig{
			Locator:      unifiClient,
			Updater:      s.personTracker,
			PollInterval: pollInterval,
			DeviceOwners: deviceOwners,
			APRooms:      cfg.Person.APRooms,
			Logger:       logger,
		})
		a.unifiPoller = poller

		// Register UniFi with connwatch for health endpoint visibility.
		a.connMgr.Watch(s.ctx, connwatch.WatcherConfig{
			Name:    "unifi",
			Probe:   func(pCtx context.Context) error { return unifiClient.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  logger,
		})

		logger.Info("unifi room presence enabled",
			"url", cfg.Unifi.URL,
			"poll_interval", pollInterval,
			"tracked_macs", len(deviceOwners),
			"ap_rooms", len(cfg.Person.APRooms),
		)
	} else if cfg.Unifi.Configured() && s.personTracker == nil {
		logger.Warn("unifi configured but person tracking disabled (no person.track entries)")
	}

	// Forge account context is now injected via tag context provider
	// (registered above in capability tag setup). It appears/disappears
	// with the forge capability tag instead of being always present.

	// Contact directory context — injects relevant contacts when the
	// user message mentions people or organizations. Uses semantic
	// search when embeddings are available; no-ops gracefully otherwise.
	var contactEmbedder contacts.EmbeddingClient
	if s.embClient != nil {
		contactEmbedder = s.embClient
	}
	a.loop.RegisterAlwaysContextProvider(contacts.NewContextProvider(a.contactStore, contactEmbedder))

	// Subject-keyed fact injection — pre-warm cold-start loops with
	// facts keyed to specific entities, contacts, zones, etc.
	if cfg.Prewarm.Enabled {
		subjectProvider := knowledge.NewSubjectContextProvider(a.factStore, logger)
		if cfg.Prewarm.MaxFacts > 0 {
			subjectProvider.SetMaxFacts(cfg.Prewarm.MaxFacts)
		}
		a.loop.RegisterAlwaysContextProvider(subjectProvider)
		logger.Info("context pre-warming enabled", "max_facts", cfg.Prewarm.MaxFacts)
	}

	// Archive retrieval injection — pre-warm cold-start loops with
	// relevant past conversation excerpts so the model has experiential
	// judgment alongside Layer 1 knowledge. See issue #404. The
	// MemorySearch wrapper unifies raw-message and distilled-surface
	// retrieval into the same call so prewarm and the model-initiated
	// archive_search tool can't drift apart (see #977 Finding 2).
	if cfg.Prewarm.Enabled && cfg.Prewarm.Archive.Enabled {
		searcher := memory.NewMemorySearch(a.archiveStore, a.wmStore, logger)
		archiveProvider := memory.NewArchiveContextProvider(
			searcher,
			cfg.Prewarm.Archive.MaxResults,
			cfg.Prewarm.Archive.MaxBytes,
			logger,
		)
		a.loop.RegisterAlwaysContextProvider(archiveProvider)
		logger.Info("archive pre-warming enabled",
			"max_results", cfg.Prewarm.Archive.MaxResults,
			"max_bytes", cfg.Prewarm.Archive.MaxBytes,
		)
	}

	logger.Info("context providers initialized",
		"episodic_daily_dir", cfg.Episodic.DailyDir,
		"episodic_history_tokens", cfg.Episodic.HistoryTokens,
	)

	// --- Subscription wake feed (#1211) ---
	// Wake-declaring subscriptions awaken their owning loop on entity
	// change: the feeder taps the filtered state stream below,
	// enqueues entity-deduped records per owner, and the shared
	// queued-wake dispatcher's debounce delivers coalesced batches.
	if a.loopQueue != nil && a.messageBus != nil && watchlistStore != nil {
		a.subWakeFeeder = newSubscriptionWakeFeeder(
			a.loopQueue, a.messageBus, watchlistStore, a.loopRegistry,
			contextfmt.SemanticState, logger,
		)
	}

	// --- State watcher ---
	// Consumes state_changed events from the HA WebSocket and forwards
	// them to the state window and person tracker. The ingestion filter
	// is a dynamic registry (#1192): ingest/both-mode subscription rows
	// from every owner, rebuilt on every registry mutation — no HA
	// re-subscription needed, the WS feed is a firehose gated
	// client-side. The person floor arrives through the registry too
	// (system-owned rows seeded above), not a hardcoded append.
	if a.haWS != nil {
		buildIngestFilter := func() (*homeassistant.EntityFilter, error) {
			globs, err := watchlistStore.IngestGlobs(time.Now())
			if err != nil {
				return nil, err
			}
			if len(globs) == 0 {
				// Nothing registered must mean ingest nothing — an empty
				// EntityFilter would mean ingest everything.
				return homeassistant.NewEntityFilterMatchNone(logger), nil
			}
			return homeassistant.NewEntityFilter(globs, logger), nil
		}
		filter, err := buildIngestFilter()
		if err != nil {
			// A failed registry read at boot degrades to the person
			// floor — the tracker must not go deaf — and says so.
			logger.Warn("ingest registry read failed at startup; ingesting the person-entity floor only", "error", err)
			var personGlobs []string
			if s.personTracker != nil {
				personGlobs = s.personTracker.EntityIDs()
			}
			if len(personGlobs) == 0 {
				filter = homeassistant.NewEntityFilterMatchNone(logger)
			} else {
				filter = homeassistant.NewEntityFilter(personGlobs, logger)
			}
		}
		limiter := homeassistant.NewEntityRateLimiter(cfg.HomeAssistant.IngestRateLimitPerMinute)

		// Compose handler: the state window, person tracker, and
		// subscription wake feeder all see every state change that
		// passes the filter and rate limiter.
		taps := []homeassistant.StateWatchHandler{stateWindowProvider.HandleStateChange}
		if s.personTracker != nil {
			taps = append(taps, s.personTracker.HandleStateChange)
		}
		if a.subWakeFeeder != nil {
			taps = append(taps, a.subWakeFeeder.HandleStateChange)
		}
		handler := taps[0]
		if len(taps) > 1 {
			all := taps
			handler = func(entityID, oldState, newState, deviceClass string) {
				for _, tap := range all {
					tap(entityID, oldState, newState, deviceClass)
				}
			}
		}

		watcher := homeassistant.NewStateWatcher(a.haWS.Events(), filter, limiter, handler, logger)
		a.haStateWatcher = watcher
		a.ingestFilterRebuild = func() {
			rebuilt, err := buildIngestFilter()
			if err != nil {
				// A transient read failure keeps the previous filter —
				// genuinely, by not swapping — rather than narrowing
				// ingestion on bad luck.
				logger.Warn("ingest registry read failed; keeping the previous ingestion filter", "error", err)
			} else {
				watcher.SetFilter(rebuilt)
			}
			// The wake index derives from the same registry state, so
			// it rides the same rebuild signal (#1211).
			if a.subWakeFeeder != nil {
				a.subWakeFeeder.Rebuild()
			}
		}
		logger.Info("state watcher configured",
			"ingest_rate_limit_per_minute", cfg.HomeAssistant.IngestRateLimitPerMinute,
		)
	}

	return nil
}
