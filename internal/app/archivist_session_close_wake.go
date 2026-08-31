package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
)

// enqueueSessionCloseWork records a closed session as a pending work item in
// the archivist's durable queue, keyed dedup on "session:<id>" so the same
// session can't pile up. It does NOT wake the archivist — the archivist is a
// self-paced consumer that drains its queue on its own cadence (issue #1024).
//
// Only sessions with conversational substance are enqueued: autonomous,
// scheduled, and auxiliary origins are filtered out by [isArchivableSession]
// so the archivist isn't drowned in automation bookkeeping. The signature
// matches the summarizer's enqueue hook, which is the single enqueue gate
// (it processes every closed session and carries the conversation origin).
func (a *App) enqueueSessionCloseWork(ctx context.Context, sessionID, conversationID, reason string) error {
	if a.loopQueue == nil {
		return fmt.Errorf("loop queue not configured")
	}
	if sessionID == "" {
		return fmt.Errorf("empty session_id")
	}
	if !archivist.IsArchivableSession(conversationID) {
		if a.logger != nil {
			a.logger.Debug("archivist: skipping non-archival session origin",
				"session_id", sessionID,
				"conversation_id", conversationID,
			)
		}
		return nil
	}
	payload := messages.LoopNotifyPayload{
		Events: []messages.LoopEventPayload{{
			Source:     "session_close",
			Type:       "session_close",
			ID:         sessionID,
			ObservedAt: time.Now().UTC(),
			Metadata: map[string]string{
				"session_id":      sessionID,
				"conversation_id": conversationID,
				"reason":          reason,
			},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session-close payload: %w", err)
	}
	if err := a.loopQueue.Enqueue(ctx, archivist.DefinitionName, "session:"+sessionID, 0, raw); err != nil {
		return fmt.Errorf("enqueue session-close work: %w", err)
	}
	return nil
}

// wireSessionCloseToArchivistQueue makes the SummarizerWorker the single gate
// that enqueues archivist work. The summarizer already processes every closed
// session (to stamp its title/tags) and has the full session context —
// including the conversation origin the archival filter keys on — so it is the
// natural and only place to decide what becomes dossier work.
//
// The former real-time EndSession callback was dropped: it saw only
// sessionID/reason, so it could not apply the origin filter, and its
// few-minutes latency advantage is irrelevant to the hourly, self-paced
// archivist. One gate, not two (issue #1024).
//
// Skipped when the archivist definition is absent or disabled, or the queue
// is missing: there is no consumer, so don't accumulate work.
func (a *App) wireSessionCloseToArchivistQueue() {
	if a == nil || a.archiveStore == nil || a.loopQueue == nil {
		return
	}
	if !a.archivistDefinitionEnabled() {
		return
	}
	if a.summaryWorker == nil {
		if a.logger != nil {
			a.logger.Warn("archivist enabled but summarizer worker absent; no session-close enqueue path")
		}
		return
	}

	a.summaryWorker.SetArchivistEnqueue(a.enqueueSessionCloseWork)

	if a.logger != nil {
		a.logger.Info("archivist session-close queue wired (summarizer gate)",
			"loop", archivist.DefinitionName,
		)
	}
}

// archivistDefinitionEnabled reports whether the archivist's effective loop
// definition exists and is enabled — the single gate for wiring the
// session-close producer. The definition document (core/loops/archivist.md,
// or the shipped default) is the same declaration that makes the loop run,
// so the producer wires exactly when a consumer exists. The legacy
// config.yaml `archivist.enabled` flag is deliberately not consulted: the
// definition made it a second switch for the same loop, and the two could
// only disagree silently — a running archivist whose queue never fills.
//
// Runtime policy (paused/inactive) is also deliberately not consulted: a
// pause is temporary, and work accumulated while paused is exactly what the
// resumed loop should drain. Only the durable declaration decides.
func (a *App) archivistDefinitionEnabled() bool {
	if a == nil {
		return false
	}
	spec, ok := a.loopDefinitionRegistry.Get(archivist.DefinitionName)
	return ok && spec.Enabled
}
