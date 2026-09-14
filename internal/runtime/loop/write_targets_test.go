package loop

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	aliceContact = "3f2a1c9e-7b4d-4e8a-9c21-5d6e7f8a9b0c"
	bobContact   = "8c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f"
	carolContact = "b7e6d5c4-3b2a-4190-8f7e-6d5c4b3a2918"
	daveContact  = "d4c3b2a1-0f9e-4d8c-b7a6-958473625140"
)

var (
	// landedAfterRetry is a contact whose dossier was rejected once and
	// then landed.
	landedAfterRetry = TargetOutcome{Calls: 2, Failures: 1, Successes: 1, LastError: "teaser is too long"}
	// landed is a contact whose dossier landed on the first call.
	landed = TargetOutcome{Calls: 1, Successes: 1}
	// refusedThroughout is a contact whose dossier was rejected three
	// times and then refused by the repeat guard.
	refusedThroughout = TargetOutcome{Calls: 3, Failures: 4, Blocked: 1, LastError: rejectionText}
)

// dossierOutcome builds contact_dossier_write's tally from its
// per-contact breakdown, summing the counts as the engine does.
func dossierOutcome(targets map[string]TargetOutcome) ToolOutcome {
	o := ToolOutcome{Targets: targets}
	for _, t := range targets {
		o.Calls += t.Calls
		o.Failures += t.Failures
		o.Blocked += t.Blocked
		o.Successes += t.Successes
		if t.Failures > 0 && t.Successes == 0 {
			o.LastError = t.LastError
		}
	}
	return o
}

func dossierKey(contact string) string { return contactDossierWriteTool + "/" + contact }

// writeKeys renders writes as tool/target keys, in order.
func writeKeys(writes []UnpublishedWrite) string {
	keys := make([]string, 0, len(writes))
	for _, w := range writes {
		keys = append(keys, w.Tool+"/"+w.Target)
	}
	return strings.Join(keys, ",")
}

