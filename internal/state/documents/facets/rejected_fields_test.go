package facets

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// TestValidateMarksTheRejectedProjection pins that every payload violation
// carries the projection key at fault as typed metadata, so the runtime can
// tell which arguments a refusal was about without reading its prose. A
// contract that is itself malformed is not about the values and marks
// nothing.
func TestValidateMarksTheRejectedProjection(t *testing.T) {
	t.Parallel()

	valid := Payload{StatusLine: "Current.", Teaser: "Open for the hook.", Digest: "Enough to act on.", Full: "Complete detail."}
	tests := []struct {
		name string
		// contract is the contract validated, DefaultContract when it
		// declares no facets and malformed is unset.
		contract  Contract
		malformed bool
		mutate    func(*Payload)
		want      []string
	}{
		{
			name:   "over budget",
			mutate: func(p *Payload) { p.StatusLine = strings.Repeat("s", statusLineMaxRunes+31) },
			want:   []string{"status_line"},
		},
		{
			name:   "required projection left empty",
			mutate: func(p *Payload) { p.Digest = "  " },
			want:   []string{"digest"},
		},
		{
			name:   "line break in a single-line projection",
			mutate: func(p *Payload) { p.StatusLine = "one\ntwo" },
			want:   []string{"status_line"},
		},
		{
			name:   "reserved heading inside a projection",
			mutate: func(p *Payload) { p.Teaser = "## Digest\n\nsmuggled" },
			want:   []string{"teaser"},
		},
		{
			name:     "json projection that is not json",
			contract: Contract{Facets: []Spec{{Name: StatusLine}, {Name: Digest, Format: FormatJSON}}},
			mutate:   func(p *Payload) { p.Digest = "prose, not json" },
			want:     []string{"digest"},
		},
		{
			name:   "full over the ceiling",
			mutate: func(p *Payload) { p.Full = strings.Repeat("f", MaxDocumentBytes+1) },
			want:   []string{"full"},
		},
		{
			// full fits alone but the headings and projections push the
			// rendered document over; full is the only lever left.
			name:   "rendered document over the ceiling is charged to full",
			mutate: func(p *Payload) { p.Full = strings.Repeat("f", MaxDocumentBytes-8) },
			want:   []string{"full"},
		},
		{
			name: "every violating projection is marked",
			mutate: func(p *Payload) {
				p.StatusLine = strings.Repeat("s", statusLineMaxRunes+1)
				p.Teaser = ""
				p.Digest = strings.Repeat("d", digestMaxRunes+1)
			},
			want: []string{"digest", "status_line", "teaser"},
		},
		{
			name:      "a malformed contract marks nothing",
			malformed: true,
			mutate:    func(*Payload) {},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			contract := tt.contract
			if len(contract.Facets) == 0 && !tt.malformed {
				contract = DefaultContract()
			}
			payload := valid
			tt.mutate(&payload)
			err := contract.Validate(payload)
			if err == nil {
				t.Fatal("Validate() accepted an invalid payload")
			}
			if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments(Validate()) = %#v, want %#v (error %q)", got, tt.want, err)
			}
			// The shared refusal frame must keep every mark it wraps.
			framed := InvalidProjectionsError("faceted document projections", err)
			if got := toolargs.RejectedArguments(framed); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments(framed) = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestFromArgsMarksAValueThatIsNotAString pins the decode-side violation:
// a projection sent as another JSON type is refused by its key.
func TestFromArgsMarksAValueThatIsNotAString(t *testing.T) {
	t.Parallel()

	_, err := DefaultContract().FromArgs(map[string]any{"status_line": "Current.", "teaser": 5.0})
	if err == nil {
		t.Fatal("FromArgs() accepted a non-string teaser")
	}
	if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, []string{"teaser"}) {
		t.Errorf("RejectedArguments(FromArgs()) = %#v, want [teaser]", got)
	}
}
