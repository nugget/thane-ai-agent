package introspection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/phasetrace"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// DocFlagsFunc reports the currently flagged (runaway) documents across
// the revision-backed roots. The panel renders flags only — the full
// churn report stays behind the doc_activity tool.
type DocFlagsFunc func(ctx context.Context) ([]documents.DocumentActivity, error)

// NewDocFlags builds the panel's flagged-document probe over the
// document store: a trailing-day Activity sweep of every
// revision-backed root, keeping only flagged rows. The store's
// mtime pre-filter keeps quiet roots free of git subprocesses.
func NewDocFlags(store *documents.Store) DocFlagsFunc {
	if store == nil {
		return nil
	}
	return func(ctx context.Context) ([]documents.DocumentActivity, error) {
		defer phasetrace.Phase(ctx, "doc_flags")()
		roots := store.RevisionBackedRoots()
		if len(roots) == 0 {
			return nil, nil
		}
		// Activity opens with Refresh, and Refresh walks every indexed
		// root rather than the one being asked about. Left implicit,
		// that whole-corpus cost is charged to whichever root sorts
		// first, and the per-root phases below name the wrong root.
		// Pay it here under its own name; the store's throttle then
		// holds the Activity calls to the work their labels claim.
		refreshDone := phasetrace.Phase(ctx, "doc_flags:refresh")
		err := store.Refresh(ctx)
		refreshDone()
		if err != nil {
			return nil, fmt.Errorf("refresh document index: %w", err)
		}
		var flagged []documents.DocumentActivity
		for _, root := range roots {
			// Per root as well as in total: one expensive root among a
			// dozen is invisible in an aggregate.
			rootDone := phasetrace.Phase(ctx, "doc_flags:"+root)
			report, err := store.Activity(ctx, documents.ActivityQuery{
				Root:  root,
				Since: time.Now().Add(-24 * time.Hour),
			})
			rootDone()
			if err != nil {
				return nil, fmt.Errorf("root %s: %w", root, err)
			}
			for _, doc := range report.Documents {
				if doc.Flagged {
					flagged = append(flagged, doc)
				}
			}
		}
		return flagged, nil
	}
}

// maxPanelDocFlags caps the flagged-document list on the panel.
const maxPanelDocFlags = 5

// maxSummaryDegradedNames caps how many degraded rows the summary line
// names before folding the rest into a "+N more" marker.
const maxSummaryDegradedNames = 8

// panelSoftMaxBytes is the point past which the panel drops its
// healthy annunciator rows and keeps only the not-ok ones, marked
// explicitly. Well under the 64KB context-bucket cap — the panel is
// ambient perception, not a report.
const panelSoftMaxBytes = 8 * 1024

// PanelProvider injects the internal-operations panel into every
// iteration whose active tags include the one it is registered under
// (the metacognitive loop's own tag). It renders the same flat payload
// the system_health tool returns (snapshotPayload), so the panel and
// the tool can never disagree in fact or in shape; the tools are the
// drill-down, the panel is the ambient perception that makes each
// fresh iteration start informed.
type PanelProvider struct {
	inspector *Inspector
	docFlags  DocFlagsFunc
	logger    *slog.Logger
}

// NewPanelProvider builds the panel provider. docFlags may be nil when
// no revision-backed roots exist.
func NewPanelProvider(inspector *Inspector, docFlags DocFlagsFunc, logger *slog.Logger) *PanelProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &PanelProvider{inspector: inspector, docFlags: docFlags, logger: logger}
}

// TagContextBucket places the panel under live state — it is the
// current condition of the runtime, re-read every iteration.
func (p *PanelProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// TagContext renders the panel. It never errors the turn: a failed
// probe becomes a field in the payload, because a broken probe is
// exactly the kind of fact this panel exists to surface.
func (p *PanelProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p == nil || p.inspector == nil {
		return "", nil
	}
	healthDone := phasetrace.Phase(ctx, "health")
	snap := p.inspector.Health(ctx)
	healthDone()

	// One projection for both surfaces: the panel body is exactly what
	// system_health returns, plus the panel-only additions below.
	payload := snapshotPayload(snap)
	degraded := snap.Degraded()

	if p.docFlags != nil {
		flags, err := p.docFlags(ctx)
		switch {
		case err != nil:
			payload["flagged_documents_error"] = err.Error()
		case len(flags) > maxPanelDocFlags:
			payload["flagged_documents"] = flags[:maxPanelDocFlags]
			payload["flagged_documents_truncated"] = len(flags)
		case len(flags) > 0:
			payload["flagged_documents"] = flags
		}
	}

	body, err := renderPanel(payload)
	if err != nil {
		p.logger.Warn("internal operations panel render failed", "error", err)
		return "", nil
	}
	if len(body) > panelSoftMaxBytes {
		// Keep the lamps that matter: not-ok rows only, marked so the
		// model knows healthy rows were elided, never silently dropped.
		payload["annunciator"] = degraded
		payload["annunciator_truncated"] = "healthy rows elided for size; system_health returns the full panel"
		if body, err = renderPanel(payload); err != nil {
			p.logger.Warn("internal operations panel render failed", "error", err)
			return "", nil
		}
	}
	return body, nil
}

func renderPanel(payload map[string]any) (string, error) {
	blob, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("### Internal Operations Panel\n\n")
	b.WriteString("Live health of your own runtime, refreshed this iteration. This is perception, not conclusion: judge what you see against your recorded baselines, and drill in with system_health, loop_status, loop_activity, queue_status, doc_activity, logs_query, or cost_summary before escalating.\n\n")
	b.WriteString("```json\n")
	b.Write(blob)
	b.WriteString("\n```")
	return b.String(), nil
}
