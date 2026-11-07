package llm

import "testing"

// TestScanWireCallBounds pins where one call ends and the next can
// begin: markup inside a call's argument data, in any grammar, is data;
// a Harmony call's arguments must be an object of their own; a
// malformed call is named by its own top-level key; and the earliest
// malformed markup is described when nothing is well-formed.
func TestScanWireCallBounds(t *testing.T) {
	t.Parallel()

	scan := func(m WireMarkup, wellFormed, offered, afterProse bool) WireScan {
		return WireScan{Markup: m, Location: WireLocationContent, WellFormed: wellFormed, NameOffered: offered, AfterProse: afterProse}
	}
	stringified := scan(WireMarkupJSONCall, false, true, false)
	stringified.StringifiedArgs = true

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
			name:    "qwen markup in a cut-off JSON call's arguments",
			content: `{"name":"note_add","arguments":{"text":"<function=get_state></function>"}`,
			want:    scan(WireMarkupJSONCall, false, true, false),
		},
		{
			name:    "qwen markup in a cut-off harmony call's arguments",
			content: `to=functions.note_add {"text":"<function=get_state></function>"`,
			want:    scan(WireMarkupHarmony, false, true, false),
		},
		{
			name:    "harmony recipient in a cut-off harmony call's arguments",
			content: `to=functions.unknown {"text":"to=functions.get_state {}"`,
			want:    scan(WireMarkupHarmony, false, false, false),
		},
		{
			name:    "a call after another call's nested markup is still found",
			content: `{"name":"note_add","arguments":"<function=get_state></function>"} ` + qwenCall,
			want:    scan(WireMarkupQwenXML, true, true, true),
		},
		// Harmony arguments are an object, after header pieces only.
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
			name:    "harmony prose between the name and an object",
			content: `to=functions.get_weather with {"location":"Paris"}`,
			want:    scan(WireMarkupHarmony, false, true, false),
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
