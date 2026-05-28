package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/curator"
)

// curatorSessionCloseWakeTimeout caps how long the
// ArchiveStore.EndSession callback waits for the loop registry to
// accept the notify. The callback runs synchronously in the goroutine
// that closed the session — must not block forever if the loop
// registry is paused or shutting down.
const curatorSessionCloseWakeTimeout = 2 * time.Second

// fireCuratorSessionCloseWake constructs the session-close envelope
// and delivers it to the curator loop. Used by BOTH the real-time
// EndSession callback (wired in wireSessionCloseToCuratorWake) and
// the SummarizerWorker's backstop scan path (wired via
// SetCuratorWake). Sharing this function keeps the two delivery
// paths' envelope shape and error handling identical.
//
// Returns the wake-delivery error so callers can decide whether to
// fall back to a direct LLM call (the summarizer's behavior) or
// just log and move on (the EndSession callback's behavior).
func (a *App) fireCuratorSessionCloseWake(ctx context.Context, sessionID, reason string) error {
	env, err := buildSessionCloseEnvelope(sessionID, reason)
	if err != nil {
		return fmt.Errorf("build envelope: %w", err)
	}
	if _, err := a.loopRegistry.NotifyLoopByName(ctx, curator.DefinitionName, env); err != nil {
		return fmt.Errorf("notify curator: %w", err)
	}
	return nil
}

// wireSessionCloseToCuratorWake installs the [memory.ArchiveStore]
// session-close callback that fires a curator wake every time a
// session ends, AND the equivalent backstop on the SummarizerWorker
// scan path. The EndSession callback is real-time delivery; the
// summarizer scan catches any session whose real-time wake was
// dropped (curator down, queue full, restart during a callback).
//
// Skipped when curator.enabled is false — both paths fall back to
// their prior behavior (EndSession is a no-op, SummarizerWorker
// calls the LLM directly).
func (a *App) wireSessionCloseToCuratorWake() {
	if a == nil || a.archiveStore == nil || a.loopRegistry == nil {
		return
	}
	if a.cfg == nil || !a.cfg.Curator.Enabled {
		return
	}

	logger := a.logger

	// Real-time path: EndSession callback.
	a.archiveStore.SetSessionCloseCallback(func(sessionID, reason string) {
		ctx, cancel := context.WithTimeout(context.Background(), curatorSessionCloseWakeTimeout)
		defer cancel()
		if err := a.fireCuratorSessionCloseWake(ctx, sessionID, reason); err != nil {
			// Common case: curator is enabled in config but not yet
			// running (still starting up, or briefly stopped during
			// reconcile). The summarizer scan will catch any missed
			// wakes on its next pass; no further action needed here.
			if logger != nil {
				logger.Debug("curator session-close wake: notify failed (summarizer backstop will handle)",
					"session_id", sessionID,
					"reason", reason,
					"error", err,
				)
			}
		}
	})

	// Backstop path: summarizer scan finds anything the real-time
	// path missed and fires the wake (instead of LLM-calling itself).
	if a.summaryWorker != nil {
		a.summaryWorker.SetCuratorWake(a.fireCuratorSessionCloseWake)
	}

	if logger != nil {
		logger.Info("curator session-close wake wired",
			"loop", curator.DefinitionName,
			"timeout", curatorSessionCloseWakeTimeout,
			"backstop", a.summaryWorker != nil,
		)
	}
}

// buildSessionCloseEnvelope constructs the loop-notify envelope the
// curator receives on session close. The payload mirrors the
// event-source shape used by email and feed pollers so the curator's
// notification-summary rendering path handles it uniformly: a single
// LoopEventPayload with source="session_close", type="session_close",
// the session ID as the event ID, and the close reason as
// metadata.reason.
func buildSessionCloseEnvelope(sessionID, reason string) (messages.Envelope, error) {
	if sessionID == "" {
		return messages.Envelope{}, fmt.Errorf("empty session_id")
	}
	target := messages.LoopWakeTarget{
		Name:     curator.DefinitionName,
		Priority: messages.PriorityNormal,
		Instructions: "A session just closed. Read it (archive_session_transcript with this " +
			"session_id), then fold any new evidence into relevant dossiers. The interactive " +
			"agent has moved on — you are the bookkeeper now.",
	}
	event := messages.LoopEventPayload{
		Source:     "session_close",
		Type:       "session_close",
		ID:         sessionID,
		ObservedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"session_id": sessionID,
			"reason":     reason,
		},
	}
	return messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: "archive_store"},
		target,
		"session_close",
		[]messages.LoopEventPayload{event},
	)
}
