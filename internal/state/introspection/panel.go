package introspection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
		var flagged []documents.DocumentActivity
		for _, root := range store.RevisionBackedRoots() {
			report, err := store.Activity(ctx, documents.ActivityQuery{
				Root:  root,
				Since: time.Now().Add(-24 * time.Hour),
			})
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
// (the metacognitive loop's own tag). It renders from the same
// Inspector the system_health tool uses, so the panel and the tool can
// never disagree; the tools are the drill-down, the panel is the
// ambient perception that makes each fresh iteration start informed.
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
	snap := p.inspector.Health(ctx)

	payload := map[string]any{
		"annunciator": snap.Annunciator,
		"version":     snap.Version,
		"host":        snap.Host,
		"loops":       snap.Loops,
		"telemetry":   snap.Telemetry,
	}
	if len(snap.Queues) > 0 {
		payload["queues"] = snap.Queues
	}
	degraded := snap.Degraded()
	if len(degraded) == 0 {
		payload["summary"] = fmt.Sprintf("all %d annunciator rows ok", len(snap.Annunciator))
	} else {
		// The summary is a headline, not the list: cap the named rows so
		// a mass outage cannot balloon the panel past its soft cap (and
		// past the context bucket's own truncator, which would cut the
		// fenced JSON mid-payload).
		names := make([]string, 0, min(len(degraded), maxSummaryDegradedNames))
		for _, row := range degraded[:min(len(degraded), maxSummaryDegradedNames)] {
			names = append(names, row.Name)
		}
		summary := fmt.Sprintf("%d of %d annunciator rows not ok: %s", len(degraded), len(snap.Annunciator), strings.Join(names, ", "))
		if extra := len(degraded) - len(names); extra > 0 {
			summary += fmt.Sprintf(" (+%d more)", extra)
		}
		payload["summary"] = summary
	}

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
