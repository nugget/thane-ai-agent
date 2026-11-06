package llm

import (
	"fmt"
	"strings"
	"testing"
)

// namedRecords returns n ordinary data objects that carry a "name" key
// but are not calls, the shape of a tool result a model recaps.
func namedRecords(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(`{"name":"Light %d","state":"on"}`, i)
	}
	return out
}

// TestScanWireJSONCandidates pins which objects the JSON detector spends
// its candidate budget on, and how a call cut off at the end of the text
// bounds its own data.
func TestScanWireJSONCandidates(t *testing.T) {
	t.Parallel()

	contentCall := WireScan{Markup: WireMarkupJSONCall, Location: WireLocationContent, WellFormed: true, NameOffered: true, AfterProse: true}
	reasoningCall := WireScan{Markup: WireMarkupJSONCall, Location: WireLocationReasoning, WellFormed: true, NameOffered: true, AfterProse: true}
	malformedA := WireScan{Markup: WireMarkupJSONCall, Location: WireLocationContent}

	type row struct {
		name      string
		content   string
		reasoning string
		want      WireScan
	}
	var tests []row
	// Named data never spends the budget, however much of it precedes
	// the call.
	for _, n := range []int{15, 16, 17, 100} {
		records := namedRecords(n)
		wrapped := `{"results":[` + strings.Join(records, ",") + `]}`
		separate := strings.Join(records, "\n")
		tests = append(tests,
			row{
				name:    fmt.Sprintf("%d named records in a wrapper then a call", n),
				content: "Results: " + wrapped + "\nNow: " + jsonCall,
				want:    contentCall,
			},
			row{
				name:    fmt.Sprintf("%d separate named records then a call", n),
				content: "Results:\n" + separate + "\nNow: " + jsonCall,
				want:    contentCall,
			},
			row{
				name:      fmt.Sprintf("%d named records in a wrapper then a call in reasoning", n),
				content:   "Let me check.",
				reasoning: "The tool returned " + wrapped + " so I'll call " + jsonCall,
				want:      reasoningCall,
			},
			row{
				name:      fmt.Sprintf("%d separate named records then a call in reasoning", n),
				content:   "Let me check.",
				reasoning: "The tool returned " + separate + " so I'll call " + jsonCall,
				want:      reasoningCall,
			},
		)
	}
	tools := strings.TrimSuffix(strings.Repeat(`{"name":"get_state","description":"Read an entity's state."},`, 17), ",")
	decoys := func(n int) string { return strings.Repeat(`{"name":"a","arguments":[1]} `, n) }
	tests = append(tests,
		row{
			name:    "a recap of 17 tool definitions then a call",
			content: "Available: [" + tools + "] calling " + jsonCall,
			want:    contentCall,
		},
		// Call-shaped decoys do spend it: that is what keeps the scan
		// linear.
		row{
			name:    "15 call-shaped decoys then a call",
			content: decoys(15) + jsonCall,
			want:    contentCall,
		},
		row{
			name:    "16 call-shaped decoys hide a later call",
			content: decoys(16) + jsonCall,
			want:    malformedA,
		},
		// A cut-off call's text ends where its arguments value ends.
		row{
			name:    "a call after a cut-off call's array arguments is a separate call",
			content: "{\"name\":\"a\",\"arguments\":[1,{\"y\":\"]\"}]\n" + jsonCall,
			want:    contentCall,
		},
		row{
			name:    "a call right after a cut-off call's null arguments is a separate call",
			content: `{"name":"a","arguments":null` + jsonCall,
			want:    contentCall,
		},
		row{
			name:    "a call after a cut-off call's trailing comma is a separate call",
			content: "{\"name\":\"a\",\"arguments\":{\"x\":1},\n" + jsonCall,
			want:    contentCall,
		},
		row{
			name:    "a call after a cut-off call with arguments before its name is a separate call",
			content: "{\"arguments\":{\"x\":1},\"name\":\"a\"\n" + jsonCall,
			want:    contentCall,
		},
		row{
			name:    "a closed call nested in a second cut-off call is its data",
			content: "{\"name\":\"a\",\"arguments\":{}\n" + `{"name":"loop_create","arguments":{"name":"get_state","arguments":{}}`,
			want:    malformedA,
		},
		// Pinned: quotes are tracked inside braces, so an inch mark in a
		// prose brace opens a string that swallows the call after it.
		row{
			name:    "a stray quote inside a prose brace hides a later call",
			content: `Size {12" screen} then ` + jsonCall,
		},
	)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ScanWire(tt.content, tt.reasoning, wireOffered); got != tt.want {
				t.Fatalf("ScanWire() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
