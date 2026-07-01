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
