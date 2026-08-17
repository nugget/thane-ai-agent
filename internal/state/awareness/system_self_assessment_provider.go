package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// SystemSelfAssessmentProvider injects the metacognitive loop's
// published status_line — the one sentence in the system that is a
// judgment about the whole system — into interactive context each turn.
// system_health is the annunciator of facts; this line is the
// annunciator of judgments, and until it existed the verdict lived
// buried in a document nobody read until a request_core_attention
// fired (#1351).
//
// It is deliberately the seed crystal of the context-advertisement pattern:
// one provider offers one faceted document's compact signal, and the shared
// discriminator decides whether to materialize it. status_line and teaser
// remain distinct document shapes but share that outward-facing signal role.
//
// Quiet by design in every degraded state: no document, no facet
// sections (the document predates its faceted spec), or an empty
// status_line all render nothing rather than a placeholder — an absent
// judgment is not a judgment.
type SystemSelfAssessmentProvider struct {
	// read returns the assessed document's body and last-write time.
	// A function rather than a store handle so the provider stays
	// testable and the app can resolve the document ref lazily from
	// the loop-definition registry — the spec owns where metacog's
	// state lives, and a hardcoded ref here would drift from it.
	read   func(ctx context.Context) (body string, updatedAt time.Time, err error)
	logger *slog.Logger
	now    func() time.Time

	// Last-good verdict, served with its honest age when a live read
	// fails. The 2026-08-12 prod incident was exactly this seam: the
	// shared context budget died upstream, the read failed every turn,
	// and the one line whose absence nobody notices went silently
	// missing — degrading-toward-silent, in the production agent's own
	// words. A stale verdict wearing its age beats an absent one.
	mu            sync.Mutex
	lastVerdict   string
	lastUpdatedAt time.Time
}

// NewSystemSelfAssessmentProvider wires the provider over a document
// read function. logger may be nil (defaults to slog.Default()).
func NewSystemSelfAssessmentProvider(read func(ctx context.Context) (string, time.Time, error), logger *slog.Logger) *SystemSelfAssessmentProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &SystemSelfAssessmentProvider{read: read, logger: logger, now: time.Now}
}

