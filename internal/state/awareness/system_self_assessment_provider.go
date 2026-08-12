package awareness

import (
	"context"
	"log/slog"
	"strings"
	"time"

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
// It is deliberately the seed crystal of the general pattern (every
// faceted loop's status_line in a parent's context, the process-table
// panel noted on #1341): one provider, one document, one line, so the
// generalization has a proven shape when it comes due.
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

// TagContext implements the agent context-provider contract. It parses
// the status_line facet out of the document body spec-free — the facet
// headings are the contract, so the provider needs no knowledge of the
// loop's spec — and renders it with a delta-formatted age so the reader
// can weigh a fresh verdict differently from a stale one.
func (p *SystemSelfAssessmentProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p == nil || p.read == nil {
		return "", nil
	}
	body, updatedAt, err := p.read(ctx)
	if err != nil {
		// A read failure is a health event for the metacognitive
		// subsystem, not for this turn: log it and stay quiet rather
		// than surfacing an error where a judgment was expected.
		p.logger.Warn("system self-assessment document unreadable", "error", err)
		return "", nil
	}
	payload, ok := looppkg.ParseFacetSections(body)
	if !ok {
		return "", nil
	}
	verdict, ok := payload.FacetByKey(string(looppkg.OutputFacetStatusLine))
	if !ok || strings.TrimSpace(verdict) == "" {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("### System Self-Assessment\n\n")
	sb.WriteString(strings.TrimSpace(verdict))
	if !updatedAt.IsZero() {
		sb.WriteString(" (metacognitive verdict; age_delta=")
		sb.WriteString(promptfmt.FormatDeltaOnly(updatedAt, p.now()))
		sb.WriteString(")")
	}
	sb.WriteString("\n")
	return sb.String(), nil
}
