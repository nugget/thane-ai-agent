package loop

import (
	"encoding/json"
	"strings"
)

// loopSelfContext is the state half of the self-context block: the
// deliberately tight subset of [LoopView] a running loop acts on, as
// against the full canonical row an operator reads.
//
// Its keys are LoopView's keys wherever the two overlap, so a loop that
// reads its own state here and calls loop_status a moment later is
// reading one vocabulary rather than translating between two. Absent
// facts are omitted rather than emitted as null: this payload ships every
// iteration, and a running loop's own state has no "not applicable" half
// the way a stored-definition row does.
type loopSelfContext struct {
	Name      string `json:"name"`
	ID        string `json:"id,omitempty"`
	Operation string `json:"operation"`
	State     string `json:"state,omitempty"`
	Eligible  bool   `json:"eligible"`

	// Ancestry is ordered leaf-adjacent first ("watchers", "core") —
	// nearest container first, because that is the one whose policy and
	// tags bear on this loop most directly.
	Ancestry   []string `json:"ancestry,omitempty"`
	ChildCount int      `json:"child_count,omitempty"`
	Intent     string   `json:"intent,omitempty"`

	Iterations        *int    `json:"iterations,omitempty"`
	WakesLast24h      *int    `json:"wakes_last_24h,omitempty"`
	LastSleep         *string `json:"last_sleep,omitempty"`
	LastSleepPlanned  *string `json:"last_sleep_planned,omitempty"`
	NextWakeDelta     *string `json:"next_wake_delta,omitempty"`
	ConsecutiveErrors *int    `json:"consecutive_errors,omitempty"`

	// WokenEarly is the comparison of the two sleep durations, made here
	// rather than left to the reader. It means a notification arrived —
	// not that the loop's chosen cadence was overridden, which is the
	// reading it would otherwise reach for.
	WokenEarly bool `json:"woken_early,omitempty"`

	LastSupervisorDelta   *string `json:"last_supervisor_delta,omitempty"`
	SupervisorItersAgo    *int    `json:"supervisor_iters_ago,omitempty"`
	LastSupervisorTrigger *string `json:"last_supervisor_trigger,omitempty"`

	SleepEnvelope *SleepEnvelope `json:"sleep_envelope,omitempty"`
	EffectiveTags []EffectiveTag `json:"effective_tags,omitempty"`
}

// SelfContextMarkdown renders this loop's canonical view as the compact,
// always-on "self-context" block a running loop sees each iteration (#1106 B3):
// who it is, where it sits in the graph, why it exists, its live cadence and
// health, and what capability tags it inherited — so the loop is self-aware
// without a loop_status tool call. Absent fields are omitted so the block stays
// tight; a zero view renders "".
//
// The shape follows docs/model-facing-context.md: a markdown heading and
// framing note for the section boundary, a JSON payload for the live
// operational state, and prose for the one normative instruction. The
// state is emitted as typed JSON rather than the key-value prose this
// block first shipped with, so the loop reads its envelope in exactly the
// object loop_status and set_next_sleep return — one fact, one shape,
// three surfaces — instead of parsing "15m–1h" back apart on a separator
// that appears in neither bound.
//
// For a self-pacing loop the block also closes the turn (#1313). Its
// lived rhythm and the move that ends the iteration belong to the same
// decision, so they are written in the same place: the loop reads how
// often it has actually been running, how long it was just out, and how
// long since it was last reviewed, and then reads what to call and what
// range that call may name. Splitting those apart is what left the
// envelope in prose prompts that went stale against the config — the
// cadence has one home here, and the prompts reason about which end of
// it they want.
func (v LoopView) SelfContextMarkdown() string {
	if v.Name == "" {
		return ""
	}

	data, err := json.MarshalIndent(v.selfContext(), "", "  ")
	if err != nil {
		// Unreachable for this payload (no channels, funcs, or NaN), but
		// a loop that lost its self-context should not also lose the
		// instruction that ends its turn.
		data = []byte("{}")
	}

	var b strings.Builder
	b.WriteString("### This loop\n\n")
	b.WriteString("Your own runtime state for this iteration.\n\n")
	b.WriteString("```json\n")
	b.Write(data)
	b.WriteString("\n```\n")
	if closing := selfClosingLine(v); closing != "" {
		b.WriteString("\n" + closing + "\n")
	}
	return b.String()
}

// selfContext projects the view down to what the loop acts on.
func (v LoopView) selfContext() loopSelfContext {
	sc := loopSelfContext{
		Name:                  v.Name,
		Operation:             v.Operation,
		Eligible:              v.Eligible,
		Ancestry:              leafFirstAncestry(v.ParentName, v.Ancestry),
		ChildCount:            v.ChildCount,
		Intent:                v.Intent,
		Iterations:            v.Iterations,
		WakesLast24h:          v.WakesLast24h,
		LastSleep:             v.LastSleep,
		LastSleepPlanned:      v.LastSleepPlanned,
		NextWakeDelta:         v.NextWakeDelta,
		ConsecutiveErrors:     v.ConsecutiveErrors,
		LastSupervisorDelta:   v.LastSupervisorDelta,
		SupervisorItersAgo:    v.SupervisorItersAgo,
		LastSupervisorTrigger: v.LastSupervisorTrigger,
		SleepEnvelope:         v.SleepEnvelope,
		EffectiveTags:         v.EffectiveTags,
	}
	if v.ID != nil && *v.ID != "" {
		sc.ID = shortLoopID(*v.ID)
	}
	if v.State != nil {
		sc.State = *v.State
	}
	sc.WokenEarly = v.LastSleep != nil && v.LastSleepPlanned != nil && *v.LastSleep != *v.LastSleepPlanned
	return sc
}

// selfClosingLine tells a self-pacing loop how to end the turn, in the
// terms the payload above just established. This is the one part of the
// block that is an instruction rather than a fact, which is why it is
// prose outside the JSON rather than another key inside it.
//
// Rendered only for a loop that actually paces itself — an event-driven
// or one-shot loop has no sleep to choose and would read this as an
// instruction it cannot follow.
//
// It states the clamp as a clamp. A loop told only "your range is X–Y"
// still has to guess whether naming something outside it fails, is
// ignored, or is quietly moved; being moved without being told is the
// specific failure #1313 was filed about.
func selfClosingLine(v LoopView) string {
	env := v.SleepEnvelope
	if env == nil {
		return ""
	}
	return "To close this turn call set_next_sleep with a duration between " + env.Min + " and " + env.Max +
		". A duration outside that range is moved to the nearest bound rather than refused, and skipping the call entirely leaves you sleeping " + env.Default +
		". Reason about which end of the range this moment calls for; the numbers are in sleep_envelope above so you do not have to guess them."
}

// shortLoopID trims a UUID-style loop id to its first segment for legibility.
func shortLoopID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// leafFirstAncestry returns the loop's graph position nearest-container
// first. LoopView.Ancestry is ordered root→leaf, so it is reversed here;
// ParentName is the fallback when the ancestry list is empty.
func leafFirstAncestry(parentName *string, ancestry []string) []string {
	if len(ancestry) > 0 {
		rev := make([]string, len(ancestry))
		for i, n := range ancestry {
			rev[len(ancestry)-1-i] = n
		}
		return rev
	}
	if parentName != nil && *parentName != "" {
		return []string{*parentName}
	}
	return nil
}
