package loop

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

const (
	tripOutputName = "utah_trip_curator"
	tripPublish    = "publish_output_utah_trip_curator"
	rejectionText  = "status_line is 190 characters and the limit is 160"
)

func tripOutputs() []OutputSpec {
	return []OutputSpec{{
		Name:   tripOutputName,
		Type:   OutputTypeMaintainedDocument,
		Ref:    "kb:utah-trip.md",
		Facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacetTeaser}},
	}}
}

// TestWriteOutcomeStateObserve pins the unpublished predicate across a
// sequence of wakes: a durable write tool with failures and no success
// in a wake is unpublished; a success clears it; a wake that never calls
// the tool leaves it standing; non-durable tools never count.
func TestWriteOutcomeStateObserve(t *testing.T) {
	durableTrip := durableWriteTools(tripOutputs())
	durableNone := durableWriteTools(nil)
	type wake struct {
		durable         map[string]bool
		outcomes        map[string]ToolOutcome
		wantRejections  int
		wantUnpublished []string
	}
	cases := []struct {
		name        string
		wakes       []wake
		wantLatest  []string
		wantWakes   int
		wantFailure int
	}{
		{
			// The production replay: every publish rejected, two identical
			// resends refused by the repeat guard, the turn ended in prose.
			name: "every publish rejected is unpublished",
			wakes: []wake{{
				durable: durableTrip,
				outcomes: map[string]ToolOutcome{
					tripPublish: {Calls: 16, Failures: 18, Blocked: 2, LastError: rejectionText},
					"doc_read":  {Calls: 2, Successes: 2},
				},
				wantRejections:  18,
				wantUnpublished: []string{tripPublish},
			}},
			wantLatest:  []string{tripPublish},
			wantWakes:   1,
			wantFailure: 18,
		},
		{
			name: "rejections followed by a success are published",
			wakes: []wake{{
				durable:        durableTrip,
				outcomes:       map[string]ToolOutcome{tripPublish: {Calls: 4, Failures: 3, Successes: 1}},
				wantRejections: 3,
			}},
		},
		{
			name: "contact dossier write counts on a loop with no outputs",
			wakes: []wake{{
				durable:         durableNone,
				outcomes:        map[string]ToolOutcome{contactDossierWriteTool: {Calls: 2, Failures: 2}},
				wantRejections:  2,
				wantUnpublished: []string{contactDossierWriteTool},
			}},
			wantLatest:  []string{contactDossierWriteTool},
			wantWakes:   1,
			wantFailure: 2,
		},
		{
			name: "failures of a non-durable tool are not writes",
			wakes: []wake{{
				durable:  durableTrip,
				outcomes: map[string]ToolOutcome{"web_fetch": {Calls: 5, Failures: 5}},
			}},
		},
		{
			name: "a later success clears the entry",
			wakes: []wake{
				{durable: durableTrip, outcomes: map[string]ToolOutcome{tripPublish: {Calls: 4, Failures: 4}}, wantRejections: 4, wantUnpublished: []string{tripPublish}},
				{durable: durableTrip, outcomes: map[string]ToolOutcome{tripPublish: {Calls: 1, Successes: 1}}},
			},
			wantWakes: 1,
		},
		{
			name: "a wake that never calls the tool leaves it unpublished",
			wakes: []wake{
				{durable: durableTrip, outcomes: map[string]ToolOutcome{tripPublish: {Calls: 4, Failures: 4}}, wantRejections: 4, wantUnpublished: []string{tripPublish}},
				{durable: durableTrip, outcomes: map[string]ToolOutcome{"doc_read": {Calls: 1, Successes: 1}}},
			},
			wantLatest:  []string{tripPublish},
			wantWakes:   1,
			wantFailure: 4,
		},
		{
			name: "an output removed by retune stops holding the loop degraded",
			wakes: []wake{
				{durable: durableTrip, outcomes: map[string]ToolOutcome{tripPublish: {Calls: 4, Failures: 4}}, wantRejections: 4, wantUnpublished: []string{tripPublish}},
				{durable: durableNone, outcomes: nil},
			},
			wantWakes: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var state writeOutcomeState
			for i, w := range tc.wakes {
				tally := state.observe(w.outcomes, w.durable, "conv-test", time.Now())
				if tally.rejections != w.wantRejections {
					t.Errorf("wake %d rejections = %d, want %d", i, tally.rejections, w.wantRejections)
				}
				var got []string
				for _, u := range tally.unpublished {
					got = append(got, u.Tool)
				}
				if strings.Join(got, ",") != strings.Join(w.wantUnpublished, ",") {
					t.Errorf("wake %d unpublished = %v, want %v", i, got, w.wantUnpublished)
				}
			}
			latest := state.snapshot()
			var tools []string
			for _, u := range latest {
				tools = append(tools, u.Tool)
			}
			if strings.Join(tools, ",") != strings.Join(tc.wantLatest, ",") {
				t.Fatalf("latest = %v, want %v", tools, tc.wantLatest)
			}
			if state.wakes != tc.wantWakes {
				t.Errorf("unpublished wakes = %d, want %d", state.wakes, tc.wantWakes)
			}
			if len(latest) == 1 && latest[0].Failures != tc.wantFailure {
				t.Errorf("failures = %d, want %d", latest[0].Failures, tc.wantFailure)
			}
		})
	}
}

