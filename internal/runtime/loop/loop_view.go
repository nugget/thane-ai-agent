package loop

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// LoopView is the canonical model-facing projection of one loop — the
// "ps auxwwww" row. Every model-facing tool that returns loop data emits
// this single shape so the model reads one consistent, comprehensive row
// no matter which tool surfaced the loop.
//
// `Running` is the discriminator. For a live loop (built via FromStatus)
// every live-only group — lifecycle deltas, economics, error state,
// supervisor cadence, the effective_* inheritance lists — is populated.
// For a stored-but-not-running definition those pointer fields are nil
// and serialize as explicit JSON null (never silently dropped), so the
// model reads "running=false ⇒ counters null" as "not running", not
// "zero work". Always-present scalars stay value-typed because they carry
// meaning in both modes.
type LoopView struct {
	// ---- identity (PID / COMMAND) ----
	Name       string  `json:"name"`
	ID         *string `json:"id"`
	Operation  string  `json:"operation"`
	Completion string  `json:"completion,omitempty"`
	Task       string  `json:"task"`
	Intent     string  `json:"intent,omitempty"`

	// ---- structure (graph; resolved to names, never bare IDs) ----
	ParentName *string  `json:"parent_name"`
	Ancestry   []string `json:"ancestry"`
	ChildCount int      `json:"child_count"`

	// ---- state (STAT) ----
	Running      bool    `json:"running"`
	State        *string `json:"state"`
	PolicyState  string  `json:"policy_state"`
	PolicySource string  `json:"policy_source,omitempty"`
	PolicyReason *string `json:"policy_reason"`
	Eligible     bool    `json:"eligible"`
	EventDriven  bool    `json:"event_driven"`
	HandlerOnly  bool    `json:"handler_only"`

	// ---- lifecycle / time (delta-oriented) ----
	StartedDelta       *string `json:"started_delta"`
	LastWakeDelta      *string `json:"last_wake_delta"`
	PolicyUpdatedDelta *string `json:"policy_updated_delta"`

	// ---- economics (%CPU / %MEM / TIME) ----
	Iterations        *int `json:"iterations"`
	Attempts          *int `json:"attempts"`
	TotalInputTokens  *int `json:"total_input_tokens"`
	TotalOutputTokens *int `json:"total_output_tokens"`
	LastInputTokens   *int `json:"last_input_tokens"`
	ContextWindow     *int `json:"context_window"`
	ContextFillPct    *int `json:"context_fill_pct"`

	// ---- error state ----
	ConsecutiveErrors *int    `json:"consecutive_errors"`
	LastError         *string `json:"last_error"`

	// ---- supervisor cadence ----
	Supervisor            bool     `json:"supervisor"`
	SupervisorProb        *float64 `json:"supervisor_prob"`
	LastSupervisorIter    *int     `json:"last_supervisor_iter"`
	LastSupervisorTrigger *string  `json:"last_supervisor_trigger"`
	SupervisorItersAgo    *int     `json:"supervisor_iters_ago"`

	// ---- inheritance + provenance (live-only; [] when running-and-empty,
	// null when not running) ----
	EffectiveTags             []EffectiveTag             `json:"effective_tags"`
	EffectiveSubscriptions    []EffectiveSubscription    `json:"effective_subscriptions"`
	EffectiveExcludeTools     []EffectiveExcludeTool     `json:"effective_exclude_tools"`
	EffectiveRoutingFactors   []EffectiveRoutingFactor   `json:"effective_routing_factors"`
	EffectiveDelegationGating *EffectiveDelegationGating `json:"effective_delegation_gating"`
}

// LoopPolicyInfo is the policy/eligibility slice a LoopView needs, keyed
// by loop name and joined from the definition registry. A live Status has
// no policy of its own; the projector left-joins this so the model reads
// active/paused/eligible without a second tool call. HasPolicy=false means
// no stored definition backs the loop (a pure ad-hoc spawn) — the
// projector reports policy_state="ephemeral" rather than a misleading
// default.
type LoopPolicyInfo struct {
	State          string
	Source         string
	Reason         string
	UpdatedAt      time.Time
	Eligible       bool
	EligibleReason string
	HasPolicy      bool
}

// LoopViewResolver carries the graph and policy joins a LoopView needs so
// they resolve once per tool call (a single pass over the status batch),
// not per row. Construct one with NewLoopViewResolver, then call FromStatus
// for each loop.
type LoopViewResolver struct {
	nameByID     map[string]string
	parentByID   map[string]string
	childCount   map[string]int
	policyByName map[string]LoopPolicyInfo
	now          time.Time
}

// NewLoopViewResolver builds the id/name/parent/child indexes once from the
// full status batch (so parent_name and child_count stay accurate even when
// the displayed rows are filtered) and captures a single clock for all
// delta strings. policyByName may be nil when no definition registry is
// available; affected loops then report policy_state="ephemeral".
func NewLoopViewResolver(statuses []Status, policyByName map[string]LoopPolicyInfo, now time.Time) LoopViewResolver {
	nameByID := make(map[string]string, len(statuses))
	parentByID := make(map[string]string, len(statuses))
	childCount := make(map[string]int, len(statuses))
	for _, s := range statuses {
		nameByID[s.ID] = s.Name
		parentByID[s.ID] = s.ParentID
		if s.ParentID != "" {
			childCount[s.ParentID]++
		}
	}
	if policyByName == nil {
		policyByName = map[string]LoopPolicyInfo{}
	}
	return LoopViewResolver{
		nameByID:     nameByID,
		parentByID:   parentByID,
		childCount:   childCount,
		policyByName: policyByName,
		now:          now,
	}
}

