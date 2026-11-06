package app

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// The wake fixtures characterise how Thane accounts for an unattended
// wake today, on the wake rig. Each row's expectations sit in an
// "ungated" column: today's behaviour, and permanently the behaviour of
// any loop the #1626 completion gate is not enabled for. #1626 adds a
// gated column beside it rather than editing these values. All data is
// synthetic.

// fixtureDigest is a faceted output; hydration generates its publish
// tool, real refusals and real description.
var fixtureDigest = looppkg.OutputSpec{
	Name:   "fixture_digest",
	Type:   looppkg.OutputTypeMaintainedDocument,
	Ref:    "core:fixture-digest.md",
	Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}, {Name: looppkg.OutputFacetTeaser}},
}

// Notify bodies, one per wake. They are distinct so a fixture can tell
// which wake's input reached which prompt.
const (
	notifyN1 = "Fixture source alpha reports three new items."
	notifyN2 = "Fixture source alpha reports one more item."
)

// overBudgetDigest is refused by Go validation: its status_line is 121
// runes against a 120 limit.
func overBudgetDigest() map[string]any {
	return map[string]any{
		"status_line": strings.Repeat("s", 121),
		"teaser":      "Three new items arrived.",
		"full":        "# Digest\n\nThree new items.",
	}
}

// validDigest lands a revision.
func validDigest() map[string]any {
	return map[string]any{
		"status_line": "Three new items.",
		"teaser":      "Three new items arrived from source alpha.",
		"full":        "# Digest\n\nThree new items from source alpha.",
	}
}

// tally is the slice of a tool's ledger entry a fixture pins.
type tally struct{ calls, successes, failures int }

// rowTally counts a tool's tool_calls rows and how many carry an error.
type rowTally struct{ rows, errors int }

// wakeStep is one wake: the notification that wakes it and the model's
// script for it.
type wakeStep struct {
	notify string
	script []*llm.ChatResponse
}

// wakeExpect is what one wake must show.
type wakeExpect struct {
	// modelCalls is how many model calls the wake made.
	modelCalls int
	// outcomes is the per-tool ledger the loop judged the wake by.
	outcomes map[string]tally
	// unpublished lists Status.UnpublishedWrites as "tool" or
	// "tool/target", sorted by tool then target.
	unpublished []string
	// lastErrorNames is a field each unpublished write's LastError must
	// name. Assertions name fields and tools, never sentences.
	lastErrorNames string
	// degradedContains is a clause DegradedReason must contain; ""
	// means the loop must read healthy.
	degradedContains string
	// Status counters after the wake.
	consecutiveErrors, iterations, unpublishedWakes int
	// rejections and unpublishedEvt are the iteration_complete event's
	// write_rejections and unpublished_writes.
	rejections, unpublishedEvt int
	// toolCallRows counts the wake conversation's tool_calls rows per tool.
	toolCallRows map[string]rowTally
	// notifyRendered are notify bodies that must appear in the wake's
	// first model call and in its conversation's stored user message.
	notifyRendered []string
	// notifyAbsent are notify bodies that must appear in none of the
	// wake's model calls and none of its stored messages.
	notifyAbsent []string
}

// wakeFixture is one row of the table.
type wakeFixture struct {
	name    string
	outputs []looppkg.OutputSpec
	wakes   []wakeStep
	// ungated holds one expectation per wake.
	ungated []wakeExpect
}

