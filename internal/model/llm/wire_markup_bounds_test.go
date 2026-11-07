package llm

import (
	"strings"
	"testing"
)

// TestScanWireCallBounds pins where one call ends and the next can
// begin: markup inside a call's argument data, in any grammar, is data;
// data nested that way never reaches past its container; a Harmony
// call's arguments are the value its header leads to; a malformed call
// is named by its own top-level key; and the earliest malformed markup
// is described when nothing is well-formed.
func TestScanWireCallBounds(t *testing.T) {
	t.Parallel()

	scan := func(m WireMarkup, wellFormed, offered, afterProse bool) WireScan {
		return WireScan{Markup: m, Location: WireLocationContent, WellFormed: wellFormed, NameOffered: offered, AfterProse: afterProse}
	}
	stringified := scan(WireMarkupJSONCall, false, true, false)
	stringified.StringifiedArgs = true
	const qwenGetState = "<tool_call>\n<function=get_state>\n<parameter=entity_id>\nlight.kitchen\n</parameter>\n</function>\n</tool_call>"

	tests := []struct {
		name    string
		content string
		offered []string
		want    WireScan
	}{
		// Argument data is never a call.
		{
			name:    "qwen markup in a JSON call's string arguments",
			content: `{"name":"note_add","arguments":"<function=get_state></function>"}`,
			want:    stringified,
		},
		{
			name:    "JSON call in a cut-off qwen parameter value",
			content: "<function=note_add>\n<parameter=text>\n" + jsonCall,
			want:    scan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "JSON call in a qwen value after the body stopped parsing",
			content: "<tool_call>\n<function=note_add>\n<parameter=title>\nx\n<parameter=text>\n" + jsonCall + "\n</parameter>\n</function>\n</tool_call>",
			want:    scan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "qwen markup in a cut-off JSON call's arguments",
			content: `{"name":"note_add","arguments":{"text":"<function=get_state></function>"}`,
			want:    scan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "qwen markup in a second cut-off JSON call's arguments",
			content: `{"name":"note_add","arguments":{}, {"name":"note_add","arguments":{"t":"<function=get_state></function>"}`,
			want:    scan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "qwen markup in a cut-off harmony call's arguments",
			content: `to=functions.note_add {"text":"<function=get_state></function>"`,
			want:    scan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "qwen markup in harmony array arguments",
			content: `to=functions.note_add json[{"text":"<function=get_state></function>"}]`,
			want:    scan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "harmony recipient in a cut-off harmony call's arguments",
			content: `to=functions.unknown {"text":"to=functions.get_state {}"`,
			want:    scan(WireMarkupHarmony, false, false, false),
		},
		// Nested data stays inside its container, so a separate call
		// after the container is still found.
		{
			name:    "a call after another call's nested markup",
			content: `{"name":"note_add","arguments":"<function=get_state></function>"} ` + qwenCall,
			want:    scan(WireMarkupQwenXML, true, true, true),
		},
		{
			name:    "a call after a cut-off qwen value nested in a JSON string",
			content: `{"name":"note_add","arguments":"<function=a><parameter=x>foo"} ` + jsonCall,
			want:    scan(WireMarkupJSONCall, true, true, true),
		},
		{
			name:    "a recipient after a cut-off harmony value nested in a JSON string",
			content: `{"name":"note_add","arguments":"to=functions.x {"} to=functions.get_state {"entity_id":"light.kitchen"}`,
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "a call after a cut-off JSON object nested in a qwen value",
			content: "<tool_call>\n<function=note_add>\n<parameter=text>\n{\"name\": \"get_state\", \"arguments\": {\n</function>\n</tool_call>\n" + qwenGetState,
			want:    scan(WireMarkupQwenXML, true, true, true),
		},
		{
			name:    "a JSON call after a malformed qwen call whose value holds a cut-off object",
			content: "<function=note_add>\n<parameter=text>\n{\"name\":\"x\",\"arguments\":{\n</parameter>\nstray\n</function>\n" + jsonCall,
			want:    scan(WireMarkupJSONCall, true, true, true),
		},
		// A harmony value that never closes ends with its message.
		{
			name:    "a recipient after unclosed harmony arguments",
			content: "to=functions.note_add {\"x\": 1\nto=functions.get_state {\"entity_id\":\"light.kitchen\"}",
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "a tokenised call after one cut off before its call token",
			content: "<|channel|>commentary to=functions.note_add <|constrain|>json<|message|>{\"text\":\"hi\"<|call|>\n<|start|>assistant<|channel|>commentary to=functions.get_weather <|constrain|>json<|message|>{\"location\":\"Paris\"}<|call|>",
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		// Harmony arguments are the value the header leads to.
		{
			name:    "harmony array arguments holding an object",
			content: `to=functions.get_weather json[{"location":"Paris"}]`,
			want:    scan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "harmony message token quoted in the arguments",
			content: `to=functions.note_add json{"text":"<|message|>"}`,
			want:    scan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "harmony code content type",
			content: `<|channel|>commentary to=functions.get_weather code<|message|>{"location":"Paris"}<|call|>`,
			want:    scan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "harmony uppercase constraint",
			content: `<|channel|>commentary to=functions.get_weather <|constrain|>JSON<|message|>{"location":"Paris"}<|call|>`,
			want:    scan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "harmony call on the analysis channel",
			content: `<|start|>assistant<|channel|>analysis to=functions.get_weather <|constrain|>json<|message|>{"location":"Paris"}<|call|>`,
			want:    scan(WireMarkupHarmony, true, true, false),
		},
		{
			name:    "recipients quoted in a closed harmony value are its data",
			content: `to=functions.note_add {"t":"` + strings.Repeat(`to=functions.x {} `, 3) + `", bad} to=functions.get_state {}`,
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "a recipient after one quoted in a JSON string",
			content: `{"name":"note_add","arguments":"see to=functions.x"} to=functions.get_state {"entity_id":"light.kitchen"}`,
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		{
			// The nested recipient's value is read with its quotes paired
			// the wrong way, so it closes past the next real recipient.
			name:    "a recipient after one quoted in an unclosed harmony value",
			content: "to=functions.note_add {\"text\":\"to=functions.x {\"\nto=functions.note_add {\"text\":\"}\"}",
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "prose quoting a recipient before a call",
			content: "I'll use \"to=functions.get_state\" instead.\n" + qwenGetState,
			want:    scan(WireMarkupQwenXML, true, true, true),
		},
		{
			name:    "an end token in a string ends an unclosed harmony value",
			content: "to=functions.note_add {\"text\":\"hi<|call|>\n<|start|>assistant<|channel|>commentary to=functions.get_weather <|constrain|>json<|message|>{\"location\":\"Paris\"}<|call|>",
			want:    scan(WireMarkupHarmony, true, true, true),
		},
		{
			name:    "a tag outside a string ends an unclosed harmony value",
			content: "to=functions.note_add {\"x\": 1<|channel|>" + qwenGetState,
			want:    scan(WireMarkupQwenXML, true, true, true),
		},
		{
			name:    "a recipient's message ending before any arguments",
			content: `<|channel|>commentary to=functions.get_state <|constrain|>json<|message|><|call|><|start|>assistant<|channel|>final<|message|>{"done":true}`,
			want:    scan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "JSON call in a qwen value after a broken parameter header",
			content: "<function=note_add>\n<parameter=a\n<parameter=text>\n" + jsonCall + "\n</parameter>\n</function>",
			want:    scan(WireMarkupQwenXML, false, true, false),
		},
		{
			// Past the candidate limit a cut-off call marks no data, so
			// the objects inside it are skipped instead.
			name:    "a closed call nested in a cut-off call past the limit",
			content: strings.Repeat("{\"name\":\"a\",\"arguments\":{}\n", maxWireCandidates) + `{"name":"loop_create","arguments":{"name":"get_state","arguments":{}}`,
			want:    scan(WireMarkupJSONCall, false, false, false),
		},
		// A malformed call is named by its own top-level key.
		{
			name:    "invalid JSON call with arguments before its name",
			content: `{"arguments":{"name":"get_state"},"name":"call_service",}`,
			offered: []string{"call_service"},
			want:    scan(WireMarkupJSONCall, false, true, false),
		},
		// The earliest malformed markup wins when nothing is well-formed.
		{
			name:    "an empty tool_call tag before a cut-off function",
			content: "<tool_call></tool_call>\n<function=get_state",
			want:    scan(WireMarkupQwenXML, false, false, false),
		},
		{
			name:    "a cut-off function before an empty tool_call tag",
			content: "<function=get_state\n<tool_call></tool_call>",
			want:    scan(WireMarkupQwenXML, false, true, false),
		},
		{
			name:    "a tool_call tag holding a function is that function's wrapper",
			content: "Checking.\n<tool_call>\n<function=get_state",
			want:    scan(WireMarkupQwenXML, false, true, true),
		},
		{
			name:    "a tool_call tag holding a function after prose is still its wrapper",
			content: "<tool_call>\nI will check.\n<function=get_state",
			want:    scan(WireMarkupQwenXML, false, true, true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			offered := tt.offered
			if offered == nil {
				offered = wireOffered
			}
			if got := ScanWire(tt.content, "", offered); got != tt.want {
				t.Fatalf("ScanWire() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