// ancestry walks the parent chain from loopID up to the root, returning
// ancestor names ordered root→leaf. A seen-set guards against a malformed
// cycle so a corrupt graph can't spin the walk.
func (r LoopViewResolver) ancestry(loopID string) []string {
	out := []string{}
	seen := map[string]bool{loopID: true}
	for pid := r.parentByID[loopID]; pid != "" && !seen[pid]; pid = r.parentByID[pid] {
		seen[pid] = true
		name, ok := r.nameByID[pid]
		if !ok {
			break
		}
		out = append(out, name)
	}
	// Reverse to root→leaf.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// FromStatus projects a live loop Status into the canonical view, joining
// the graph (parent_name/ancestry/child_count) and the stored definition's
// policy/eligibility. Live-only fields are all populated; only the
// definition-joined policy half can be absent (ephemeral loops).
func (r LoopViewResolver) FromStatus(s Status) LoopView {
	id := s.ID
	state := string(s.State)
	iterations := s.Iterations
	attempts := s.Attempts
	consecErr := s.ConsecutiveErrors

	v := LoopView{
		Name:        s.Name,
		ID:          &id,
		Operation:   string(s.Config.Operation),
		Completion:  string(s.Config.Completion),
		Task:        s.Config.Task,
		Intent:      s.Config.Metadata["intent"],
		Ancestry:    r.ancestry(s.ID),
		ChildCount:  r.childCount[s.ID],
		Running:     true,
		State:       &state,
		EventDriven: s.EventDriven,
		HandlerOnly: s.HandlerOnly,
		Iterations:  &iterations,
		Attempts:    &attempts,
		Supervisor:  s.Config.Supervisor,

		EffectiveTags:             orEmptyLoopSlice(s.EffectiveTags),
		EffectiveSubscriptions:    orEmptyLoopSlice(s.EffectiveSubscriptions),
		EffectiveExcludeTools:     orEmptyLoopSlice(s.EffectiveExcludeTools),
		EffectiveRoutingFactors:   orEmptyLoopSlice(s.EffectiveRoutingFactors),
		EffectiveDelegationGating: s.EffectiveDelegationGating,
	}

	if pn := r.nameByID[s.ParentID]; pn != "" {
		v.ParentName = &pn
	}
	// Delta-oriented timestamps per the model-facing convention: signed
	// exact-second offsets from now (AGENTS.md), e.g. "-15120s" / "+240s".
	if !s.StartedAt.IsZero() {
		d := promptfmt.FormatDeltaOnly(s.StartedAt, r.now)
		v.StartedDelta = &d
	}
	if !s.LastWakeAt.IsZero() {
		d := promptfmt.FormatDeltaOnly(s.LastWakeAt, r.now)
		v.LastWakeDelta = &d
	}

	// Token economics — left nil for handler-only loops, which run no LLM
	// iterations and have no token metrics (a literal 0 would read as a real
	// datum, not "not applicable"). Context fill is precomputed so the model
	// never divides.
	if !s.HandlerOnly {
		totalIn := s.TotalInputTokens
		totalOut := s.TotalOutputTokens
		lastIn := s.LastInputTokens
		v.TotalInputTokens = &totalIn
		v.TotalOutputTokens = &totalOut
		v.LastInputTokens = &lastIn
		if s.ContextWindow > 0 {
			cw := s.ContextWindow
			v.ContextWindow = &cw
			if s.LastInputTokens > 0 {
				pct := s.LastInputTokens * 100 / s.ContextWindow
				v.ContextFillPct = &pct
			}
		}
	}

	v.ConsecutiveErrors = &consecErr
	// Null, not "", when there is no error — cleaner at the model boundary.
	if s.LastError != "" {
		lastErr := s.LastError
		v.LastError = &lastErr
	}

	if s.Config.Supervisor {
		prob := s.Config.SupervisorProb
		v.SupervisorProb = &prob
	}
	if s.LastSupervisorIter > 0 {
		lsi := s.LastSupervisorIter
		v.LastSupervisorIter = &lsi
		trig := string(s.LastSupervisorTrigger)
		v.LastSupervisorTrigger = &trig
		ago := s.Iterations - s.LastSupervisorIter
		if ago < 0 {
			ago = 0
		}
		v.SupervisorItersAgo = &ago
	}

	// Policy/eligibility left-joined from the stored definition.
	if pol, ok := r.policyByName[s.Name]; ok && pol.HasPolicy {
		v.PolicyState = pol.State
		v.PolicySource = pol.Source
		if pol.Reason != "" {
			reason := pol.Reason
			v.PolicyReason = &reason
		}
		if !pol.UpdatedAt.IsZero() {
			d := promptfmt.FormatDeltaOnly(pol.UpdatedAt, r.now)
			v.PolicyUpdatedDelta = &d
		}
		v.Eligible = pol.Eligible
	} else {
		// No stored definition backs this loop (ad-hoc spawn): there is no
		// policy to report. Say so explicitly rather than implying "active".
		v.PolicyState = "ephemeral"
		v.Eligible = true
	}

	return v
}

// orEmptyLoopSlice returns a non-nil empty slice for a nil input so a
// running loop with nothing to inherit serializes as `[]` (evaluated,
// empty) rather than `null` (not running / not evaluated) — resolving the
// Status Effective* nil-conflation at the model-facing boundary.
func orEmptyLoopSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