func TestWakeFixtures(t *testing.T) {
	t.Parallel()

	publish := fixtureDigest.ToolName()

	fixtures := []wakeFixture{
		{
			// F-a (#1544/#1578): every publish is refused, the model
			// resends it once (2 is under the repeat guard's 3) and then
			// stops in prose. This is the end-to-end twin of
			// TestUnpublishedWakeReplayAndClear in the loop package: the
			// same Status, DegradedReason and iteration_complete values,
			// scaled to 2 rejections, then a landing wake that clears
			// the record and keeps its count.
			name:    "F-a publish rejected then the model stops",
			outputs: []looppkg.OutputSpec{fixtureDigest},
			wakes: []wakeStep{
				{notify: notifyN1, script: []*llm.ChatResponse{
					scriptedToolCall("fa-w1-c1", publish, overBudgetDigest()),
					scriptedToolCall("fa-w1-c2", publish, overBudgetDigest()),
					scriptedText("I couldn't update the digest this time."),
				}},
				{notify: notifyN2, script: []*llm.ChatResponse{
					scriptedToolCall("fa-w2-c1", publish, overBudgetDigest()),
					scriptedToolCall("fa-w2-c2", publish, validDigest()),
					scriptedText("Published."),
				}},
			},
			ungated: []wakeExpect{
				{
					modelCalls:       3,
					outcomes:         map[string]tally{publish: {calls: 2, failures: 2}},
					unpublished:      []string{publish},
					lastErrorNames:   "status_line",
					degradedContains: "ended without publishing " + publish + " after 2 rejections",
					iterations:       1,
					unpublishedWakes: 1,
					rejections:       2,
					unpublishedEvt:   1,
					toolCallRows:     map[string]rowTally{publish: {rows: 2, errors: 2}},
					notifyRendered:   []string{notifyN1},
				},
				{
					modelCalls:       3,
					outcomes:         map[string]tally{publish: {calls: 2, successes: 1, failures: 1}},
					iterations:       2,
					unpublishedWakes: 1,
					rejections:       1,
					toolCallRows:     map[string]rowTally{publish: {rows: 2, errors: 1}},
					notifyRendered:   []string{notifyN2},
					// pendingNotifies was drained by wake 1, so N1 reaches
					// wake 2 nowhere: it survives only as wake 1's stored
					// prompt text.
					notifyAbsent: []string{notifyN1},
				},
			},
		},
		{
			// F-ok: the positive control. The publish lands and the wake
			// is clean.
			name:    "F-ok publish lands",
			outputs: []looppkg.OutputSpec{fixtureDigest},
			wakes: []wakeStep{
				{notify: notifyN1, script: []*llm.ChatResponse{
					scriptedToolCall("fok-w1-c1", publish, validDigest()),
					scriptedText("Published."),
				}},
			},
			ungated: []wakeExpect{
				{
					modelCalls:     2,
					outcomes:       map[string]tally{publish: {calls: 1, successes: 1}},
					iterations:     1,
					toolCallRows:   map[string]rowTally{publish: {rows: 1}},
					notifyRendered: []string{notifyN1},
				},
			},
		},
	}

	for i, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			if len(fx.ungated) != len(fx.wakes) {
				t.Fatalf("fixture has %d wakes and %d ungated expectations", len(fx.wakes), len(fx.ungated))
			}
			rig := newWakeRig(t, wakeRigSpec{
				name:    fmt.Sprintf("fixture-loop-%d", i),
				task:    "Keep the fixture digest current with what fixture source alpha reports.",
				outputs: fx.outputs,
				wakes:   len(fx.wakes),
			})
			for w, step := range fx.wakes {
				obs := rig.wake(t, step.notify, step.script)
				assertWake(t, fmt.Sprintf("wake %d", w+1), rig, obs, fx.ungated[w])
			}
		})
	}
}

