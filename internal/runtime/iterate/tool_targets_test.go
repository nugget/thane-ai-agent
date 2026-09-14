package iterate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
)

// withEmptyTarget returns o with the breakdown a run reports when it
// has no TargetKey: one entry, under the empty target, carrying the
// tool's own counts.
func withEmptyTarget(o ToolOutcome) ToolOutcome {
	o.Targets = map[string]TargetOutcome{"": {
		Calls: o.Calls, Failures: o.Failures, Blocked: o.Blocked,
		Successes: o.Successes, LastError: o.LastError,
	}}
	return o
}

func dossierArgs(contactID, statusLine string) map[string]any {
	return map[string]any{"contact_id": contactID, "status_line": statusLine}
}

// TestToolOutcomesPerTarget pins the per-target breakdown: each call,
// including a call the repeat guard refuses, is charged to the target
// TargetKey names for it. A tool that lands one target and never lands
// another shows both, which the tool's summed counts cannot.
func TestToolOutcomesPerTarget(t *testing.T) {
	const (
		dossier     = "contact_dossier_write"
		rejection   = "status_line is 190 characters and the limit is 160"
		forkRefusal = "contact_dossier_write refused to start a second dossier: write what you meant into the existing dossier with contact_dossier_write contact_id alice"
	)
	byContact := func(_ string, args map[string]any) string {
		id, _ := args["contact_id"].(string)
		return id
	}
	// Alice's dossier lands. Bob's is rejected three times, and the
	// fourth identical call is refused by the repeat guard.
	aliceThenBob := []map[string]any{
		dossierArgs("alice", "a"),
		dossierArgs("bob", "b"), dossierArgs("bob", "b"), dossierArgs("bob", "b"), dossierArgs("bob", "b"),
	}
	aliceLandsBobRejected := []scriptStep{{result: "written"}, {err: rejection}}

	cases := []struct {
		name      string
		tool      string
		targetKey func(string, map[string]any) string
		script    []scriptStep
		calls     []map[string]any
		want      ToolOutcome
	}{
		{
			name:      "one target lands and another is refused on every call",
			tool:      dossier,
			targetKey: byContact,
			script:    aliceLandsBobRejected,
			calls:     aliceThenBob,
			want: ToolOutcome{Calls: 4, Failures: 4, Blocked: 1, Successes: 1, LastError: rejection,
				Targets: map[string]TargetOutcome{
					"alice": {Calls: 1, Successes: 1},
					"bob":   {Calls: 3, Failures: 4, Blocked: 1, LastError: rejection},
				}},
		},
		{
			name:   "no target key keeps every call under the empty target",
			tool:   dossier,
			script: aliceLandsBobRejected,
			calls:  aliceThenBob,
			want:   withEmptyTarget(ToolOutcome{Calls: 4, Failures: 4, Blocked: 1, Successes: 1, LastError: rejection}),
		},
		{
			name:      "a declared output's tool writes the empty target",
			tool:      "publish_output_trip",
			targetKey: byContact,
			script:    []scriptStep{{err: rejection}, {result: "published"}},
			calls:     []map[string]any{publishArgs("a", "t", "d"), publishArgs("b", "t", "d")},
			want:      withEmptyTarget(ToolOutcome{Calls: 2, Failures: 1, Successes: 1, LastError: rejection}),
		},
		{
			// Frank's first dossier is refused as a second dossier for a
			// person Alice's record already holds one for: the call wrote no
			// document of its own, and neither does its guard-refused repeat.
			name:      "a target the tool refuses as a document is charged to no target, guard refusals included",
			tool:      dossier,
			targetKey: byContact,
			script:    []scriptStep{{result: "written"}, {err: forkRefusal, targetRefused: true}},
			calls: []map[string]any{
				dossierArgs("alice", "a"),
				dossierArgs("frank", "f"), dossierArgs("frank", "f"), dossierArgs("frank", "f"), dossierArgs("frank", "f"),
			},
			want: ToolOutcome{Calls: 4, Failures: 4, Blocked: 1, Successes: 1, LastError: forkRefusal,
				Targets: map[string]TargetOutcome{
					"alice": {Calls: 1, Successes: 1},
					"":      {Calls: 3, Failures: 4, Blocked: 1, LastError: forkRefusal},
				}},
		},
		{
			// Once Frank's calls reach content validation, the refusal is
			// about his dossier, and so is a guard refusal of the repeat.
			name:      "a content rejection after a target refusal is the target's again",
			tool:      dossier,
			targetKey: byContact,
			script:    []scriptStep{{err: forkRefusal, targetRefused: true}, {err: rejection}},
			calls: []map[string]any{
				dossierArgs("frank", "f"),
				dossierArgs("frank", "g"), dossierArgs("frank", "g"), dossierArgs("frank", "g"), dossierArgs("frank", "g"),
			},
			want: ToolOutcome{Calls: 4, Failures: 5, Blocked: 1, LastError: rejection,
				Targets: map[string]TargetOutcome{
					"":      {Calls: 1, Failures: 1, LastError: forkRefusal},
					"frank": {Calls: 3, Failures: 4, Blocked: 1, LastError: rejection},
				}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg(nil, nil)
			cfg.Executor = &scriptedExecutor{scripts: map[string][]scriptStep{tc.tool: tc.script}}
			keyed := 0
			if tc.targetKey != nil {
				cfg.TargetKey = func(tool string, args map[string]any) string {
					keyed++
					return tc.targetKey(tool, args)
				}
			}
			calls := make([]llm.ToolCall, len(tc.calls))
			for i, args := range tc.calls {
				calls[i] = numberedCall(i, tc.tool, args)
			}
			result, _ := runScript(t, cfg, calls)

			if got := result.ToolOutcomes[tc.tool]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("outcome = %+v, want %+v", got, tc.want)
			}
			if tc.targetKey != nil && keyed != len(tc.calls) {
				t.Errorf("TargetKey called %d times, want %d: once per call, refused calls included", keyed, len(tc.calls))
			}
		})
	}
}

