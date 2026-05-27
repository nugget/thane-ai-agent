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

// wireSessionCloseToCuratorWake installs the [memory.ArchiveStore]
// session-close callback that fires a curator wake every time a
// session ends. The wake carries the closed session's ID and reason
// as a structured event payload so the curator's iteration can
// dispatch on Kind=="session_close" and read the just-closed session.
//
// Skipped when curator.enabled is false — the callback would
// uselessly call NotifyLoopByName against a missing loop and log a
// warn on every session close. Skipped when archiveStore or
// loopRegistry isn't constructed yet (defensive — both should always
// be present by the time initStores reaches this hook).
//
// The summarizer worker's periodic scan remains the backstop for any
// wake that gets dropped (loop registry shutting down, curator queue
// full, callback panic absorbed by EndSession).
func (a *App) wireSessionCloseToCuratorWake() {
	if a == nil || a.archiveStore == nil || a.loopRegistry == nil {
		return
	}
	if a.cfg == nil || !a.cfg.Curator.Enabled {
		return
	}

	logger := a.logger
	registry := a.loopRegistry
	curatorName := curator.DefinitionName

	a.archiveStore.SetSessionCloseCallback(func(sessionID, reason string) {
		ctx, cancel := context.WithTimeout(context.Background(), curatorSessionCloseWakeTimeout)
		defer cancel()

		env, err := buildSessionCloseEnvelope(sessionID, reason)
		if err != nil {
			if logger != nil {
				logger.Warn("curator session-close wake: envelope build failed",
					"session_id", sessionID,
					"reason", reason,
					"error", err,
				)
			}
			return
		}

		if _, err := registry.NotifyLoopByName(ctx, curatorName, env); err != nil {
			// Common case: curator is enabled in config but not yet
			// running (still starting up, or briefly stopped during
			// reconcile). The backstop summarizer scan will pick up
			// the unsummarized session on its next pass; no further
			// action needed here. Log at Debug so steady-state noise
			// is silent but the path stays observable when needed.
			if logger != nil {
				logger.Debug("curator session-close wake: notify failed (summarizer backstop will handle)",
					"session_id", sessionID,
					"reason", reason,
					"error", err,
				)
			}
		}
	})

	if logger != nil {
		logger.Info("curator session-close wake wired",
			"loop", curatorName,
			"timeout", curatorSessionCloseWakeTimeout,
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