// TestDegradedReason pins the one degraded definition both health
// rollups read, including the unpublished clause's wording and bounds.
func TestDegradedReason(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	write := func(tool string, failures int, ago time.Duration) UnpublishedWrite {
		return UnpublishedWrite{Tool: tool, Failures: failures, At: now.Add(-ago)}
	}
	cases := []struct {
		name   string
		status Status
		want   string
	}{
		{name: "healthy", status: Status{State: StateSleeping}, want: ""},
		{name: "consecutive errors", status: Status{ConsecutiveErrors: 3}, want: "3 consecutive errors"},
		{name: "error state with reset counter", status: Status{State: StateError}, want: "error"},
		{
			name:   "unpublished write",
			status: Status{UnpublishedWrites: []UnpublishedWrite{write(tripPublish, 18, 2*time.Hour)}},
			want:   "wake at -2h ended without publishing " + tripPublish + " after 18 rejections",
		},
		{
			name:   "single rejection is singular",
			status: Status{UnpublishedWrites: []UnpublishedWrite{write(contactDossierWriteTool, 1, 90*time.Second)}},
			want:   "wake at -90s ended without publishing contact_dossier_write after 1 rejection",
		},
		{
			name: "errors and an unpublished write are both named",
			status: Status{ConsecutiveErrors: 2,
				UnpublishedWrites: []UnpublishedWrite{write(tripPublish, 5, time.Hour)}},
			want: "2 consecutive errors; wake at -1h ended without publishing " + tripPublish + " after 5 rejections",
		},
		{
			name: "newest first, capped with an explicit remainder",
			status: Status{UnpublishedWrites: []UnpublishedWrite{
				write("publish_output_a", 2, 4*time.Hour),
				write("publish_output_b", 2, 3*time.Hour),
				write("publish_output_c", 2, 2*time.Hour),
				write("publish_output_d", 2, time.Hour),
			}},
			want: "wake at -1h ended without publishing publish_output_d after 2 rejections; " +
				"wake at -2h ended without publishing publish_output_c after 2 rejections; " +
				"wake at -3h ended without publishing publish_output_b after 2 rejections (+1 more)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DegradedReason(tc.status, now); got != tc.want {
				t.Errorf("DegradedReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUnpublishedWakeReplayAndClear replays the production shape through
// a live loop: a wake whose durable write fails on every call, repeat-
// guard refusals included, ends with a text reply. The wake must be
// recorded as unpublished and read as degraded while the loop's error
// counters, mailbox ack, and iteration accounting stay exactly as for a
// successful wake; a later wake that lands the write clears it.
func TestUnpublishedWakeReplayAndClear(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		outputs []OutputSpec
		tool    string
	}{
		{name: "declared output publish", outputs: tripOutputs(), tool: tripPublish},
		{name: "contact dossier write with no declared outputs", tool: contactDossierWriteTool},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bus := events.New()
			ch := bus.Subscribe(64)
			defer bus.Unsubscribe(ch)
			mailbox := newTestMailbox(t)
			loopName := "unpublished-replay-" + string(rune('a'+i))

			var (
				l          *Loop
				wakes      atomic.Int32
				afterFirst Status
				depthAt2   = -1
			)
			runner := &turnCallbackRunner{fn: func(ctx context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
				switch wakes.Add(1) {
				case 1:
					// Queue the next wake's input now; it is not in this
					// wake's batch, so the post-ack re-wake delivers it.
					if _, err := mailbox.enqueue(ctx, loopName, "test", []byte("second")); err != nil {
						return nil, err
					}
					return &RunResponse{
						Content: "The trip document could not be updated this time.", Model: "m", FinishReason: "stop",
						ToolOutcomes: map[string]ToolOutcome{
							tc.tool:    {Calls: 16, Failures: 18, Blocked: 2, LastError: rejectionText},
							"doc_read": {Calls: 1, Successes: 1},
						},
					}, nil
				default:
					afterFirst = l.Status()
					depth, err := mailbox.depth(ctx, loopName)
					if err != nil {
						return nil, err
					}
					depthAt2 = depth
					return &RunResponse{
						Content: "published", Model: "m", FinishReason: "stop",
						ToolOutcomes: map[string]ToolOutcome{tc.tool: {Calls: 2, Failures: 1, Successes: 1, LastError: rejectionText}},
					}, nil
				}
			}}
			var err error
			l, err = New(Config{
				Name:      loopName,
				Operation: OperationEventDriven,
				Outputs:   tc.outputs,
				TurnBuilder: func(_ context.Context, _ TurnInput) (*AgentTurn, error) {
					return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "wake"}}}}, nil
				},
				MaxIter: 2,
			}, Deps{Runner: runner, Mailbox: mailbox, EventBus: bus})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := mailbox.enqueue(context.Background(), loopName, "test", []byte("first")); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			if err := l.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer l.Stop()
			select {
			case <-l.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("loop did not finish")
			}

			// Between the wakes: unpublished, degraded, and nothing else moved.
			if len(afterFirst.UnpublishedWrites) != 1 {
				t.Fatalf("after first wake UnpublishedWrites = %+v, want one entry", afterFirst.UnpublishedWrites)
			}
			w := afterFirst.UnpublishedWrites[0]
			if w.Tool != tc.tool || w.Failures != 18 || w.LastError != rejectionText || w.ConversationID == "" || w.At.IsZero() {
				t.Errorf("unpublished write = %+v", w)
			}
			if afterFirst.UnpublishedWakes != 1 {
				t.Errorf("UnpublishedWakes = %d, want 1", afterFirst.UnpublishedWakes)
			}
			if afterFirst.ConsecutiveErrors != 0 || afterFirst.LastError != "" || afterFirst.Iterations != 1 {
				t.Errorf("error accounting moved: consecutive=%d last=%q iterations=%d",
					afterFirst.ConsecutiveErrors, afterFirst.LastError, afterFirst.Iterations)
			}
			wantClause := "ended without publishing " + tc.tool + " after 18 rejections"
			if reason := DegradedReason(afterFirst, time.Now()); !strings.Contains(reason, wantClause) {
				t.Errorf("DegradedReason = %q, want it to contain %q", reason, wantClause)
			}
			// Only the second wake's own item is pending: the first wake's
			// batch was acked as for any successful wake.
			if depthAt2 != 1 {
				t.Errorf("mailbox depth during second wake = %d, want 1 (first batch acked)", depthAt2)
			}

			// After the landing wake: cleared, count retained, nothing pending.
			final := l.Status()
			if len(final.UnpublishedWrites) != 0 || final.UnpublishedWakes != 1 {
				t.Errorf("final unpublished = %+v wakes=%d, want cleared with count 1", final.UnpublishedWrites, final.UnpublishedWakes)
			}
			if reason := DegradedReason(final, time.Now()); reason != "" {
				t.Errorf("final DegradedReason = %q, want healthy", reason)
			}
			if depth, err := mailbox.depth(context.Background(), loopName); err != nil || depth != 0 {
				t.Errorf("final mailbox depth = %d (err %v), want 0", depth, err)
			}

			var completes []events.Event
			for {
				select {
				case evt := <-ch:
					if evt.Kind == events.KindLoopIterationComplete {
						completes = append(completes, evt)
					}
					continue
				default:
				}
				break
			}
			if len(completes) != 2 {
				t.Fatalf("iteration_complete events = %d, want 2", len(completes))
			}
			for idx, want := range []struct{ rejections, unpublished int }{{18, 1}, {1, 0}} {
				data := completes[idx].Data
				if data["write_rejections"] != want.rejections || data["unpublished_writes"] != want.unpublished {
					t.Errorf("iteration_complete[%d] write_rejections=%v unpublished_writes=%v, want %d/%d",
						idx, data["write_rejections"], data["unpublished_writes"], want.rejections, want.unpublished)
				}
			}
		})
	}
}
