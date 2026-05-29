package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
)

// sessionCloseEnqueueTimeout caps how long the ArchiveStore.EndSession
// callback waits to enqueue archivist work. The callback runs
// synchronously in the goroutine that closed the session, so it must not
// block forever if the queue store is locked or shutting down.
const sessionCloseEnqueueTimeout = 2 * time.Second

// enqueueSessionCloseWork records a closed session as a pending work item
// in the archivist's durable queue, keyed dedup on "session:<id>" so the
// same session can't pile up. It does NOT wake the archivist — the
// archivist is a self-paced consumer that drains its queue on its own
// cadence (issue #1024). This is wired into both the real-time
// EndSession callback and the summarizer's backstop scan; sharing it
// keeps the enqueue shape identical on both paths.
//
// Signature matches the summarizer's wake-callback type so it can be
// registered there unchanged.
func (a *App) enqueueSessionCloseWork(_ context.Context, sessionID, reason string) error {
	if a.loopQueue == nil {
		return fmt.Errorf("loop queue not configured")
	}
	if sessionID == "" {
		return fmt.Errorf("empty session_id")
	}
	payload := messages.LoopNotifyPayload{
		Events: []messages.LoopEventPayload{{
			Source:     "session_close",
			Type:       "session_close",
			ID:         sessionID,
			ObservedAt: time.Now().UTC(),
			Metadata: map[string]string{
				"session_id": sessionID,
				"reason":     reason,
			},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session-close payload: %w", err)
	}
	if err := a.loopQueue.Enqueue(archivist.DefinitionName, "session:"+sessionID, 0, raw); err != nil {
		return fmt.Errorf("enqueue session-close work: %w", err)
	}
	return nil
}

// wireSessionCloseToArchivistQueue installs the [memory.ArchiveStore]
// session-close callback that enqueues archivist work every time a
// session ends, AND the equivalent backstop on the SummarizerWorker scan
// path. The EndSession callback is real-time; the summarizer scan catches
// any session whose real-time enqueue was dropped (queue locked, restart
// mid-callback). Both paths enqueue — neither wakes the archivist.
//
// Skipped when archivist.enabled is false or the queue is absent: there
// is no consumer, so don't accumulate work. The summarizer still stamps
// session metadata itself either way (it no longer defers that to the
// archivist — see [memory.SummarizerWorker.summarizeSession]).
func (a *App) wireSessionCloseToArchivistQueue() {
	if a == nil || a.archiveStore == nil || a.loopQueue == nil {
		return
	}
	if a.cfg == nil || !a.cfg.Archivist.Enabled {
		return
	}

	logger := a.logger

	// Real-time path: EndSession callback.
	a.archiveStore.SetSessionCloseCallback(func(sessionID, reason string) {
		ctx, cancel := context.WithTimeout(context.Background(), sessionCloseEnqueueTimeout)
		defer cancel()
		if err := a.enqueueSessionCloseWork(ctx, sessionID, reason); err != nil {
			// The summarizer scan re-enqueues anything the real-time
			// path missed on its next pass; no further action here.
			if logger != nil {
				logger.Debug("archivist session-close enqueue failed (summarizer backstop will handle)",
					"session_id", sessionID,
					"reason", reason,
					"error", err,
				)
			}
		}
	})

	// Backstop path: summarizer scan re-enqueues anything the real-time
	// path missed. The summarizer stamps metadata regardless.
	if a.summaryWorker != nil {
		a.summaryWorker.SetArchivistEnqueue(a.enqueueSessionCloseWork)
	}

	if logger != nil {
		logger.Info("archivist session-close queue wired",
			"loop", archivist.DefinitionName,
			"timeout", sessionCloseEnqueueTimeout,
			"backstop", a.summaryWorker != nil,
		)
	}
}
