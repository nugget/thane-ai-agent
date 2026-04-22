package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/awareness"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/notifications"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/unifi"
)

// initAwareness sets up context providers, entity watchlist, state change
// window, person tracker, UniFi room presence, and the HA state watcher.
// These components give the agent ambient awareness of the environment.
func (a *App) initAwareness(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	// --- Context providers ---
	// Dynamic system prompt injection. Providers add context based on
	// current state before each LLM call.
	contextProvider := agent.NewCompositeContextProvider()
	contextProvider.Add(agent.NewChannelProvider(&contactNameLookup{store: a.contactStore, logger: logger}))
	contextProvider.Add(awareness.NewChannelOverviewProvider(awareness.ChannelOverviewConfig{
		Loops:  &channelLoopAdapter{registry: a.loopRegistry},
		Phones: &contactPhoneResolver{store: a.contactStore},
		Hints:  tools.HintsFromContext,
		Logger: logger,
	}))

	// Notification history — injects recent sends (fire-and-forget and
	// actionable with HITL status) so agents avoid duplicate notifications.
	if a.notifRecords != nil {
		contextProvider.Add(notifications.NewHistoryProvider(notifications.HistoryProviderConfig{
			Records: a.notifRecords,
			Logger:  logger,
		}))
	}

	episodicProvider := memory.NewEpisodicProvider(a.archiveStore, logger, memory.EpisodicConfig{
		Timezone:          cfg.Timezone,
		DailyDir:          cfg.Episodic.DailyDir,
		LookbackDays:      cfg.Episodic.LookbackDays,
		HistoryTokens:     cfg.Episodic.HistoryTokens,
		SessionGapMinutes: cfg.Episodic.SessionGapMinutes,
	})
	contextProvider.Add(episodicProvider)

	wmProvider := memory.NewWorkingMemoryProvider(a.wmStore, tools.ConversationIDFromContext)
	contextProvider.Add(wmProvider)

	// --- Entity watchlist ---
	// Allows the agent to dynamically add HA entities to a watched list
	// whose live state is injected into context each turn. Persisted in
	// SQLite so the watchlist survives restarts. Shares thane.db.
	watchlistStore, err := awareness.NewWatchlistStore(a.mem.DB())
	if err != nil {
		return fmt.Errorf("watchlist store: %w", err)
	}

	if a.ha != nil {
		watchlistProvider := awareness.NewWatchlistProvider(watchlistStore, a.ha, logger)
		contextProvider.Add(watchlistProvider)

		// Register tag-scoped watchlist providers for entities added
		// with tags. Each distinct tag in the store gets a provider that
		// emits those entities only when the tag is active.
		if taggedTags, err := watchlistStore.DistinctTags(); err == nil && len(taggedTags) > 0 {
			for _, tag := range taggedTags {
				a.loop.RegisterTagContextProvider(tag,
					awareness.NewWatchlistTagProvider(tag, watchlistStore, a.ha, logger))
			}
			logger.Info("tagged watchlist entities registered",
				"tags", taggedTags)
		}

		logger.Info("entity watchlist context enabled")
	}

	a.loop.Tools().SetWatchlistStore(watchlistStore)
	a.loop.Tools().OnWatchlistTagAdded(func(tag string) {
		if a.ha == nil || strings.TrimSpace(tag) == "" {
			return
		}
		a.loop.RegisterTagContextProvider(tag,
			awareness.NewWatchlistTagProvider(tag, watchlistStore, a.ha, logger))
	})

	// --- State change window ---
	// Maintains a rolling buffer of recent HA state changes, injected
	// into the system prompt on every agent run for ambient awareness.
	stateWindowLoc := time.Local
	if cfg.Timezone != "" {
		if parsed, err := time.LoadLocation(cfg.Timezone); err == nil {
			stateWindowLoc = parsed
		}
	}
	stateWindowProvider := homeassistant.NewStateWindowProvider(
		cfg.StateWindow.MaxEntries,
		time.Duration(cfg.StateWindow.MaxAgeMinutes)*time.Minute,
		stateWindowLoc,
		logger,
	)
	contextProvider.Add(stateWindowProvider)

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
		contextProvider.Add(s.personTracker)

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
	contextProvider.Add(contacts.NewContextProvider(a.contactStore, contactEmbedder))

	// Subject-keyed fact injection — pre-warm cold-start loops with
	// facts keyed to specific entities, contacts, zones, etc.
	if cfg.Prewarm.Enabled {
		subjectProvider := knowledge.NewSubjectContextProvider(a.factStore, logger)
		if cfg.Prewarm.MaxFacts > 0 {
			subjectProvider.SetMaxFacts(cfg.Prewarm.MaxFacts)
		}
		contextProvider.Add(subjectProvider)
		logger.Info("context pre-warming enabled", "max_facts", cfg.Prewarm.MaxFacts)
	}

	// Archive retrieval injection — pre-warm cold-start loops with
	// relevant past conversation excerpts so the model has experiential
	// judgment alongside Layer 1 knowledge. See issue #404.
	if cfg.Prewarm.Enabled && cfg.Prewarm.Archive.Enabled {
		archiveProvider := memory.NewArchiveContextProvider(
			a.archiveStore,
			cfg.Prewarm.Archive.MaxResults,
			cfg.Prewarm.Archive.MaxBytes,
			logger,
		)
		contextProvider.Add(archiveProvider)
		logger.Info("archive pre-warming enabled",
			"max_results", cfg.Prewarm.Archive.MaxResults,
			"max_bytes", cfg.Prewarm.Archive.MaxBytes,
		)
	}

	a.loop.SetContextProvider(contextProvider)
	logger.Info("context providers initialized",
		"episodic_daily_dir", cfg.Episodic.DailyDir,
		"episodic_history_tokens", cfg.Episodic.HistoryTokens,
	)

	// --- State watcher ---
	// Consumes state_changed events from the HA WebSocket and forwards
	// them to the state window and person tracker. Person entity IDs
	// are auto-merged into entity globs so the person tracker receives
	// state changes regardless of the user's subscribe config.
	if a.haWS != nil {
		globs := append([]string(nil), cfg.HomeAssistant.Subscribe.EntityGlobs...)
		if s.personTracker != nil {
			globs = append(globs, s.personTracker.EntityIDs()...)
		}
		filter := homeassistant.NewEntityFilter(globs, logger)
		limiter := homeassistant.NewEntityRateLimiter(cfg.HomeAssistant.Subscribe.RateLimitPerMinute)

		// Compose handler: state window and person tracker both see
		// every state change that passes the filter and rate limiter.
		var handler homeassistant.StateWatchHandler
		if s.personTracker != nil {
			handler = func(entityID, oldState, newState string) {
				stateWindowProvider.HandleStateChange(entityID, oldState, newState)
				s.personTracker.HandleStateChange(entityID, oldState, newState)
			}
		} else {
			handler = stateWindowProvider.HandleStateChange
		}

		watcher := homeassistant.NewStateWatcher(a.haWS.Events(), filter, limiter, handler, logger)
		a.haStateWatcher = watcher
		logger.Info("state watcher configured",
			"entity_globs", globs,
			"rate_limit_per_minute", cfg.HomeAssistant.Subscribe.RateLimitPerMinute,
		)
	}

	return nil
}
