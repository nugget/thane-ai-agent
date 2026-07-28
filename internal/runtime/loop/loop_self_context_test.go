package loop

import (
	"strings"
	"testing"
)

func TestLoopView_SelfContextMarkdown_Full(t *testing.T) {
	id := "019f16aa-f878-730d-9756-dc9d8ffedb0c"
	state := "sleeping"
	iters := 138
	nextWake := "+5940s"
	consec := 0
	parent := "watchers"
	v := LoopView{
		Name: "reservoir_watch", ID: &id, Operation: "service", State: &state,
		Eligible: true, ParentName: &parent, Ancestry: []string{"core", "watchers"},
		Intent:            "Keep a current read on the reservoir level",
		Iterations:        &iters,
		NextWakeDelta:     &nextWake,
		ConsecutiveErrors: &consec,
		EffectiveTags:     []EffectiveTag{{Tag: "home", From: "travel"}, {Tag: "curate", From: EffectiveOriginSelf}},
	}

	got := v.SelfContextMarkdown()
	for _, w := range []string{
		"### This loop",
		"reservoir_watch (019f16aa) · service · sleeping · eligible",
		"parent: watchers ← core", // ancestry root→leaf, rendered leaf-adjacent first
		"intent: Keep a current read on the reservoir level",
		"iteration 138 · next wake +5940s · consecutive_errors 0",
		"effective tags: home (←travel), curate (self)",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("self-context missing %q\n--- got ---\n%s", w, got)
		}
	}
}

func TestLoopView_SelfContextMarkdown_OmitsAbsentFields(t *testing.T) {
	// No intent, no live cadence, no tags: the block must stay tight, never
	// printing an empty label.
	v := LoopView{Name: "bare", Operation: "service", Eligible: false}
	got := v.SelfContextMarkdown()
	if strings.Contains(got, "intent:") || strings.Contains(got, "effective tags:") ||
		strings.Contains(got, "iteration") || strings.Contains(got, "parent:") {
		t.Errorf("block should omit absent fields, got:\n%s", got)
	}
	if !strings.Contains(got, "bare · service · ineligible") {
		t.Errorf("identity line wrong:\n%s", got)
	}
}

func TestLoopView_SelfContextMarkdown_Empty(t *testing.T) {
	if got := (LoopView{}).SelfContextMarkdown(); got != "" {
		t.Errorf("zero view should render empty, got %q", got)
	}
}

// TestLoopView_SelfContextMarkdown_CadenceTrailhead is the #1313 shape:
// the lived rhythm and the move that closes the turn read as one block,
// because they are one decision. Before this the loop saw neither — its
// envelope lived in prose prompts that went stale against the config.
func TestLoopView_SelfContextMarkdown_CadenceTrailhead(t *testing.T) {
	iters := 412
	wakes := 47
	slept := "28m"
	planned := "30m"
	supDelta := "-4h12m"
	supAgo := 9
	trigger := "random"
	v := LoopView{
		Name: "metacognitive", Operation: "service", Eligible: true,
		Iterations:            &iters,
		WakesLast24h:          &wakes,
		LastSleep:             &slept,
		LastSleepPlanned:      &planned,
		LastSupervisorDelta:   &supDelta,
		SupervisorItersAgo:    &supAgo,
		LastSupervisorTrigger: &trigger,
		SleepEnvelope:         &SleepEnvelope{Min: "15m", Max: "1h", Default: "30m", Jitter: 0.2},
	}

	got := v.SelfContextMarkdown()
	for _, w := range []string{
		"iteration 412",
		"47 turns in the last 24h",
		// The two durations differ, so the block says it was woken rather
		// than letting "asleep 28m" read as a 28m cadence it never chose.
		"woken after 28m of a planned 30m",
		"last supervisor review -4h12m (random, 9 turns back)",
		"sleep envelope: 15m–1h · default 30m · ±20% jitter",
		"To close this turn call set_next_sleep with a duration in 15m–1h",
		"moved to the nearest bound rather than refused",
		"leaves you sleeping 30m",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("self-context missing %q\n--- got ---\n%s", w, got)
		}
	}
}

// TestLoopView_SelfContextMarkdown_UninterruptedSleepReadsPlainly keeps
// the common case terse: naming both durations when they agree would
// imply a distinction that isn't there.
func TestLoopView_SelfContextMarkdown_UninterruptedSleepReadsPlainly(t *testing.T) {
	slept := "30m"
	planned := "30m"
	v := LoopView{
		Name: "metacognitive", Operation: "service", Eligible: true,
		LastSleep: &slept, LastSleepPlanned: &planned,
		SleepEnvelope: &SleepEnvelope{Min: "15m", Max: "1h", Default: "30m"},
	}
	got := v.SelfContextMarkdown()
	if !strings.Contains(got, "asleep 30m before this turn") {
		t.Errorf("expected the plain phrasing\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "planned") {
		t.Errorf("an uninterrupted sleep should not mention a planned duration\n--- got ---\n%s", got)
	}
}

// TestLoopView_SelfContextMarkdown_NoClosingLineWithoutASleepToChoose
// keeps the trailhead off loops that cannot follow it. An event-driven
// loop told to "close this turn with set_next_sleep" would be reading an
// instruction the tool will refuse.
func TestLoopView_SelfContextMarkdown_NoClosingLineWithoutASleepToChoose(t *testing.T) {
	v := LoopView{Name: "mqtt_watch", Operation: "service", EventDriven: true, Eligible: true}
	got := v.SelfContextMarkdown()
	if strings.Contains(got, "set_next_sleep") || strings.Contains(got, "sleep envelope") {
		t.Errorf("event-driven loop should get no cadence trailhead\n--- got ---\n%s", got)
	}
}

// TestLoopView_SelfContextMarkdown_JitterOmittedWhenOff avoids
// advertising a ±0% spread as if it were a real one.
func TestLoopView_SelfContextMarkdown_JitterOmittedWhenOff(t *testing.T) {
	v := LoopView{
		Name: "steady", Operation: "service", Eligible: true,
		SleepEnvelope: &SleepEnvelope{Min: "15m", Max: "1h", Default: "30m"},
	}
	if got := v.SelfContextMarkdown(); strings.Contains(got, "jitter") {
		t.Errorf("jitter should be omitted when disabled\n--- got ---\n%s", got)
	}
}