// TagContextBucket places the verdict in live state: it is a current
// reading of the system, not continuity material.
func (p *SystemSelfAssessmentProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

const systemSelfAssessmentAdvertisementID = "system_self_assessment"

// ContextAdvertisements offers the metacognitive verdict as cheap ambient
// context. Advertising does not read the document; only a selected projection
// pays that cost.
func (p *SystemSelfAssessmentProvider) ContextAdvertisements(context.Context, agentctx.ContextRequest) ([]agentctx.ContextAdvertisement, error) {
	if p == nil || p.read == nil {
		return nil, nil
	}
	field, ok := looppkg.FacetFieldByKey(string(looppkg.OutputFacetStatusLine))
	if !ok {
		return nil, fmt.Errorf("status_line facet metadata is unavailable")
	}
	return []agentctx.ContextAdvertisement{{
		ID:      systemSelfAssessmentAdvertisementID,
		Source:  "metacognition",
		Kind:    "faceted_document",
		Bucket:  agentctx.ContextBucketLiveState,
		Summary: "The system's latest metacognitive verdict.",
		Matches: []agentctx.ContextMatchSignal{{
			Kind:     agentctx.ContextMatchAmbient,
			Strength: 1,
		}},
		Projections: []agentctx.ContextProjection{{
			Name:           field.Key,
			Role:           field.ContextRole,
			Format:         "text/markdown",
			EstimatedBytes: field.MaxRunes*utf8.UTFMax + 256,
		}},
	}}, nil
}

// MaterializeContextAdvertisement renders the selected metacognitive signal
// through the existing last-good and age-aware read path.
func (p *SystemSelfAssessmentProvider) MaterializeContextAdvertisement(ctx context.Context, req agentctx.ContextRequest, selection agentctx.ContextSelection) (string, error) {
	if selection.Advertisement.Source != "metacognition" || selection.Advertisement.ID != systemSelfAssessmentAdvertisementID {
		return "", fmt.Errorf("unknown metacognitive context advertisement %q/%q", selection.Advertisement.Source, selection.Advertisement.ID)
	}
	if selection.Projection.Name != string(looppkg.OutputFacetStatusLine) {
		return "", fmt.Errorf("unsupported metacognitive context projection %q", selection.Projection.Name)
	}
	return p.TagContext(ctx, req)
}

// TagContext implements the agent context-provider contract. It parses
// the status_line facet out of the document body spec-free — the facet
// headings are the contract, so the provider needs no knowledge of the
// loop's spec — and renders it with a delta-formatted age so the reader
// can weigh a fresh verdict differently from a stale one.
func (p *SystemSelfAssessmentProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p == nil || p.read == nil {
		return "", nil
	}
	// The read runs on its own bounded deadline, detached from the
	// assembler's shared budget: this provider sits at the tail of the
	// context walk, and a budget exhausted by earlier providers used to
	// arrive here already dead — the read failed before git ever ran,
	// and the failure was misattributed to this document. Detaching
	// costs at most readTimeout on a genuinely slow substrate and
	// nothing when it is healthy (single-digit milliseconds in prod).
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	body, updatedAt, err := p.read(readCtx)
	if err != nil {
		// A read failure is a health event for the metacognitive
		// subsystem, not for this turn: log it, and serve the last
		// good verdict with its honest age rather than going quiet —
		// an absent judgment reads as "nothing to say", which is not
		// what a failed read means.
		p.logger.Warn("system self-assessment document unreadable", "error", err)
		p.mu.Lock()
		verdict, at := p.lastVerdict, p.lastUpdatedAt
		p.mu.Unlock()
		if verdict == "" {
			return "", nil
		}
		return p.render(verdict, at, true), nil
	}
	payload, ok := looppkg.ParseFacetSections(body)
	if !ok {
		p.clearCache()
		return "", nil
	}
	verdict, ok := payload.FacetByKey(string(looppkg.OutputFacetStatusLine))
	if !ok || strings.TrimSpace(verdict) == "" {
		// A successful read with nothing to say is a real quiet state
		// (pre-facet document, empty verdict) — forget the cache so a
		// later failure cannot resurrect a verdict metacog withdrew.
		p.clearCache()
		return "", nil
	}
	verdict = strings.TrimSpace(verdict)
	p.mu.Lock()
	p.lastVerdict, p.lastUpdatedAt = verdict, updatedAt
	p.mu.Unlock()
	return p.render(verdict, updatedAt, false), nil
}

// readTimeout bounds the provider's detached document read. Healthy
// reads are single-digit milliseconds; the bound only matters when the
// substrate itself is degraded, and it keeps one slow read from
// stretching the turn unboundedly.
const readTimeout = 1500 * time.Millisecond

func (p *SystemSelfAssessmentProvider) clearCache() {
	p.mu.Lock()
	p.lastVerdict, p.lastUpdatedAt = "", time.Time{}
	p.mu.Unlock()
}

// render formats the verdict block. cached marks a verdict served
// because the live read failed — the age delta carries how stale it
// is, the marker carries why.
func (p *SystemSelfAssessmentProvider) render(verdict string, updatedAt time.Time, cached bool) string {
	var sb strings.Builder
	sb.WriteString("### System Self-Assessment\n\n")
	sb.WriteString(verdict)
	sb.WriteString(" (metacognitive verdict")
	if !updatedAt.IsZero() {
		sb.WriteString("; age_delta=")
		sb.WriteString(promptfmt.FormatDeltaOnly(updatedAt, p.now()))
	}
	if cached {
		sb.WriteString("; cached, live read failed")
	}
	sb.WriteString(")\n")
	return sb.String()
}
