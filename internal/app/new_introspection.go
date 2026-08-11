package app

import (
	"context"
	"path/filepath"
	"time"

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

	// Reuse the log index's retention when configured; the journal's own
	// default (30d) otherwise. Pure age-based either way.
	retention := a.cfg.Logging.RetentionDaysDuration()
	recorder := introspection.NewRecorder(journal, a.eventBus, retention, a.logger)
	a.deferWorker("loop-event-recorder", func(ctx context.Context) error {
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

// initInspector wires the health inspector — the single assembler
// behind the system_health tool and the metacog context panel. Every
// source closure is nil-safe and reads live App state at call time, so
// components that come up later (the memory guard is stored in Serve)
// or never (no remote doc roots) degrade to absent rows rather than
// wiring errors. The telemetry collector here is deliberately separate
// from the MQTT publisher's: that one only exists when MQTT telemetry
// is enabled, and health must not depend on MQTT.
func (a *App) initInspector() {
	telSources := telemetry.Sources{
		LoopRegistry: a.loopRegistry,
		UsageStore:   a.usageStore,
		ArchiveStore: a.archiveStore,
		LogsDB:       a.indexDB,
		DBPaths:      a.telemetryDBPaths(),
		Logger:       a.logger,
	}
	if a.attachmentStore != nil {
		telSources.AttachmentSource = a.attachmentStore
	}

	src := introspection.HealthSources{
		Telemetry: telemetry.NewCollector(telSources),
		DataDir:   a.cfg.DataDir,
		StartedAt: time.Now(),
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