// TestUnchangedArgumentsNotePerTarget: the note compares a failed call
// only with the previous failure for the same document. Another
// target's landed write in between does not reset the comparison, and
// another target's failure is never compared with this one.
func TestUnchangedArgumentsNotePerTarget(t *testing.T) {
	const (
		dossier   = "contact_dossier_write"
		rejection = "status_line is 190 characters and the limit is 160"
		long      = "an over-long status line"
	)
	rejected := scriptStep{err: rejection}
	written := scriptStep{result: "written"}
	cases := []struct {
		name     string
		script   []scriptStep
		calls    []map[string]any
		wantNote []bool
	}{
		{
			name:     "another target's success between two rejections keeps the comparison",
			script:   []scriptStep{rejected, written, rejected},
			calls:    []map[string]any{dossierArgs("bob", long), dossierArgs("alice", "a"), dossierArgs("bob", long)},
			wantNote: []bool{false, false, true},
		},
		{
			name:     "a rejection is never compared with another target's",
			script:   []scriptStep{rejected, rejected},
			calls:    []map[string]any{dossierArgs("bob", long), dossierArgs("alice", long)},
			wantNote: []bool{false, false},
		},
		{
			name:     "the same target's success resets the comparison",
			script:   []scriptStep{rejected, written, rejected},
			calls:    []map[string]any{dossierArgs("bob", long), dossierArgs("bob", "short"), dossierArgs("bob", long)},
			wantNote: []bool{false, false, false},
		},
	}
	note := prompts.UnchangedArgumentsNote(dossier, "status_line")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg(nil, nil)
			cfg.Executor = &scriptedExecutor{scripts: map[string][]scriptStep{dossier: tc.script}}
			cfg.TargetKey = func(_ string, args map[string]any) string {
				id, _ := args["contact_id"].(string)
				return id
			}
			calls := make([]llm.ToolCall, len(tc.calls))
			for i, args := range tc.calls {
				calls[i] = numberedCall(i, dossier, args)
			}
			_, contents := runScript(t, cfg, calls)
			for i, want := range tc.wantNote {
				if got := strings.HasSuffix(contents[i], "\n\n"+note); got != want {
					t.Errorf("call %d carries the note = %v, want %v: %q", i, got, want, contents[i])
				}
			}
		})
	}
}
