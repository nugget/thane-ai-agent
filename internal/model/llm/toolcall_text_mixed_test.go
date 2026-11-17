package llm

import (
	"reflect"
	"strings"
	"testing"
)

// The contract for content beside structured calls
// (recoverTextCallsBesideNative, reached through
// ApplyTextToolCallFallback):
//
//   - M1: the structured calls are always kept, unchanged and in order.
//   - M2: content that is bare tool-call JSON past leading whitespace
//     (one object or array that is the whole content, or a run of two
//     or more calls with any text after it ignored) adds its calls to
//     offered tools whose names no structured
//     call shares, ahead of the structured calls and in the order
//     written. The content is cleared, the action is
//     recovered_beside_native and the source native_and_text_fallback.
//   - M3: when no call is added (prose, a fence, a tag, a tool name
//     followed by JSON, malformed JSON, unoffered names, or only names
//     the structured calls already have) the response is left exactly
//     as it arrived: action none, source native, content kept.

var mixedOffered = []string{"publish_output_state", "set_next_sleep", "tag_activate", "tag_deactivate"}

func nativeCall(id, name string, args map[string]any) ToolCall {
	var c ToolCall
	c.ID = id
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}

func TestApplyTextToolCallFallbackRecoversCallsBesideNative(t *testing.T) {
	t.Parallel()

	sleep := nativeCall("call_sleep", "set_next_sleep", map[string]any{"duration": "45m"})
	tag := nativeCall("call_tag", "tag_activate", map[string]any{"tag": "forge"})
	publish := `{"name":"publish_output_state","arguments":{"status_line":"Panel clean.","digest":"steady"}}`
	textSleep := `{"name":"set_next_sleep","arguments":{"duration":"30m"}}`
	untag := `{"name":"tag_deactivate","arguments":{"tag":"forge"}}`

	// Each content form is paired with the calls it writes, so the
	// expected result can be built from M2 for any structured calls.
	forms := []struct {
		name    string
		content string
		calls   []string
	}{
		{"single object", publish, []string{"publish_output_state"}},
		{"single object with whitespace around", "\n  " + publish + "\n", []string{"publish_output_state"}},
		{"array", "[" + publish + "," + untag + "]", []string{"publish_output_state", "tag_deactivate"}},
		{"newline-separated calls", publish + "\n" + untag, []string{"publish_output_state", "tag_deactivate"}},
		{"glued calls", publish + untag, []string{"publish_output_state", "tag_deactivate"}},
		{"calls repeating a structured name", publish + "\n" + textSleep, []string{"publish_output_state", "set_next_sleep"}},
		{"calls then prose", publish + "\n" + untag + "\nPublished.", []string{"publish_output_state", "tag_deactivate"}},
		// Written against both alphabetical and offered order, so only
		// the order written can produce this.
		{"calls in written order", untag + "\n" + publish, []string{"tag_deactivate", "publish_output_state"}},
		{"array in written order", "[" + untag + "," + publish + "]", []string{"tag_deactivate", "publish_output_state"}},
	}
	natives := [][]ToolCall{{sleep}, {tag}, {sleep, tag}, {tag, sleep}}

	for _, form := range forms {
		for _, native := range natives {
			nativeNames := callNames(native)
			t.Run(form.name+"/"+strings.Join(nativeNames, "+"), func(t *testing.T) {
				t.Parallel()
				var wantNames []string
				for _, name := range form.calls {
					if !containsName(nativeNames, name) {
						wantNames = append(wantNames, name)
					}
				}
				resp := &ChatResponse{Message: Message{Content: form.content, ToolCalls: append([]ToolCall(nil), native...)}}
				got := ApplyTextToolCallFallback(resp, mixedOffered, DefaultToolCallTextProfile())

				if len(wantNames) == 0 {
					assertMixedUntouched(t, resp, got, form.content, native)
					return
				}
				if got != TextToolActionRecoveredBesideNative || resp.TextToolAction != got {
					t.Fatalf("action = %q (stored %q), want %q", got, resp.TextToolAction, TextToolActionRecoveredBesideNative)
				}
				if resp.ToolCallSource != ToolCallSourceNativeAndText {
					t.Fatalf("source = %q, want %q", resp.ToolCallSource, ToolCallSourceNativeAndText)
				}
				if resp.Message.Content != "" {
					t.Fatalf("content = %q, want empty", resp.Message.Content)
				}
				if names := callNames(resp.Message.ToolCalls); !reflect.DeepEqual(names, append(wantNames, nativeNames...)) {
					t.Fatalf("calls = %v, want %v then %v", names, wantNames, nativeNames)
				}
				if tail := resp.Message.ToolCalls[len(wantNames):]; !reflect.DeepEqual(tail, native) {
					t.Fatalf("structured calls changed: %+v, want %+v", tail, native)
				}
				for _, call := range resp.Message.ToolCalls[:len(wantNames)] {
					if call.ID != "" {
						t.Fatalf("recovered call %q has ID %q; IDs are the provider's to fill", call.Function.Name, call.ID)
					}
				}
			})
		}
	}
}

