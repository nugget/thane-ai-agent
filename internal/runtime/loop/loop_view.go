package loop

import (
	"fmt"
	"strings"
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
	Name       string      `json:"name"`
	ID         *string     `json:"id"`
	Operation  string      `json:"operation"`
	Completion string      `json:"completion,omitempty"`
	Task       string      `json:"task"`
	Intent     string      `json:"intent,omitempty"`
	Origin     *OriginInfo `json:"origin,omitempty"`

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
	// PendingRetune is true while a queued retune has not yet been promoted
	// into the live config — in practice only while an in-flight turn is
	// finishing under its previous config. Omitted when false.
	PendingRetune bool `json:"pending_retune,omitempty"`

	// ---- lifecycle / time (delta-oriented) ----
	StartedDelta  *string `json:"started_delta"`
	LastWakeDelta *string `json:"last_wake_delta"`
	// NextWakeDelta is the signed-second delta to the scheduled wake while the
	// loop is in a timer-based sleep (e.g. "+5953s"); null when processing or
	// event-driven. CurrentSleepDuration is the self-paced interval it is
	// honoring this cycle, in the same seconds unit but UNSIGNED — it is a
	// duration, not a delta-from-now (e.g. "5940s"). So a freshly-started sleep
	// reads next_wake_delta≈"+5940s" / current_sleep_duration="5940s", and you
	// watch the former shrink while the latter holds.
	NextWakeDelta        *string `json:"next_wake_delta"`
	CurrentSleepDuration *string `json:"current_sleep_duration"`
	PolicyUpdatedDelta   *string `json:"policy_updated_delta"`

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
	v := LoopView{
		Name: s.Name,
		// Normalize through effectiveOperation so the canonical row reports the
		// same operation FromDefinition does (an unset operation is
		// "request_reply", never ""): the two projectors must agree field-for-
		// field on the same loop.
		Operation:  string(effectiveOperation(s.Config.Operation)),
		Completion: string(s.Config.Completion),
		Task:       s.Config.Task,
		Intent:     s.Config.Intent,
		Origin:     s.Config.Origin.Clone(),
		Ancestry:   r.ancestry(s.ID),
		ChildCount: r.childCount[s.ID],
		Supervisor: s.Config.Supervisor,
		// EventDriven is set from the live Status in applyLiveTelemetry.
	}
	if pn := r.nameByID[s.ParentID]; pn != "" {
		v.ParentName = &pn
	}
	if s.Config.Supervisor {
		prob := s.Config.SupervisorProb
		v.SupervisorProb = &prob
	}

	// Policy/eligibility left-joined from the stored definition. A live Status
	// carries no policy of its own; HasPolicy=false is an ad-hoc spawn.
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

	applyLiveTelemetry(&v, s, r.now)
	return v
}

