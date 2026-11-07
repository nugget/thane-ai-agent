package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseQwenXMLCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []qwenXMLCall
	}{
		{
			name: "single call strips one newline around values",
			text: qwenCall,
			want: []qwenXMLCall{{
				start: len("<tool_call>\n"), name: "get_state", complete: true,
				params: []qwenXMLParam{{key: "entity_id", value: "light.kitchen"}},
			}},
		},
		{
			name: "multi-line value keeps inner whitespace and order",
			text: "<function=note_add>\n<parameter=text>\n  line one\nline two  \n\n</parameter>\n<parameter=tags>\n[\"a\"]\n</parameter>\n</function>",
			want: []qwenXMLCall{{
				name: "note_add", complete: true,
				params: []qwenXMLParam{
					{key: "text", value: "  line one\nline two  \n"},
					{key: "tags", value: `["a"]`},
				},
			}},
		},
		{
			name: "two calls",
			text: "<tool_call>\n<function=get_state>\n</function>\n</tool_call>\n<tool_call>\n<function=get_weather>\n<parameter=location>\nParis\n</parameter>\n</function>\n</tool_call>",
			want: []qwenXMLCall{
				{start: len("<tool_call>\n"), name: "get_state", complete: true},
				{
					start: strings.Index("<tool_call>\n<function=get_state>\n</function>\n</tool_call>\n<tool_call>\n<function=get_weather>", "<function=get_weather>"),
					name:  "get_weather", complete: true,
					params: []qwenXMLParam{{key: "location", value: "Paris"}},
				},
			},
		},
		{
			name: "closers missing at end of text",
			text: "<function=get_state>\n<parameter=entity_id>\nlight.kitchen\n</parameter>",
			want: []qwenXMLCall{{
				name: "get_state", complete: true,
				params: []qwenXMLParam{{key: "entity_id", value: "light.kitchen"}},
			}},
		},
		{
			name: "truncated value keeps closed params and is incomplete",
			text: "<function=call_service>\n<parameter=domain>\nlight\n</parameter>\n<parameter=service>\nturn_o",
			want: []qwenXMLCall{{
				name:   "call_service",
				params: []qwenXMLParam{{key: "domain", value: "light"}},
			}},
		},
		{
			name: "truncated header",
			text: "<function=get_st",
			want: []qwenXMLCall{{name: "get_st"}},
		},
		{
			name: "free-text body is incomplete",
			text: "<function=get_state>\nentity_id: light.kitchen\n</function>",
			want: []qwenXMLCall{{name: "get_state"}},
		},
		{
			name: "text between parameters is incomplete",
			text: "<function=call_service>\n<parameter=domain>\nlight\n</parameter>\nand then\n<parameter=service>\nturn_on\n</parameter>\n</function>",
			want: []qwenXMLCall{{
				name:   "call_service",
				params: []qwenXMLParam{{key: "domain", value: "light"}},
			}},
		},
		{
			name: "prose after the last parameter at end of text is incomplete",
			text: "<function=get_state>\n<parameter=entity_id>\nlight.kitchen\n</parameter>\nThe light is on.",
			want: []qwenXMLCall{{
				name:   "get_state",
				params: []qwenXMLParam{{key: "entity_id", value: "light.kitchen"}},
			}},
		},
		{
			name: "empty parameter key",
			text: "<function=get_state>\n<parameter=>\nx\n</parameter>\n</function>",
			want: []qwenXMLCall{{name: "get_state"}},
		},
		{
			name: "no markup",
			text: "call get_state with entity_id light.kitchen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseQwenXMLCalls(tt.text)
			// Value spans are pinned by TestParseQwenXMLValueSpans.
			for i := range got {
				got[i].values = nil
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseQwenXMLCalls() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestParseQwenXMLValueSpans pins the spans a call's argument data
// occupies, which is what keeps markup quoted in a value from being
// read as a call. A cut-off or unkeyed value is data all the same.
func TestParseQwenXMLValueSpans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "closed values", text: qwenCall, want: []string{"\nlight.kitchen\n"}},
		{
			name: "value cut off by the end of the text",
			text: "<function=note_add>\n<parameter=text>\n{\"name\":\"get_state\"",
			want: []string{"\n{\"name\":\"get_state\""},
		},
		{
			name: "value running into the next parameter, and the values after it",
			text: "<function=note_add>\n<parameter=a>\nx\n<parameter=b>\ny\n</parameter>",
			want: []string{"\nx\n", "\ny\n"},
		},
		{
			name: "values after free text in the body",
			text: "<function=note_add>\njunk\n<parameter=text>\ny\n</parameter>\n</function>",
			want: []string{"\ny\n"},
		},
		{name: "unkeyed value", text: "<function=get_state>\n<parameter=>\nx\n</parameter>\n</function>", want: []string{"\nx\n"}},
		{name: "cut-off header has no value", text: "<function=note_add>\n<parameter=te"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := parseQwenXMLCalls(tt.text)
			if len(calls) != 1 {
				t.Fatalf("parseQwenXMLCalls() = %d calls, want 1", len(calls))
			}
			var got []string
			for _, v := range calls[0].values {
				got = append(got, tt.text[v.start:v.end])
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("value spans = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestQwenXMLCandidatesStopAtLimit proves detection parses no more than
// its candidate limit: allocations stay flat however many calls follow.
func TestQwenXMLCandidatesStopAtLimit(t *testing.T) {
	few := strings.Repeat(qwenCall, maxWireCandidates)
	many := strings.Repeat(qwenCall, 50*maxWireCandidates)
	if got := len(qwenXMLCandidates(many)); got != maxWireCandidates {
		t.Fatalf("qwenXMLCandidates() = %d candidates, want %d", got, maxWireCandidates)
	}
	fewAllocs := testing.AllocsPerRun(20, func() { qwenXMLCandidates(few) })
	manyAllocs := testing.AllocsPerRun(20, func() { qwenXMLCandidates(many) })
	if manyAllocs != fewAllocs {
		t.Fatalf("allocations grew with the calls after the limit: %v for %d calls, %v for %d",
			fewAllocs, maxWireCandidates, manyAllocs, 50*maxWireCandidates)
	}
}