// TestWriteTarget pins how a call's target is named: a dossier's
// contact_id, normalized, and nothing for any other tool.
func TestWriteTarget(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{name: "dossier contact_id", tool: contactDossierWriteTool, args: map[string]any{"contact_id": bobContact}, want: bobContact},
		// The tool refuses each of these spellings; the canonical retry
		// that lands is the same contact's dossier.
		{name: "padded upper case spells the same contact", tool: contactDossierWriteTool,
			args: map[string]any{"contact_id": "  " + strings.ToUpper(bobContact) + "\n"}, want: bobContact},
		{name: "braced spells the same contact", tool: contactDossierWriteTool,
			args: map[string]any{"contact_id": "{" + bobContact + "}"}, want: bobContact},
		{name: "urn form spells the same contact", tool: contactDossierWriteTool,
			args: map[string]any{"contact_id": "urn:uuid:" + bobContact}, want: bobContact},
		{name: "unhyphenated spells the same contact", tool: contactDossierWriteTool,
			args: map[string]any{"contact_id": strings.ReplaceAll(bobContact, "-", "")}, want: bobContact},
		{name: "a contact tag spells no contact", tool: contactDossierWriteTool,
			args: map[string]any{"contact_id": "contact:" + bobContact}, want: ""},
		{name: "a name spells no contact", tool: contactDossierWriteTool, args: map[string]any{"contact_id": "Bob"}, want: ""},
		{name: "the nil UUID spells no contact", tool: contactDossierWriteTool,
			args: map[string]any{"contact_id": "00000000-0000-0000-0000-000000000000"}, want: ""},
		{name: "dossier without contact_id", tool: contactDossierWriteTool, args: map[string]any{"teaser": "t"}, want: ""},
		{name: "dossier with a non-string contact_id", tool: contactDossierWriteTool, args: map[string]any{"contact_id": 42}, want: ""},
		{name: "dossier with nil args", tool: contactDossierWriteTool, want: ""},
		{name: "declared output tool", tool: tripPublish, args: map[string]any{"contact_id": bobContact}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := writeTarget(tc.tool, tc.args); got != tc.want {
				t.Errorf("writeTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWriteOutcomeStatePerTarget pins the unpublished predicate per
// tool and target: a target refused on every call is unpublished even
// when another target of the same tool landed, and only a success for
// that same target clears it.
func TestWriteOutcomeStatePerTarget(t *testing.T) {
	durable := durableWriteTools(tripOutputs())
	type wake struct {
		outcomes        map[string]ToolOutcome
		wantRejections  int
		wantUnpublished string
	}
	aliceLandsBobRefused := wake{
		outcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(map[string]TargetOutcome{
			aliceContact: landedAfterRetry, bobContact: refusedThroughout,
		})},
		wantRejections:  5,
		wantUnpublished: dossierKey(bobContact),
	}
	// noDossier is a dossier call that wrote no dossier of its own: its
	// contact_id spelled no contact, or the tool refused the contact it
	// named and pointed the model at another. The engine tallies it under
	// the empty target.
	noDossier := map[string]TargetOutcome{"": {Calls: 1, Failures: 1,
		LastError: "contact_id " + carolContact + " is not an active structured contact; call contact_lookup"}}
	withNoDossier := func(targets map[string]TargetOutcome) map[string]ToolOutcome {
		merged := map[string]TargetOutcome{"": noDossier[""]}
		for target, o := range targets {
			merged[target] = o
		}
		return map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(merged)}
	}
	cases := []struct {
		name       string
		wakes      []wake
		wantLatest string
		// wantEntry, when set, is the one entry latest must hold, with the
		// refused target's own counts rather than the tool's.
		wantEntry *UnpublishedWrite
	}{
		{
			name:       "one contact lands and another is refused on every call",
			wakes:      []wake{aliceLandsBobRefused},
			wantLatest: dossierKey(bobContact),
			wantEntry: &UnpublishedWrite{Tool: contactDossierWriteTool, Target: bobContact,
				Failures: 4, LastError: rejectionText},
		},
		{
			name: "every refused contact is its own entry",
			wakes: []wake{{
				outcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(map[string]TargetOutcome{
					bobContact: refusedThroughout, carolContact: refusedThroughout,
				})},
				wantRejections:  8,
				wantUnpublished: dossierKey(bobContact) + "," + dossierKey(carolContact),
			}},
			wantLatest: dossierKey(bobContact) + "," + dossierKey(carolContact),
		},
		{
			name: "a later wake landing another contact leaves the refused one",
			wakes: []wake{aliceLandsBobRefused, {
				outcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(map[string]TargetOutcome{aliceContact: landed})},
			}},
			wantLatest: dossierKey(bobContact),
		},
		{
			name: "a later wake landing the refused contact clears it",
			wakes: []wake{aliceLandsBobRefused, {
				outcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(map[string]TargetOutcome{bobContact: landed})},
			}},
		},
		{
			name: "a declared output's tool is the empty target",
			wakes: []wake{{
				outcomes: map[string]ToolOutcome{tripPublish: {Calls: 4, Failures: 4, LastError: rejectionText,
					Targets: map[string]TargetOutcome{"": {Calls: 4, Failures: 4, LastError: rejectionText}}}},
				wantRejections:  4,
				wantUnpublished: tripPublish + "/",
			}},
			wantLatest: tripPublish + "/",
		},
		{
			name: "a declared output's later success clears it",
			wakes: []wake{
				{
					outcomes: map[string]ToolOutcome{tripPublish: {Calls: 4, Failures: 4,
						Targets: map[string]TargetOutcome{"": {Calls: 4, Failures: 4}}}},
					wantRejections:  4,
					wantUnpublished: tripPublish + "/",
				},
				{outcomes: map[string]ToolOutcome{tripPublish: {Calls: 1, Successes: 1,
					Targets: map[string]TargetOutcome{"": {Calls: 1, Successes: 1}}}}},
			},
		},
		{
			// A runner that names no targets reports only the tool's sums,
			// so one landed dossier reads as the tool landing, as before.
			name: "no breakdown keeps the per-tool reading of a success",
			wakes: []wake{{
				outcomes:       map[string]ToolOutcome{contactDossierWriteTool: {Calls: 4, Failures: 3, Successes: 1}},
				wantRejections: 3,
			}},
		},
		{
			name: "no breakdown keeps the per-tool reading of a failure",
			wakes: []wake{{
				outcomes:        map[string]ToolOutcome{contactDossierWriteTool: {Calls: 2, Failures: 2}},
				wantRejections:  2,
				wantUnpublished: contactDossierWriteTool + "/",
			}},
			wantLatest: contactDossierWriteTool + "/",
		},
		{
			// Carol's contact_id named no active contact; the refusal said
			// to look the contact up, and Alice's dossier landed.
			name:  "refused X, then landed Y as the refusal advised",
			wakes: []wake{{outcomes: withNoDossier(map[string]TargetOutcome{aliceContact: landed}), wantRejections: 1}},
		},
		{
			name: "a call that wrote no dossier is flagged when the wake landed none",
			wakes: []wake{{
				outcomes:        withNoDossier(nil),
				wantRejections:  1,
				wantUnpublished: contactDossierWriteTool + "/",
			}},
			wantLatest: contactDossierWriteTool + "/",
		},
		{
			name: "any later landed dossier clears a call that wrote no dossier",
			wakes: []wake{
				{outcomes: withNoDossier(nil), wantRejections: 1, wantUnpublished: contactDossierWriteTool + "/"},
				{outcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(map[string]TargetOutcome{aliceContact: landed})}},
			},
		},
		{
			name: "a landed dossier clears calls that wrote none but not a refused contact",
			wakes: []wake{
				{
					outcomes:        withNoDossier(map[string]TargetOutcome{bobContact: refusedThroughout}),
					wantRejections:  5,
					wantUnpublished: contactDossierWriteTool + "/," + dossierKey(bobContact),
				},
				{outcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(map[string]TargetOutcome{aliceContact: landed})}},
			},
			wantLatest: dossierKey(bobContact),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var state writeOutcomeState
			for i, w := range tc.wakes {
				tally := state.observe(w.outcomes, durable, "conv-test", time.Now())
				if tally.rejections != w.wantRejections {
					t.Errorf("wake %d rejections = %d, want %d", i, tally.rejections, w.wantRejections)
				}
				if got := writeKeys(tally.unpublished); got != w.wantUnpublished {
					t.Errorf("wake %d unpublished = %q, want %q", i, got, w.wantUnpublished)
				}
			}
			latest := state.snapshot()
			if got := writeKeys(latest); got != tc.wantLatest {
				t.Fatalf("latest = %q, want %q", got, tc.wantLatest)
			}
			if tc.wantEntry != nil {
				got := latest[0]
				if got.Tool != tc.wantEntry.Tool || got.Target != tc.wantEntry.Target ||
					got.Failures != tc.wantEntry.Failures || got.LastError != tc.wantEntry.LastError {
					t.Errorf("entry = %+v, want %+v", got, *tc.wantEntry)
				}
			}
		})
	}
}

