package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/state/introspection"
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
