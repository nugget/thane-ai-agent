package llm

import (
	"encoding/json"
	"math"
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
			name:    "qwen free-text body is not a parameter",
			content: "<function=get_state>\nentity_id: light.kitchen\n</function>",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen prose after the last parameter with no closer",
			content: "<function=get_state>\n<parameter=entity_id>\nlight.kitchen\n</parameter>\nThe light is on, done.",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen prose quoting the function tag",
			content: "The Qwen format wraps calls as <function=name> tags.",
			want:    contentScan(WireMarkupQwenXML, false, false, true),
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
			name:    "json truncated inside arguments that carry their own name",
			content: `{"name":"note_add","arguments":{"name":"Alice","sched`,
			want:    contentScan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "json truncated inside nested service data with a name",
			content: `{"name":"call_service","arguments":{"domain":"light","service_data":{"name":"Kitchen","bri`,
			want:    contentScan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "json truncated with only the name key so far",
			content: `{"name":"get_sta`,
			want:    contentScan(WireMarkupJSONCall, false, false, false),
		},
		{
			name:    "json truncated after the name, inside the arguments key",
			content: `{"name":"get_state","argu`,
			want:    contentScan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "json truncated object with a name key is not a call",
			content: `{"name": "Alice", "email": "alice@exa`,
		},
		{
			name:    "json truncated after a stray prose brace",
			content: `Use the {braces as shown: {"name":"get_state","arguments":{"entity`,
			want:    contentScan(WireMarkupJSONCall, false, true, true),
		},
		{
			name:    "json closed call nested in an unclosed call is its argument",
			content: `{"name":"loop_create","arguments":{"name":"get_state","arguments":{}}`,
			want:    contentScan(WireMarkupJSONCall, false, false, false),
		},
		{
			name:    "json call after an unclosed call's closed arguments is a separate call",
			content: "{\"name\":\"loop_create\",\"arguments\":{\"x\":1}\nSorry, retrying:\n" + `{"name":"get_state","arguments":{}}`,
			want:    contentScan(WireMarkupJSONCall, true, true, true),
		},
		{
			name:    "json call right after an unclosed call's closed arguments is a separate call",
			content: "{\"name\":\"loop_create\",\"arguments\":{}\n" + `{"name":"get_state","arguments":{}}`,
			want:    contentScan(WireMarkupJSONCall, true, true, true),
		},
		{
			name:    "json closed call in a later key of an unclosed call is its data",
			content: `{"name":"loop_create","arguments":{},"spec":{"name":"get_state","arguments":{}}`,
			want:    contentScan(WireMarkupJSONCall, false, false, false),
		},
		{
			name:    "json closed call before an unclosed call's arguments is its data",
			content: `{"name":"loop_create","spec":{"name":"get_state","arguments":{}},"arguments":{`,
			want:    contentScan(WireMarkupJSONCall, false, false, false),
		},
		{
			name:    "json closed call after an unclosed stray prose brace",
			content: `Use {braces like this: {"name":"get_state","arguments":{}}`,
			want:    contentScan(WireMarkupJSONCall, true, true, true),
		},
		{
			name:    "json truncated after a closed name with no arguments key is not a call",
			content: `{"name":"Alice"`,
		},
		{
			name:    "json truncated right after the name key's colon",
			content: `{"name":`,
			want:    contentScan(WireMarkupJSONCall, false, false, false),
		},
		{
			name:    "json truncated after a closed name and a comma is not a call",
			content: `{"name":"Alice", `,
		},
		{
			name:    "json truncated inside a second key that is not arguments is not a call",
			content: `{"name":"Alice","em`,
		},
		{
			name:      "json truncated name-only data in content leaves a reasoning call visible",
			content:   `{"name":"Alice"`,
			reasoning: jsonCall,
			want:      WireScan{Markup: WireMarkupJSONCall, Location: WireLocationReasoning, WellFormed: true, NameOffered: true},
		},
		{
			name:    "json truncated after the arguments key, before its colon",
			content: `{"name":"get_state","arguments"`,
			want:    contentScan(WireMarkupJSONCall, false, true, false),
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
			// The stripped role header runs "commentary" into the name.
			// The suffix is deliberately not trimmed: doing so would
			// shorten real tool names that end in it.
			name:    "harmony stripped role header keeps the commentary suffix on the name",
			content: "assistant to=functions.get_weathercommentary json{\"location\":\"Paris\"}",
			want:    contentScan(WireMarkupHarmony, true, false, false),
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
		{
			name:    "harmony argument-less recipient does not borrow the next one's arguments",
			content: "to=functions.unknown to=functions.get_weather {}",
			want:    contentScan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "harmony tokenised argument-less header does not borrow the next message",
			content: "<|channel|>commentary to=functions.unknown <|constrain|>json<|start|>assistant<|channel|>commentary to=functions.get_weather <|message|>{}",
			want:    contentScan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "harmony only an argument-less recipient before another is malformed",
			content: "to=functions.get_weather to=functions.",
			want:    contentScan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "qwen partial parameter opener after the last parameter",
			content: "<function=call_service><parameter=domain>light</parameter><parameter",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen parameter key cut off after the last parameter",
			content: "<function=call_service><parameter=domain>light</parameter><parameter=serv",
			want:    contentScan(WireMarkupQwenXML, false, true, false),
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
		{"empty tool_call tags", func(n int) string { return strings.Repeat("<tool_call></tool_call>", n) }},
		{"cut-off calls", func(n int) string { return strings.Repeat(`{"name":"a","arguments":{}`, n) }},
		{"qwen calls in a JSON call's arguments", func(n int) string {
			return `{"name":"a","arguments":"` + strings.Repeat("<function=b></function>", n) + `"}`
		}},
		{"JSON calls in a cut-off qwen value", func(n int) string { return "<function=a>\n<parameter=b>\n" + strings.Repeat(jsonCall, n) }},
		{"prose with stray braces", func(n int) string { return strings.Repeat(`a {"name": b `, n) }},
		{"cut-off name-only objects before trailing space", func(n int) string {
			return strings.Repeat(`{"name":"x",{`, n) + strings.Repeat(" ", n)
		}},
		{"malformed function headers before trailing space", func(n int) string {
			return strings.Repeat("<function=a\n", n) + strings.Repeat(" ", n)
		}},
		{"named data objects before a call", func(n int) string {
			return strings.Repeat(`{"name":"x","state":"on"},`, n) + jsonCall
		}},
		// Every level is a candidate that parses but is not a call, so
		// only the candidate cap keeps this linear. Depth stays under the
		// JSON decoder's nesting limit, which would otherwise end it early.
		{"nested call-shaped decoys", func(n int) string {
			return strings.Repeat(`{"name":"a","x":`, n/4) + `{"arguments":1}` + strings.Repeat("}", n/4)
		}},
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