// TestWriteOutcomeStateBound keeps a loop's record bounded however many
// contacts a bad run refuses, dropping the oldest entries first and
// breaking a tie between one wake's entries by key.
func TestWriteOutcomeStateBound(t *testing.T) {
	const refused = maxUnpublishedWriteEntries + 2
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	contact := func(i int) string { return fmt.Sprintf("contact-%02d", i) }
	refusal := func(contacts ...string) map[string]ToolOutcome {
		targets := make(map[string]TargetOutcome, len(contacts))
		for _, c := range contacts {
			targets[c] = refusedThroughout
		}
		return map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(targets)}
	}
	cases := []struct {
		name string
		// run feeds the wakes and returns the last wake's tally.
		run             func(*writeOutcomeState) wakeWriteTally
		wantDropped     []string
		wantWakes       int
		wantLastTallied int
	}{
		{
			// Keys run opposite to age, so dropping by key alone would
			// keep the wrong entries.
			name: "oldest wakes drop first",
			run: func(s *writeOutcomeState) wakeWriteTally {
				var tally wakeWriteTally
				for i := 0; i < refused; i++ {
					tally = s.observe(refusal(contact(refused-1-i)), durableWriteTools(nil), "conv", base.Add(time.Duration(i)*time.Minute))
				}
				return tally
			},
			wantDropped:     []string{contact(refused - 1), contact(refused - 2)},
			wantWakes:       refused,
			wantLastTallied: 1,
		},
		{
			name: "one wake over the bound drops its entries in key order",
			run: func(s *writeOutcomeState) wakeWriteTally {
				contacts := make([]string, refused)
				for i := range contacts {
					contacts[i] = contact(i)
				}
				return s.observe(refusal(contacts...), durableWriteTools(nil), "conv", base)
			},
			wantDropped:     []string{contact(0), contact(1)},
			wantWakes:       1,
			wantLastTallied: refused,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var state writeOutcomeState
			tally := tc.run(&state)
			latest := state.snapshot()
			if len(latest) != maxUnpublishedWriteEntries {
				t.Fatalf("latest holds %d entries, want %d", len(latest), maxUnpublishedWriteEntries)
			}
			kept := writeKeys(latest)
			for _, c := range tc.wantDropped {
				if strings.Contains(kept, dossierKey(c)) {
					t.Errorf("latest kept %s, want it dropped: %s", c, kept)
				}
			}
			if state.wakes != tc.wantWakes {
				t.Errorf("unpublished wakes = %d, want %d", state.wakes, tc.wantWakes)
			}
			// The journal counts what the wake failed to land, not what the
			// bounded record kept.
			if len(tally.unpublished) != tc.wantLastTallied {
				t.Errorf("last wake tallied %d unpublished, want %d", len(tally.unpublished), tc.wantLastTallied)
			}
		})
	}
}