func TestApplyTextToolCallFallbackLeavesOtherContentBesideNative(t *testing.T) {
	t.Parallel()

	sleep := nativeCall("call_sleep", "set_next_sleep", map[string]any{"duration": "45m"})
	publish := `{"name":"publish_output_state","arguments":{"status_line":"Panel clean."}}`
	cases := map[string]string{
		"prose":                        "Publishing now and sleeping for 45m.",
		"prose then call":              "Publishing now. " + publish,
		"fenced call":                  "```json\n" + publish + "\n```",
		"tagged call":                  "<tool_call>" + publish + "</tool_call>",
		"tagged call after prose":      "Publishing now. <tool_call>" + publish + "</tool_call>",
		"tool name then json":          `publish_output_state {"status_line":"Panel clean."}`,
		"stringified arguments":        `{"name":"publish_output_state","arguments":"{\"status_line\":\"x\"}"}`,
		"unoffered name":               `{"name":"request_capability","arguments":{"tag":"forge"}}`,
		"only the structured name":     `{"name":"set_next_sleep","arguments":{"duration":"30m"}}`,
		"json that is not a call":      `{"temperature":72}`,
		"one call then prose":          publish + "\nPublished.",
		"truncated call":               `{"name":"publish_output_state","arguments":{"status_line":"Pan`,
		"whitespace only":              " \n ",
		"array of unoffered names":     `[{"name":"request_capability","arguments":{}}]`,
		"named data objects":           `{"name":"kitchen","state":"on"}` + "\n" + `{"name":"garage","state":"off"}`,
		"calls after leading non-json": "-\n" + publish + "\n" + publish,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			native := []ToolCall{sleep}
			resp := &ChatResponse{Message: Message{Content: content, ToolCalls: append([]ToolCall(nil), native...)}}
			got := ApplyTextToolCallFallback(resp, mixedOffered, DefaultToolCallTextProfile())
			assertMixedUntouched(t, resp, got, content, native)
		})
	}
}

// The production shape, sized like it: the publish call written as text
// alongside a structured sleep call. The model then reported the publish
// done, though it never ran.
func TestApplyTextToolCallFallbackRunsThePublishWrittenBesideTheSleepCall(t *testing.T) {
	t.Parallel()

	full := strings.Repeat("Steady panel, nothing new to report. ", 350)
	content := `{"name":"publish_output_state","arguments":{"status_line":"Panel clean.","digest":"steady","full":"` + full + `"}}`
	resp := &ChatResponse{Message: Message{
		Content:   content,
		ToolCalls: []ToolCall{nativeCall("chatcmpl-tool-1", "set_next_sleep", map[string]any{"duration": "45m"})},
	}}
	if got := ApplyTextToolCallFallback(resp, mixedOffered, DefaultToolCallTextProfile()); got != TextToolActionRecoveredBesideNative {
		t.Fatalf("action = %q, want %q", got, TextToolActionRecoveredBesideNative)
	}
	if names := callNames(resp.Message.ToolCalls); !reflect.DeepEqual(names, []string{"publish_output_state", "set_next_sleep"}) {
		t.Fatalf("calls = %v", names)
	}
	if got := resp.Message.ToolCalls[0].Function.Arguments["full"]; got != full {
		t.Fatalf("full argument changed: %d chars, want %d", len(got.(string)), len(full))
	}
}

func assertMixedUntouched(t *testing.T, resp *ChatResponse, got TextToolAction, content string, native []ToolCall) {
	t.Helper()
	if got != TextToolActionNone || resp.TextToolAction != TextToolActionNone {
		t.Fatalf("action = %q (stored %q), want %q", got, resp.TextToolAction, TextToolActionNone)
	}
	if resp.ToolCallSource != ToolCallSourceNative {
		t.Fatalf("source = %q, want %q", resp.ToolCallSource, ToolCallSourceNative)
	}
	if resp.Message.Content != content {
		t.Fatalf("content = %q, want it unchanged", resp.Message.Content)
	}
	if !reflect.DeepEqual(resp.Message.ToolCalls, native) {
		t.Fatalf("calls = %+v, want %+v", resp.Message.ToolCalls, native)
	}
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