// assertWake checks one wake against its expectation and the table
// invariants.
func assertWake(t *testing.T, label string, rig *wakeRig, obs wakeObservation, want wakeExpect) {
	t.Helper()

	// Table invariants.
	if err := checkScriptConsumed(obs); err != nil {
		t.Errorf("%s: script invariant: %v", label, err)
	}
	for _, problem := range checkToolSurface(obs.calls, rig.hydrated) {
		t.Errorf("%s: surface invariant: %s", label, problem)
	}

	if len(obs.calls) != want.modelCalls {
		t.Errorf("%s: model calls = %d, want %d", label, len(obs.calls), want.modelCalls)
	}
	if got := projectOutcomes(obs.outcomes); !reflect.DeepEqual(got, want.outcomes) {
		t.Errorf("%s: outcomes = %v, want %v", label, got, want.outcomes)
	}

	s := obs.status
	var unpublished []string
	for _, w := range s.UnpublishedWrites {
		key := w.Tool
		if w.Target != "" {
			key += "/" + w.Target
		}
		unpublished = append(unpublished, key)
		// An entry can outlive the wake that recorded it, so it must
		// name a conversation and a time, not necessarily this wake's.
		if w.ConversationID == "" || w.At.IsZero() {
			t.Errorf("%s: unpublished write %s carries conversation %q at %v, want both set", label, key, w.ConversationID, w.At)
		}
		if !strings.Contains(w.LastError, want.lastErrorNames) {
			t.Errorf("%s: unpublished write %s last error %q does not name %q", label, key, w.LastError, want.lastErrorNames)
		}
	}
	if !reflect.DeepEqual(unpublished, want.unpublished) {
		t.Errorf("%s: unpublished = %v, want %v", label, unpublished, want.unpublished)
	}
	if want.degradedContains == "" {
		if obs.degraded != "" {
			t.Errorf("%s: DegradedReason = %q, want healthy", label, obs.degraded)
		}
	} else if !strings.Contains(obs.degraded, want.degradedContains) {
		t.Errorf("%s: DegradedReason = %q, want it to contain %q", label, obs.degraded, want.degradedContains)
	}
	if s.ConsecutiveErrors != want.consecutiveErrors || s.Iterations != want.iterations || s.UnpublishedWakes != want.unpublishedWakes {
		t.Errorf("%s: consecutive errors %d, iterations %d, unpublished wakes %d; want %d, %d, %d", label,
			s.ConsecutiveErrors, s.Iterations, s.UnpublishedWakes,
			want.consecutiveErrors, want.iterations, want.unpublishedWakes)
	}
	if want.consecutiveErrors == 0 && s.LastError != "" {
		t.Errorf("%s: LastError = %q, want empty for a wake that did not fail", label, s.LastError)
	}

	if obs.event.Kind != events.KindLoopIterationComplete {
		t.Errorf("%s: wake ended with %s, want %s", label, obs.event.Kind, events.KindLoopIterationComplete)
	}
	if got, want := obs.event.Data["conversation_id"], obs.conversationID; got != want {
		t.Errorf("%s: iteration_complete conversation_id = %v, want %v", label, got, want)
	}
	if obs.event.Data["write_rejections"] != want.rejections || obs.event.Data["unpublished_writes"] != want.unpublishedEvt {
		t.Errorf("%s: iteration_complete write_rejections=%v unpublished_writes=%v, want %d/%d", label,
			obs.event.Data["write_rejections"], obs.event.Data["unpublished_writes"], want.rejections, want.unpublishedEvt)
	}

	rows := make(map[string]rowTally)
	for _, row := range obs.toolCalls {
		r := rows[row.ToolName]
		r.rows++
		if row.Error != "" {
			r.errors++
		}
		rows[row.ToolName] = r
	}
	if len(rows) == 0 {
		rows = nil
	}
	if !reflect.DeepEqual(rows, want.toolCallRows) {
		t.Errorf("%s: tool_calls rows = %v, want %v", label, rows, want.toolCallRows)
	}

	var first string
	if len(obs.calls) > 0 {
		first = userContent(obs.calls[0].messages)
	}
	stored := storedUserContent(obs)
	for _, body := range want.notifyRendered {
		if !strings.Contains(first, body) {
			t.Errorf("%s: notify %q is not in the wake's first model call", label, body)
		}
		if !strings.Contains(stored, body) {
			t.Errorf("%s: notify %q is not in the wake conversation's stored user message", label, body)
		}
	}
	for _, body := range want.notifyAbsent {
		for i, call := range obs.calls {
			for _, m := range call.messages {
				if strings.Contains(m.Content, body) {
					t.Errorf("%s: notify %q reached model call %d (%s message)", label, body, i, m.Role)
				}
			}
		}
		for _, m := range obs.stored {
			if strings.Contains(m.Content, body) {
				t.Errorf("%s: notify %q is stored in the wake conversation (%s message)", label, body, m.Role)
			}
		}
	}
}

// projectOutcomes reduces the ledger to the counts a fixture pins, or
// nil when the wake called nothing.
func projectOutcomes(outcomes map[string]looppkg.ToolOutcome) map[string]tally {
	if len(outcomes) == 0 {
		return nil
	}
	out := make(map[string]tally, len(outcomes))
	for tool, o := range outcomes {
		out[tool] = tally{calls: o.Calls, successes: o.Successes, failures: o.Failures}
	}
	return out
}

// storedUserContent joins the wake conversation's stored user messages.
func storedUserContent(obs wakeObservation) string {
	var b strings.Builder
	for _, m := range obs.stored {
		if m.Role == "user" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
