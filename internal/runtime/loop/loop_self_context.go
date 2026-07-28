package loop

import (
	"fmt"
	"strings"
)

// SelfContextMarkdown renders this loop's canonical view as the compact,
// always-on "self-context" block a running loop sees each iteration (#1106 B3):
// who it is, where it sits in the graph, why it exists, its live cadence and
// health, and what capability tags it inherited — so the loop is self-aware
// without a loop_status tool call. Absent fields are omitted so the block stays
// tight; a zero view renders "".
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
	var b strings.Builder
	b.WriteString("### This loop\n")

	// Identity: name (short id) · operation · state · eligibility.
	line := v.Name
	if v.ID != nil && *v.ID != "" {
		line += " (" + shortLoopID(*v.ID) + ")"
	}
	line += " · " + v.Operation
	if v.State != nil && *v.State != "" {
		line += " · " + *v.State
	}
	if v.Eligible {
		line += " · eligible"
	} else {
		line += " · ineligible"
	}
	b.WriteString(line + "\n")

	// Structure: graph position (leaf-adjacent first) + child count if any.
	if chain := ancestryChain(v.ParentName, v.Ancestry); chain != "" {
		b.WriteString("parent: " + chain)
		if v.ChildCount > 0 {
			b.WriteString(fmt.Sprintf("  (%d children)", v.ChildCount))
		}
		b.WriteString("\n")
	}

	// Purpose.
	if v.Intent != "" {
		b.WriteString("intent: " + v.Intent + "\n")
	}

	// Live cadence & health — only the facts a self-pacing loop acts on.
	if cadence := selfCadenceLine(v); cadence != "" {
		b.WriteString(cadence + "\n")
	}
	if review := selfReviewLine(v); review != "" {
		b.WriteString(review + "\n")
	}
	if env := v.SleepEnvelope; env != nil {
		b.WriteString("sleep envelope: " + selfEnvelopeLine(env) + "\n")
	}

	// Inherited capability tags with provenance.
	if len(v.EffectiveTags) > 0 {
		b.WriteString("effective tags: " + renderSelfEffectiveTags(v.EffectiveTags) + "\n")
	}

	// The exit, last because it is what to do after everything above has
	// been read.
	if closing := selfClosingLine(v); closing != "" {
		b.WriteString("\n" + closing + "\n")
	}

	return b.String()
}

// selfEnvelopeLine renders the permitted range, the fallback, and the
// randomization as one line.
func selfEnvelopeLine(env *SleepEnvelope) string {
	line := env.Min + "–" + env.Max + " · default " + env.Default
	if env.Jitter > 0 {
		line += fmt.Sprintf(" · ±%d%% jitter", int(env.Jitter*100))
	}
	return line
}

// selfClosingLine tells a self-pacing loop how to end the turn, in the
// terms the lines above just established. Rendered only for a loop that
// actually paces itself — an event-driven or one-shot loop has no sleep
// to choose and would read this as an instruction it cannot follow.
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
	return "To close this turn call set_next_sleep with a duration in " + env.Min + "–" + env.Max +
		". A duration outside that range is moved to the nearest bound rather than refused, and skipping the call entirely leaves you sleeping " + env.Default +
		". Reason about which end of the range this moment calls for; the numbers are here so you do not have to guess them."
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

// ancestryChain renders the loop's graph position leaf-adjacent first
// ("watchers ← core"). Ancestry is ordered root→leaf, so it is reversed here;
// ParentName is the fallback when the ancestry list is empty.
func ancestryChain(parentName *string, ancestry []string) string {
	if len(ancestry) > 0 {
		rev := make([]string, len(ancestry))
		for i, n := range ancestry {
			rev[len(ancestry)-1-i] = n
		}
		return strings.Join(rev, " ← ")
	}
	if parentName != nil && *parentName != "" {
		return *parentName
	}
	return ""
}

// selfCadenceLine renders the rhythm the loop has actually been keeping:
// where it is in its life, how often it has run lately, and how long it
// was just out. A lifetime iteration count alone cannot answer "am I
// running too often" on a loop whose interval it chose differently every
// time; the trailing-day count can.
func selfCadenceLine(v LoopView) string {
	var parts []string
	if v.Iterations != nil {
		parts = append(parts, fmt.Sprintf("iteration %d", *v.Iterations))
	}
	if v.WakesLast24h != nil {
		parts = append(parts, fmt.Sprintf("%s in the last 24h", pluralTurns(*v.WakesLast24h)))
	}
	if slept := selfSleptPhrase(v); slept != "" {
		parts = append(parts, slept)
	}
	if v.NextWakeDelta != nil {
		parts = append(parts, "next wake "+*v.NextWakeDelta)
	}
	if v.ConsecutiveErrors != nil {
		parts = append(parts, fmt.Sprintf("consecutive_errors %d", *v.ConsecutiveErrors))
	}
	return strings.Join(parts, " · ")
}

// selfSleptPhrase describes the sleep that just ended. When a
// notification cut it short both durations are named, because "I asked
// for 30m and ran after 4m" means something woke the loop — not that its
// choice was overridden, which is the reading it would otherwise reach
// for.
func selfSleptPhrase(v LoopView) string {
	if v.LastSleep == nil {
		return ""
	}
	if v.LastSleepPlanned != nil && *v.LastSleepPlanned != *v.LastSleep {
		return "woken after " + *v.LastSleep + " of a planned " + *v.LastSleepPlanned
	}
	return "asleep " + *v.LastSleep + " before this turn"
}

// selfReviewLine reports the most recent supervisor pass in both units
// that matter: how long ago it was, and how many turns back. A loop
// choosing its cadence uses the first; a loop judging whether its own
// recent work has been reviewed uses the second.
func selfReviewLine(v LoopView) string {
	if v.LastSupervisorDelta == nil && v.SupervisorItersAgo == nil {
		return ""
	}
	line := "last supervisor review"
	if v.LastSupervisorDelta != nil {
		line += " " + *v.LastSupervisorDelta
	}
	var detail []string
	if v.LastSupervisorTrigger != nil && *v.LastSupervisorTrigger != "" {
		detail = append(detail, *v.LastSupervisorTrigger)
	}
	if v.SupervisorItersAgo != nil {
		detail = append(detail, fmt.Sprintf("%s back", pluralTurns(*v.SupervisorItersAgo)))
	}
	if len(detail) > 0 {
		line += " (" + strings.Join(detail, ", ") + ")"
	}
	return line
}

func pluralTurns(n int) string {
	if n == 1 {
		return "1 turn"
	}
	return fmt.Sprintf("%d turns", n)
}

func renderSelfEffectiveTags(tags []EffectiveTag) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.From == "" || t.From == EffectiveOriginSelf {
			parts = append(parts, t.Tag+" (self)")
		} else {
			parts = append(parts, t.Tag+" (←"+t.From+")")
		}
	}
	return strings.Join(parts, ", ")
}