// applyLiveTelemetry fills the live-only half of a LoopView from a running
// loop's Status: identity, runtime state, lifecycle deltas, economics, error
// state, supervisor cadence, and the effective_* inheritance lists. Shared by
// FromStatus (always live) and FromDefinition (only when the definition has a
// running loop) so both projectors emit identical live fields. It sets
// Running=true; a stored-but-not-running view never calls it and keeps every
// live-only pointer nil.
func applyLiveTelemetry(v *LoopView, s Status, now time.Time) {
	id := s.ID
	v.ID = &id
	v.Running = true
	// Normalize an empty runtime state to "running" — matches the definition
	// registry's runtime-state tally and keeps a live row from reading state:"".
	state := string(s.State)
	if state == "" {
		state = "running"
	}
	v.State = &state
	v.HandlerOnly = s.HandlerOnly
	// EventDriven is a live runtime property (Status carries l.isEventDriven()).
	// Take it from the Status so a running definition-backed row matches a
	// FromStatus row exactly instead of re-deriving it from the spec operation.
	v.EventDriven = s.EventDriven
	v.PendingRetune = s.PendingRetune

	iterations := s.Iterations
	attempts := s.Attempts
	v.Iterations = &iterations
	v.Attempts = &attempts

	// Delta-oriented timestamps per the model-facing convention: signed
	// exact-second offsets from now (AGENTS.md), e.g. "-15120s" / "+240s".
	if !s.StartedAt.IsZero() {
		d := promptfmt.FormatDeltaOnly(s.StartedAt, now)
		v.StartedDelta = &d
	}
	if !s.LastWakeAt.IsZero() {
		d := promptfmt.FormatDeltaOnly(s.LastWakeAt, now)
		v.LastWakeDelta = &d
	}
	// Scheduled next wake + the self-paced interval, for timer-based sleeps —
	// turns the census into a schedule. Event-driven loops (no timer) leave
	// SleepUntil zero and correctly report null.
	if !s.SleepUntil.IsZero() {
		nw := promptfmt.FormatDeltaOnly(s.SleepUntil, now)
		v.NextWakeDelta = &nw
		// Unsigned seconds — a duration magnitude, not a delta-from-now — so the
		// whole row speaks one unit (e.g. current_sleep "5940s" alongside
		// next_wake_delta "+5940s").
		cs := fmt.Sprintf("%ds", int64(s.CurrentSleep/time.Second))
		v.CurrentSleepDuration = &cs
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

	consecErr := s.ConsecutiveErrors
	v.ConsecutiveErrors = &consecErr
	// Null, not "", when there is no error — cleaner at the model boundary.
	if s.LastError != "" {
		lastErr := s.LastError
		v.LastError = &lastErr
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

	// effective_* come straight off the Status — the registry populates them via
	// the same ancestor walk loop_status uses. [] when running-and-empty
	// (evaluated, nothing inherited) vs null when not running.
	v.EffectiveTags = orEmptyLoopSlice(s.EffectiveTags)
	v.EffectiveSubscriptions = orEmptyLoopSlice(s.EffectiveSubscriptions)
	v.EffectiveExcludeTools = orEmptyLoopSlice(s.EffectiveExcludeTools)
	v.EffectiveRoutingFactors = orEmptyLoopSlice(s.EffectiveRoutingFactors)
	v.EffectiveDelegationGating = s.EffectiveDelegationGating
}

// DefinitionViewResolver carries the definition-graph joins a stored-definition
// LoopView needs — parent_name/ancestry/child_count resolved from
// Spec.ParentName, not live loop IDs — so they resolve once per render.
// Construct one with NewDefinitionViewResolver, then call FromDefinition for
// each definition.
type DefinitionViewResolver struct {
	parentByName map[string]string
	childCount   map[string]int
	now          time.Time
}

// NewDefinitionViewResolver builds the name/parent/child indexes once over the
// full definition snapshot set (so parent_name and child_count stay accurate
// even when the rendered rows are filtered) and captures one clock for deltas.
func NewDefinitionViewResolver(snapshots []DefinitionSnapshot, now time.Time) DefinitionViewResolver {
	parentByName := make(map[string]string, len(snapshots))
	childCount := make(map[string]int, len(snapshots))
	for _, snap := range snapshots {
		parent := strings.TrimSpace(snap.Spec.ParentName)
		parentByName[snap.Name] = parent
		if parent != "" {
			childCount[parent]++
		}
	}
	return DefinitionViewResolver{
		parentByName: parentByName,
		childCount:   childCount,
		now:          now,
	}
}

// ancestry walks the parent_name chain from a definition up to the root,
// returning ancestor names ordered root→leaf. A seen-set guards a malformed
// cycle so a corrupt graph can't spin the walk.
func (r DefinitionViewResolver) ancestry(name string) []string {
	out := []string{}
	seen := map[string]bool{name: true}
	for pn := r.parentByName[name]; pn != "" && !seen[pn]; pn = r.parentByName[pn] {
		seen[pn] = true
		out = append(out, pn)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// FromDefinition projects one stored loop definition into the canonical view —
// the definition-corpus counterpart of FromStatus. Static fields come from the
// Spec, graph fields from the definition's parent_name chain, and policy +
// eligibility from the stored snapshot (a definition, unlike a live Status,
// carries its own policy). When the definition has a running loop, pass its
// Status as live and the live-only half is overlaid, identical to a FromStatus
// row; when live is nil the row is stored-only — Running=false and every
// live-only pointer stays nil (serialized as explicit null, never zero).
func (r DefinitionViewResolver) FromDefinition(snap DefinitionSnapshot, eligibility DefinitionEligibilityStatus, live *Status) LoopView {
	spec := snap.Spec
	op := effectiveOperation(spec.Operation)
	v := LoopView{
		Name:        snap.Name,
		Operation:   string(op),
		Completion:  string(spec.Completion),
		Task:        spec.Task,
		Intent:      spec.Intent,
		Origin:      spec.Origin.Clone(),
		Ancestry:    r.ancestry(snap.Name),
		ChildCount:  r.childCount[snap.Name],
		EventDriven: op == OperationEventDriven,
		Supervisor:  spec.Supervisor,

		PolicyState:  string(snap.PolicyState),
		PolicySource: string(snap.PolicySource),
		Eligible:     eligibility.Eligible,
	}
	if pn := strings.TrimSpace(spec.ParentName); pn != "" {
		v.ParentName = &pn
	}
	if snap.PolicyReason != "" {
		reason := snap.PolicyReason
		v.PolicyReason = &reason
	}
	if !snap.PolicyUpdatedAt.IsZero() {
		d := promptfmt.FormatDeltaOnly(snap.PolicyUpdatedAt, r.now)
		v.PolicyUpdatedDelta = &d
	}
	if spec.Supervisor {
		prob := spec.SupervisorProb
		v.SupervisorProb = &prob
	}

	if live != nil {
		applyLiveTelemetry(&v, *live, r.now)
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
