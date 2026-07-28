package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeSelfContext pulls the JSON payload back out of the rendered
// block. Asserting on the decoded object rather than on substrings is the
// point of the block being JSON: a test that matched prose would pass on
// a payload the model could not parse.
func decodeSelfContext(t *testing.T, block string) map[string]any {
	t.Helper()
	start := strings.Index(block, "```json\n")
	if start < 0 {
		t.Fatalf("block carries no json payload:\n%s", block)
	}
	body := block[start+len("```json\n"):]
	end := strings.Index(body, "\n```")
	if end < 0 {
		t.Fatalf("json payload is unterminated:\n%s", block)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body[:end]), &out); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, body[:end])
	}
	return out
}

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
	if !strings.HasPrefix(got, "### This loop\n") {
		t.Errorf("block should open with its section heading:\n%s", got)
	}

	payload := decodeSelfContext(t, got)
	if payload["name"] != "reservoir_watch" || payload["operation"] != "service" || payload["state"] != "sleeping" {
		t.Errorf("identity = %#v", payload)
	}
	// Short id: the full UUID costs context every iteration and the first
	// segment is enough to correlate against a log line.
	if payload["id"] != "019f16aa" {
		t.Errorf("id = %v, want the shortened 019f16aa", payload["id"])
	}
	if payload["eligible"] != true {
		t.Errorf("eligible = %v, want true", payload["eligible"])
	}
	// Nearest container first — the one whose policy bears most directly
	// on this loop.
	if ancestry, _ := payload["ancestry"].([]any); len(ancestry) != 2 || ancestry[0] != "watchers" || ancestry[1] != "core" {
		t.Errorf("ancestry = %v, want [watchers core]", payload["ancestry"])
	}
	if payload["intent"] != "Keep a current read on the reservoir level" {
		t.Errorf("intent = %v", payload["intent"])
	}
	if payload["iterations"] != float64(138) || payload["next_wake_delta"] != "+5940s" {
		t.Errorf("cadence = %#v", payload)
	}
	tags, _ := payload["effective_tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("effective_tags = %v, want 2 entries", payload["effective_tags"])
	}
	first, _ := tags[0].(map[string]any)
	if first["tag"] != "home" || first["from"] != "travel" {
		t.Errorf("first tag = %#v, want home from travel", first)
	}
}

// TestLoopView_SelfContextMarkdown_OmitsAbsentFields keeps the payload
// tight. It ships every iteration, and a running loop has no
// "not applicable" half the way a stored-definition row does — so an
// absent fact is absent, not null.
func TestLoopView_SelfContextMarkdown_OmitsAbsentFields(t *testing.T) {
	v := LoopView{Name: "bare", Operation: "service", Eligible: false}
	payload := decodeSelfContext(t, v.SelfContextMarkdown())
	for _, key := range []string{"intent", "effective_tags", "iterations", "ancestry", "sleep_envelope", "last_sleep", "id", "state"} {
		if _, present := payload[key]; present {
			t.Errorf("payload should omit absent %q, got %#v", key, payload)
		}
	}
	if payload["name"] != "bare" || payload["eligible"] != false {
		t.Errorf("identity = %#v", payload)
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
	slept := "4m"
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
	payload := decodeSelfContext(t, got)

	if payload["wakes_last_24h"] != float64(47) || payload["iterations"] != float64(412) {
		t.Errorf("rhythm = %#v", payload)
	}
	if payload["last_sleep"] != "4m" || payload["last_sleep_planned"] != "30m" {
		t.Errorf("sleep durations = %#v", payload)
	}
	// The comparison is made in Go, not left to the reader.
	if payload["woken_early"] != true {
		t.Errorf("woken_early = %v, want true when the two durations disagree", payload["woken_early"])
	}
	if payload["last_supervisor_delta"] != "-4h12m" || payload["supervisor_iters_ago"] != float64(9) || payload["last_supervisor_trigger"] != "random" {
		t.Errorf("supervisor recency = %#v", payload)
	}

	// The envelope arrives as the same object loop_status and
	// set_next_sleep return, not as a range the model must split apart.
	env, _ := payload["sleep_envelope"].(map[string]any)
	if env == nil {
		t.Fatalf("sleep_envelope missing: %#v", payload)
	}
	if env["min"] != "15m" || env["max"] != "1h" || env["default"] != "30m" || env["jitter"] != 0.2 {
		t.Errorf("envelope = %#v", env)
	}

	// The one instruction stays prose, outside the payload.
	for _, w := range []string{
		"To close this turn call set_next_sleep with a duration between 15m and 1h",
		"moved to the nearest bound rather than refused",
		"leaves you sleeping 30m",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("closing line missing %q\n--- got ---\n%s", w, got)
		}
	}
}

// TestLoopView_SelfContextMarkdown_UninterruptedSleepIsNotFlaggedEarly
// keeps the flag meaningful: it fires on a notification wake, not on
// every wake.
func TestLoopView_SelfContextMarkdown_UninterruptedSleepIsNotFlaggedEarly(t *testing.T) {
	slept := "30m"
	planned := "30m"
	v := LoopView{
		Name: "metacognitive", Operation: "service", Eligible: true,
		LastSleep: &slept, LastSleepPlanned: &planned,
		SleepEnvelope: &SleepEnvelope{Min: "15m", Max: "1h", Default: "30m"},
	}
	payload := decodeSelfContext(t, v.SelfContextMarkdown())
	if _, present := payload["woken_early"]; present {
		t.Errorf("woken_early should be omitted when the sleep ran its course: %#v", payload)
	}
}

// TestLoopView_SelfContextMarkdown_NoClosingLineWithoutASleepToChoose
// keeps the trailhead off loops that cannot follow it. An event-driven
// loop told to "close this turn with set_next_sleep" would be reading an
// instruction the tool will refuse.
func TestLoopView_SelfContextMarkdown_NoClosingLineWithoutASleepToChoose(t *testing.T) {
	v := LoopView{Name: "mqtt_watch", Operation: "service", EventDriven: true, Eligible: true}
	got := v.SelfContextMarkdown()
	if strings.Contains(got, "set_next_sleep") {
		t.Errorf("event-driven loop should get no cadence trailhead\n--- got ---\n%s", got)
	}
	if payload := decodeSelfContext(t, got); payload["sleep_envelope"] != nil {
		t.Errorf("event-driven loop should carry no envelope: %#v", payload)
	}
}

// TestLoopView_SelfContextMarkdown_JitterOmittedWhenOff avoids
// advertising a ±0 spread as if it were a real one.
func TestLoopView_SelfContextMarkdown_JitterOmittedWhenOff(t *testing.T) {
	v := LoopView{
		Name: "steady", Operation: "service", Eligible: true,
		SleepEnvelope: &SleepEnvelope{Min: "15m", Max: "1h", Default: "30m"},
	}
	payload := decodeSelfContext(t, v.SelfContextMarkdown())
	env, _ := payload["sleep_envelope"].(map[string]any)
	if env == nil {
		t.Fatalf("sleep_envelope missing: %#v", payload)
	}
	if _, present := env["jitter"]; present {
		t.Errorf("jitter should be omitted when disabled: %#v", env)
	}
}
