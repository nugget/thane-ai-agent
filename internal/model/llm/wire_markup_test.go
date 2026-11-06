package llm

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

var wireOffered = []string{"get_state", "call_service", "get_weather", "note_add"}

const (
	qwenCall = "<tool_call>\n<function=get_state>\n<parameter=entity_id>\nlight.kitchen\n</parameter>\n</function>\n</tool_call>"
	jsonCall = `{"name":"get_state","arguments":{"entity_id":"light.kitchen"}}`

	harmonyTokenised = "<|start|>assistant<|channel|>commentary to=functions.get_weather <|constrain|>json<|message|>{\"location\":\"Paris\"}<|call|>"
)

func TestScanWire(t *testing.T) {
	t.Parallel()

	contentScan := func(m WireMarkup, wellFormed, offered, afterProse bool) WireScan {
		return WireScan{Markup: m, Location: WireLocationContent, WellFormed: wellFormed, NameOffered: offered, AfterProse: afterProse}
	}

	tests := []struct {
		name      string
		content   string
		reasoning string
		offered   []string
		want      WireScan
	}{
		// qwen_xml
		{
			name:    "qwen well-formed whole text",
			content: qwenCall,
			want:    contentScan(WireMarkupQwenXML, true, true, false),
		},
		{
			name:    "qwen well-formed after prose",
			content: "Let me check the kitchen light.\n\n" + qwenCall,
			want:    contentScan(WireMarkupQwenXML, true, true, true),
		},
		{
			name:    "qwen closing tags consumed at end of text",
			content: "<tool_call>\n<function=get_state>\n<parameter=entity_id>\nlight.kitchen\n</parameter>\n",
			want:    contentScan(WireMarkupQwenXML, true, true, false),
		},
		{
			name:    "qwen without tool_call wrapper",
			content: "<function=call_service>\n<parameter=domain>\nlight\n</parameter>\n<parameter=service>\nturn_on\n</parameter>\n</function>",
			want:    contentScan(WireMarkupQwenXML, true, true, false),
		},
		{
			name:    "qwen fenced as xml",
			content: "```xml\n" + qwenCall + "\n```",
			want:    contentScan(WireMarkupQwenXML, true, true, false),
		},
		{
			name:    "qwen truncated inside parameter value",
			content: "<tool_call>\n<function=get_state>\n<parameter=entity_id>\nlight.kit",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen truncated inside function name",
			content: "Checking.\n<tool_call>\n<function=get_st",
			want:    contentScan(WireMarkupQwenXML, false, false, true),
		},
		{
			name:    "qwen parameter runs into the next parameter",
			content: "<function=call_service>\n<parameter=domain>\nlight\n<parameter=service>\nturn_on\n</parameter>\n</function>",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen function never closed before the next call",
			content: "<tool_call>\n<function=note_add>\n<parameter=text>\nhi\n</parameter>\n<tool_call>\n<function=get_st",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen unoffered name",
			content: "<tool_call>\n<function=delete_everything>\n</function>\n</tool_call>",
			want:    contentScan(WireMarkupQwenXML, true, false, false),
		},
		{
			name:    "qwen bare tool_call tag cut off",
			content: "I'll look that up.\n<tool_call>\n",
			want:    contentScan(WireMarkupQwenXML, false, false, true),
		},
		{
			name:    "qwen later well-formed call beats earlier truncated one",
			content: "<function=get_st\n\n" + qwenCall,
			want:    contentScan(WireMarkupQwenXML, true, true, true),
		},

		// json_call
		{
			name:    "json whole text",
			content: jsonCall,
			want:    contentScan(WireMarkupJSONCall, true, true, false),
		},
		{
			name:    "json after prose",
			content: "Sure, checking now. " + jsonCall,
			want:    contentScan(WireMarkupJSONCall, true, true, true),
		},
		{
			name:    "json fenced whole text",
			content: "```json\n" + jsonCall + "\n```",
			want:    contentScan(WireMarkupJSONCall, true, true, false),
		},
		{
			name:    "json fenced after prose",
			content: "I'll check the light.\n\n```tool\n" + jsonCall + "\n```",
			want:    contentScan(WireMarkupJSONCall, true, true, true),
		},
		{
			name:    "json hermes tool_call tag",
			content: "<tool_call>\n" + jsonCall + "\n</tool_call>",
			want:    contentScan(WireMarkupJSONCall, true, true, false),
		},
		{
			name:    "json array of calls",
			content: "[" + jsonCall + `,{"name":"note_add","arguments":{"text":"x"}}]`,
			want:    contentScan(WireMarkupJSONCall, true, true, false),
		},
		{
			name:    "json braces inside string values",
			content: `{"name":"note_add","arguments":{"text":"use } and { freely"}}`,
			want:    contentScan(WireMarkupJSONCall, true, true, false),
		},
		{
			name:    "json null arguments",
			content: `{"name":"get_weather","arguments":null}`,
			want:    contentScan(WireMarkupJSONCall, true, true, false),
		},
		{
			name:    "json nested name key is an argument, not a call",
			content: `{"name":"loop_create","arguments":{"name":"get_state","arguments":"x"}}`,
			want:    contentScan(WireMarkupJSONCall, true, false, false),
		},
		{
			name:    "json stringified arguments",
			content: `{"name":"get_state","arguments":"{\"entity_id\":\"light.kitchen\"}"}`,
			want: WireScan{
				Markup: WireMarkupJSONCall, Location: WireLocationContent,
				NameOffered: true, StringifiedArgs: true,
			},
		},
		{
			name:    "json stringified arguments after prose",
			content: `Calling it: {"name":"get_state","arguments":"{}"}`,
			want: WireScan{
				Markup: WireMarkupJSONCall, Location: WireLocationContent,
				NameOffered: true, AfterProse: true, StringifiedArgs: true,
			},
		},
		{
			name:    "json empty name",
			content: `{"name":"","arguments":{}}`,
			want:    contentScan(WireMarkupJSONCall, false, false, false),
		},
		{
			name:    "json balanced but invalid",
			content: `{"name": "get_state", "arguments": {'entity_id': 'light.kitchen'}}`,
			want:    contentScan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "json truncated",
			content: `Checking. {"name":"get_state","arguments":{"entity_id":"light.ki`,
			want:    contentScan(WireMarkupJSONCall, false, true, true),
		},
		{
			name:    "json truncated after a stray prose brace",
			content: `Use the {braces as shown: {"name":"get_state","arguments":{"entity`,
			want:    contentScan(WireMarkupJSONCall, false, true, true),
		},
		{
			name:    "json after an unmatched quote in prose",
			content: `He said "hello. ` + jsonCall,
			want:    contentScan(WireMarkupJSONCall, true, true, true),
		},

		// harmony
		{
			name:    "harmony tokenised",
			content: harmonyTokenised,
			want:    contentScan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "harmony de-tokenised",
			content: "assistantcommentary to=functions.get_weather json{\"location\":\"Paris\"}",
			want:    contentScan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "harmony recipient in the role header",
			content: "<|start|>assistant to=functions.get_weather<|channel|>commentary json<|message|>{\"location\":\"Paris\"}<|call|>",
			want:    contentScan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "harmony after analysis",
			content: "<|channel|>analysis<|message|>Need the weather.<|end|>" + harmonyTokenised,
			want:    contentScan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "harmony truncated arguments",
			content: "<|channel|>commentary to=functions.get_weather <|constrain|>json<|message|>{\"location\":\"Par",
			want:    contentScan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "harmony without arguments",
			content: "commentary to=functions.get_weather",
			want:    contentScan(WireMarkupHarmony, false, true, false),
		},

		// prose and other non-calls
		{
			name:    "prose mentioning function names",
			content: "I can use get_state or call_service to check the kitchen light, and get_weather for the forecast. The function name goes in the \"name\" field.",
		},
		{
			name:    "prose with a JSON object that is not a call",
			content: `The light reports {"entity_id": "light.kitchen", "state": "on"}.`,
		},
		{
			name:    "code with name and arguments identifiers",
			content: "function f() { return {name: 1, arguments: 2}; }",
		},
		{
			name:    "markdown code fence with ordinary code",
			content: "```go\nfunc main() { fmt.Println(\"hi\") }\n```",
		},
		{
			name: "empty channels",
		},

		// channels
		{
			name:      "call in reasoning only",
			content:   "The kitchen light is on.",
			reasoning: "I should verify first.\n" + qwenCall,
			want:      WireScan{Markup: WireMarkupQwenXML, Location: WireLocationReasoning, WellFormed: true, NameOffered: true, AfterProse: true},
		},
		{
			name:      "call in reasoning with empty content",
			reasoning: jsonCall,
			want:      WireScan{Markup: WireMarkupJSONCall, Location: WireLocationReasoning, WellFormed: true, NameOffered: true},
		},
		{
			name:      "content markup wins over reasoning even when malformed",
			content:   `{"name":"get_state","arguments":"{}"}`,
			reasoning: qwenCall,
			want:      WireScan{Markup: WireMarkupJSONCall, Location: WireLocationContent, NameOffered: true, StringifiedArgs: true},
		},
		{
			name:    "well-formed qwen beats earlier malformed json",
			content: `{"name":"get_state","arguments":"{}"} then ` + qwenCall,
			want:    contentScan(WireMarkupQwenXML, true, true, true),
		},
		{
			name:    "nothing offered means no name is offered",
			content: jsonCall,
			offered: []string{},
			want:    contentScan(WireMarkupJSONCall, true, false, false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			offered := wireOffered
			if tt.offered != nil {
				offered = tt.offered
			}
			got := ScanWire(tt.content, tt.reasoning, offered)
			if got != tt.want {
				t.Fatalf("ScanWire() = %+v, want %+v", got, tt.want)
			}
			if got.Found() != (tt.want.Markup != "") {
				t.Fatalf("Found() = %v, want %v", got.Found(), tt.want.Markup != "")
			}
		})
	}
}

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
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseQwenXMLCalls() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWireScanJSONShape(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(ScanWire("", jsonCall, wireOffered))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"markup":"json_call","location":"reasoning","well_formed":true,"name_offered":true,"after_prose":false,"stringified_args":false}`
	if string(got) != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

// TestScanWireScalesLinearly guards the detectors' bounds. Runaway
// text full of braces, tags or recipients must cost time in proportion
// to its length. Quadruple the input and a linear scan takes about four
// times as long, where anything quadratic takes sixteen; the bound sits
// between them, so the machine and the race detector cancel in the
// ratio and only a change in complexity trips it.
func TestScanWireScalesLinearly(t *testing.T) {
	const (
		base   = 5_000
		factor = 4
		bound  = 10.0
	)
	inputs := []struct {
		name string
		text func(n int) string
	}{
		{"open braces", func(n int) string { return strings.Repeat("{", n) + `"name":"x"` }},
		{"nested name objects", func(n int) string { return strings.Repeat(`{"name":`, n) + strings.Repeat("}", n) }},
		{"unclosed functions", func(n int) string { return strings.Repeat("<function=a>\n<parameter=b>\n", n) }},
		{"bare tool_call tags", func(n int) string { return strings.Repeat("<tool_call>{", n) }},
		{"harmony recipients", func(n int) string { return strings.Repeat("to=functions.a {", n) }},
		{"prose with stray braces", func(n int) string { return strings.Repeat(`a {"name": b `, n) }},
	}
	// fastest of a few runs, so a scheduler hiccup cannot fake a slope.
	measure := func(text string) time.Duration {
		best := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			ScanWire(text, text, wireOffered)
			best = min(best, time.Since(start))
		}
		return max(best, time.Microsecond)
	}
	for _, in := range inputs {
		t.Run(in.name, func(t *testing.T) {
			small := measure(in.text(base))
			large := measure(in.text(base * factor))
			if ratio := float64(large) / float64(small); ratio > bound {
				t.Fatalf("x%d input took x%.1f as long (%v -> %v), want under x%.0f", factor, ratio, small, large, bound)
			}
		})
	}
}