// TestUnpublishedWriteClauseTargets pins how the health clause names a
// target, and that it names at most three writes, newest first, with
// same-wake ties in key order.
func TestUnpublishedWriteClauseTargets(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	write := func(tool, target string, failures int, ago time.Duration) UnpublishedWrite {
		return UnpublishedWrite{Tool: tool, Target: target, Failures: failures, At: now.Add(-ago)}
	}
	cases := []struct {
		name   string
		writes []UnpublishedWrite
		want   string
	}{
		{
			name:   "a dossier names its contact",
			writes: []UnpublishedWrite{write(contactDossierWriteTool, bobContact, 7, 2*time.Hour)},
			want:   "wake at -2h ended without publishing contact_dossier_write for contact " + bobContact + " after 7 rejections",
		},
		{
			name:   "a declared output names no target",
			writes: []UnpublishedWrite{write(tripPublish, "", 1, 90*time.Second)},
			want:   "wake at -90s ended without publishing " + tripPublish + " after 1 rejection",
		},
		{
			name: "three named, newest first, the rest counted",
			writes: []UnpublishedWrite{
				write(tripPublish, "", 2, 4*time.Hour),
				write(contactDossierWriteTool, carolContact, 3, time.Hour),
				write(contactDossierWriteTool, daveContact, 5, 3*time.Hour),
				write(contactDossierWriteTool, aliceContact, 4, time.Hour),
				write(contactDossierWriteTool, bobContact, 7, time.Hour),
			},
			want: "wake at -1h ended without publishing contact_dossier_write for contact " + aliceContact + " after 4 rejections; " +
				"wake at -1h ended without publishing contact_dossier_write for contact " + bobContact + " after 7 rejections; " +
				"wake at -1h ended without publishing contact_dossier_write for contact " + carolContact + " after 3 rejections (+2 more)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnpublishedWriteClause(Status{UnpublishedWrites: tc.writes}, now); got != tc.want {
				t.Errorf("clause = %q\nwant     %q", got, tc.want)
			}
		})
	}
}

