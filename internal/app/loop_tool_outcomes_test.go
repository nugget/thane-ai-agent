package app

import (
	"reflect"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestLoopToolOutcomesCarriesTargets: the per-target breakdown crosses
// into the loop's mirror intact, because the loop judges each document
// a wake wrote on its own.
func TestLoopToolOutcomesCarriesTargets(t *testing.T) {
	const rejection = "status_line is 190 characters and the limit is 160"
	cases := []struct {
		name string
		src  map[string]iterate.ToolOutcome
		want map[string]looppkg.ToolOutcome
	}{
		{name: "no tools called"},
		{
			name: "per-target breakdown",
			src: map[string]iterate.ToolOutcome{"contact_dossier_write": {
				Calls: 4, Failures: 4, Blocked: 1, Successes: 1, LastError: rejection,
				Targets: map[string]iterate.TargetOutcome{
					"alice": {Calls: 1, Successes: 1},
					"bob":   {Calls: 3, Failures: 4, Blocked: 1, LastError: rejection},
				},
			}},
			want: map[string]looppkg.ToolOutcome{"contact_dossier_write": {
				Calls: 4, Failures: 4, Blocked: 1, Successes: 1, LastError: rejection,
				Targets: map[string]looppkg.TargetOutcome{
					"alice": {Calls: 1, Successes: 1},
					"bob":   {Calls: 3, Failures: 4, Blocked: 1, LastError: rejection},
				},
			}},
		},
		{
			name: "an outcome without a breakdown",
			src:  map[string]iterate.ToolOutcome{"web_fetch": {Calls: 1, Successes: 1}},
			want: map[string]looppkg.ToolOutcome{"web_fetch": {Calls: 1, Successes: 1}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loopToolOutcomes(tc.src); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("loopToolOutcomes = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCompileLoopAgentRequestCarriesTargetKey: the loop's target
// function reaches the agent request, and an unset one stays unset.
func TestCompileLoopAgentRequestCarriesTargetKey(t *testing.T) {
	byContact := func(tool string, args map[string]any) string {
		id, _ := args["contact_id"].(string)
		return tool + ":" + id
	}
	cases := []struct {
		name      string
		targetKey func(string, map[string]any) string
		want      string
	}{
		{name: "set", targetKey: byContact, want: "contact_dossier_write:alice"},
		{name: "unset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compileLoopAgentRequest(looppkg.Request{TargetKey: tc.targetKey}).TargetKey
			if tc.targetKey == nil {
				if got != nil {
					t.Error("TargetKey set on the agent request, want nil")
				}
				return
			}
			if got == nil {
				t.Fatal("TargetKey dropped from the agent request")
			}
			if key := got("contact_dossier_write", map[string]any{"contact_id": "alice"}); key != tc.want {
				t.Errorf("TargetKey = %q, want %q", key, tc.want)
			}
		})
	}
}
