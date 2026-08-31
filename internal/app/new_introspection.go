package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/memguard"
	"github.com/nugget/thane-ai-agent/internal/platform/telemetry"
	"github.com/nugget/thane-ai-agent/internal/state/introspection"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// initIntrospectionJournal wires the persistent loop-event journal: a
// bus subscriber that records loop lifecycle events (wakes with
// attribution, iteration outcomes, errors, state changes) into logs.db,
// so post-incident questions like "who kept waking this loop" survive a
// restart. Skipped quietly when logs.db or the event bus is
// unavailable — the journal is observability, never a boot dependency.
func (a *App) initIntrospectionJournal() {
	if a.indexDB == nil || a.eventBus == nil {
		return
	}
	journal, err := introspection.NewJournal(a.indexDB, a.logger)
	if err != nil {
		a.logger.Warn("loop event journal unavailable", "error", err)
		return
	}
	a.loopEventJournal = journal

	// Reuse the log index's retention only when the operator explicitly
	// set one; otherwise pass zero so the recorder applies the journal's
	// own default (30d) rather than inheriting the log index's implicit
	// 7-day fallback. Pure age-based either way.
	var retention time.Duration
	if a.cfg.Logging.RetentionDays != nil {
		retention = a.cfg.Logging.RetentionDaysDuration()
	}
	recorder := introspection.NewRecorder(journal, a.eventBus, retention, a.logger)
	a.deferWorker("loop-event-recorder", func(ctx context.Context) error {
		// One boot row per process start, stamped with build identity —
		// what makes deploy boundaries and restart storms computable.
		// Retried from its own goroutine: boot is the worst moment to
		// write to logs.db (the indexer's startup burst can outlast the
		// 5s busy timeout), and this row must not lose that race.
		journal.RecordBootWithRetry(ctx, buildinfo.Version, buildinfo.GitCommit, a.logger)
		go recorder.Run(ctx)
		return nil
	})
}

// telemetryDBPaths names the SQLite files the telemetry collector sizes.
// Shared by the MQTT telemetry publisher and the health inspector so the
// two report the same databases.
func (a *App) telemetryDBPaths() map[string]string {
	cfg := a.cfg
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
	return dbPaths
}

// telemetryCollector returns the app-wide telemetry collector, creating
// it on first use (App construction is single-threaded). The health
// inspector and the MQTT publisher share the one instance so both
// report the same numbers from one snapshot cache — the publisher's
// fresh collections warm the inspector's assembly-path reads — while
// neither owns the other's lifecycle: health works with MQTT disabled,
// and whichever wires first creates it.
func (a *App) telemetryCollector() *telemetry.Collector {
	if a.telCollector == nil {
		src := telemetry.Sources{
			LoopRegistry: a.loopRegistry,
			UsageStore:   a.usageStore,
			ArchiveStore: a.archiveStore,
			LogsDB:       a.indexDB,
			DBPaths:      a.telemetryDBPaths(),
			Logger:       a.logger,
		}
		if a.attachmentStore != nil {
			src.AttachmentSource = a.attachmentStore
		}
		a.telCollector = telemetry.NewCollector(src)
	}
	return a.telCollector
}

// initInspector wires the health inspector — the single assembler
// behind the system_health tool and the metacog context panel. Every
// source closure is nil-safe and reads live App state at call time, so
// components that come up later (the memory guard is stored in Serve)
// or never (no remote doc roots) degrade to absent rows rather than
// wiring errors.
func (a *App) initInspector() {
	src := introspection.HealthSources{
		Telemetry:    a.telemetryCollector(),
		DataDir:      a.cfg.DataDir,
		StartedAt:    time.Now(),
		BuildVersion: buildinfo.Version,
		BuildCommit:  buildinfo.GitCommit,
	}
	if a.loopEventJournal != nil {
		journal := a.loopEventJournal
		src.BootHistory = func(ctx context.Context) ([]introspection.BootRecord, error) {
			// This page feeds the visible boot tail and the
			// version-boundary walk only — boots_last_24h comes from
			// BootCountSince, so no crash storm can outrun the page.
			// 500 keeps the boundary findable across a storm of
			// same-version restarts without pretending to be a count.
			return journal.RecentBoots(ctx, 500)
		}
		src.BootCountSince = journal.CountBootsSince
	}
	if a.connMgr != nil {
		src.ConnStatus = a.connMgr.Status
	}
	src.MemGuard = func() (memguard.Reading, bool) {
		guard := a.memGuard.Load()
		if guard == nil {
			return memguard.Reading{}, false
		}
		return guard.Reading(), true
	}
	if a.eventBus != nil {
		src.BusDropped = a.eventBus.DroppedCount
	}
	if a.indexHandler != nil {
		src.IndexStats = a.indexHandler.Stats
		src.LogSeverity = a.indexHandler.SeveritySnapshot
	}
	if a.syncRegistry != nil {
		src.SyncStates = a.syncRegistry.All
	}
	if a.loopQueue != nil {
		src.QueueStats = func(ctx context.Context) ([]loopqueue.ConsumerPending, error) {
			return a.loopQueue.PendingStats(ctx)
		}
	}
	if a.loopRegistry != nil {
		src.LoopStatuses = a.loopRegistry.Statuses
	}
	a.inspector = introspection.NewInspector(src)

	// The diagnostics tool family renders from the same Inspector (and
	// journal/store/queue) the metacog context panel uses — one source
	// of truth, two surfaces.
	a.loop.Tools().RegisterProvider(introspection.NewTools(introspection.ToolDeps{
		Inspector: a.inspector,
		Journal:   a.loopEventJournal,
		Documents: a.documentStore,
		Queue:     a.loopQueue,
	}))
}