// TestPrepareAgentTurnRequestTargetKey: every turn the loop prepares
// carries the loop's own target function, replacing any a caller set,
// because the loop judges the wake's writes by those targets.
func TestPrepareAgentTurnRequestTargetKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		declared func(string, map[string]any) string
	}{
		{name: "no caller target key"},
		{name: "a caller target key is replaced", declared: func(string, map[string]any) string { return "caller" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := New(Config{Name: "target-key-test", Task: "t"}, Deps{Runner: &noopRunner{}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			req, err := l.prepareAgentTurnRequest(Request{TargetKey: tc.declared}, "conv-1", false)
			if err != nil {
				t.Fatalf("prepareAgentTurnRequest: %v", err)
			}
			if req.TargetKey == nil {
				t.Fatal("prepared request has no TargetKey")
			}
			if got := req.TargetKey(contactDossierWriteTool, map[string]any{"contact_id": strings.ToUpper(bobContact)}); got != bobContact {
				t.Errorf("dossier target = %q, want %q", got, bobContact)
			}
			if got := req.TargetKey(tripPublish, map[string]any{"contact_id": bobContact}); got != "" {
				t.Errorf("declared output target = %q, want empty", got)
			}
		})
	}
}

// TestUnpublishedDossierTargetsReplay drives a live loop through the
// production shape: one wake lands Alice's dossier and never lands
// Bob's, a later wake lands only Alice's again, and a third lands
// Bob's. Bob's write stays flagged until the third wake.
func TestUnpublishedDossierTargetsReplay(t *testing.T) {
	t.Parallel()
	wakes := []struct {
		targets   map[string]TargetOutcome
		wantAfter string
	}{
		{targets: map[string]TargetOutcome{aliceContact: landedAfterRetry, bobContact: refusedThroughout}, wantAfter: dossierKey(bobContact)},
		{targets: map[string]TargetOutcome{aliceContact: landed}, wantAfter: dossierKey(bobContact)},
		{targets: map[string]TargetOutcome{bobContact: landed}},
	}
	const loopName = "dossier-target-replay"
	mailbox := newTestMailbox(t)
	var (
		l          *Loop
		wakeCount  atomic.Int32
		keyMissing atomic.Bool
		after      = make([]Status, len(wakes))
	)
	runner := &turnCallbackRunner{fn: func(ctx context.Context, req RunRequest, _ StreamCallback) (*RunResponse, error) {
		n := int(wakeCount.Add(1))
		if req.TargetKey == nil {
			keyMissing.Store(true)
		}
		if n > 1 {
			after[n-2] = l.Status()
		}
		if n < len(wakes) {
			if _, err := mailbox.enqueue(ctx, loopName, "test", []byte(fmt.Sprintf("wake %d", n+1))); err != nil {
				return nil, err
			}
		}
		return &RunResponse{
			Content: "done", Model: "m", FinishReason: "stop",
			ToolOutcomes: map[string]ToolOutcome{contactDossierWriteTool: dossierOutcome(wakes[n-1].targets)},
		}, nil
	}}
	var err error
	l, err = New(Config{
		Name:      loopName,
		Operation: OperationEventDriven,
		TurnBuilder: func(_ context.Context, _ TurnInput) (*AgentTurn, error) {
			return &AgentTurn{Request: Request{Messages: []Message{{Role: "user", Content: "wake"}}}}, nil
		},
		MaxIter: len(wakes),
	}, Deps{Runner: runner, Mailbox: mailbox})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := mailbox.enqueue(context.Background(), loopName, "test", []byte("wake 1")); err != nil {
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
	after[len(wakes)-1] = l.Status()

	if keyMissing.Load() {
		t.Error("a prepared turn reached the runner without a TargetKey")
	}
	if got := int(wakeCount.Load()); got != len(wakes) {
		t.Fatalf("wakes = %d, want %d", got, len(wakes))
	}
	for i, w := range wakes {
		if got := writeKeys(after[i].UnpublishedWrites); got != w.wantAfter {
			t.Errorf("after wake %d unpublished = %q, want %q", i+1, got, w.wantAfter)
		}
	}
	// The flagged write keeps the wake that refused it; a later wake
	// that only landed another contact does not restamp it.
	if first, second := after[0].UnpublishedWrites, after[1].UnpublishedWrites; len(first) == 1 && len(second) == 1 &&
		(!first[0].At.Equal(second[0].At) || first[0].ConversationID != second[0].ConversationID) {
		t.Errorf("entry moved between wakes: %+v then %+v", first[0], second[0])
	}
	if final := after[len(wakes)-1]; final.UnpublishedWakes != 1 || DegradedReason(final, time.Now()) != "" {
		t.Errorf("final unpublished wakes = %d, reason %q; want 1 and healthy", final.UnpublishedWakes, DegradedReason(final, time.Now()))
	}
}
