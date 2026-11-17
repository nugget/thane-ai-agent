package llm

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The contract for tool calls written as JSON objects one after another
// (parseConcatenatedToolCallJSON, reached through ParseTextToolCalls). A
// call is what the single-object form reads: an object with a non-empty
// string name and arguments, if any, that are an object or null.
//
//   - C1: two or more offered calls, glued or separated by any JSON
//     whitespace, with any whitespace around them, parse to exactly
//     those calls in order: the same calls as the array form.
//   - C2: the run ends at the first text that is not a JSON value or a
//     value of the wrong shape (not an object, a name that is not a
//     string, arguments that are not an object or null); what follows
//     is ignored, as it always was.
//   - C3: null and objects without a name are skipped.
//   - C4: fewer than two calls in the run yields no calls.
//   - C5: unoffered names are dropped as the array form drops them, and
//     when none is offered the repair parse returns every call.

var sequenceOffered = []string{"publish_output_state", "set_next_sleep", "tag_activate"}

// sequenceCalls are well-formed calls, including string arguments that
// hold the separators and braces the parser must not split on.
var sequenceCalls = []string{
	`{"name":"publish_output_state","arguments":{"status_line":"Panel clean.","digest":"one}{two\n{three}","full":"# Doc\n\n{\"nested\": true}\n"}}`,
	`{"name":"set_next_sleep","arguments":{"duration":"30m","reason":"steady"}}`,
	"{\n  \"name\": \"tag_activate\",\n  \"arguments\": {\n    \"tag\": \"forge\"\n  }\n}",
	`{"name":"set_next_sleep","arguments":null}`,
	`{"name":"tag_activate"}`,
}

var sequenceSeparators = []string{"", "\n", "\n\n", " ", "\t", "\r\n", " \n\t "}

// sequenceTails follow the last call and are ignored (C2).
var sequenceTails = []string{"", "\n", " \n ", "\nDone.", "]", "}", "Session notes", "\n,", "\n{\"name\":"}

func TestParseTextToolCallsReadsWhitespaceSeparatedSequences(t *testing.T) {
	t.Parallel()

	profile := DefaultToolCallTextProfile()
	check := func(t *testing.T, objs []string, seps []string, lead, tail string) {
		t.Helper()
		var b strings.Builder
		b.WriteString(lead)
		for i, obj := range objs {
			if i > 0 {
				b.WriteString(seps[i-1])
			}
			b.WriteString(obj)
		}
		b.WriteString(tail)
		content := b.String()

		want := ParseTextToolCalls("["+strings.Join(objs, ",")+"]", sequenceOffered, profile)
		if len(want) != len(objs) {
			t.Fatalf("array form parsed %d calls from %d objects", len(want), len(objs))
		}
		got := ParseTextToolCalls(content, sequenceOffered, profile)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("content %q\n got %+v\nwant %+v", content, got, want)
		}
	}

	for _, lead := range []string{"", "\n", " \n "} {
		for _, tail := range sequenceTails {
			for _, sep := range sequenceSeparators {
				for _, a := range sequenceCalls {
					for _, b := range sequenceCalls {
						check(t, []string{a, b}, []string{sep}, lead, tail)
						for _, c := range sequenceCalls {
							second := sequenceSeparators[(len(a)+len(c))%len(sequenceSeparators)]
							check(t, []string{a, b, c}, []string{sep, second}, lead, tail)
						}
					}
				}
			}
		}
	}
}

func TestParseTextToolCallsEndsTheRunAtTheFirstNonCall(t *testing.T) {
	t.Parallel()

	first, second := sequenceCalls[0], sequenceCalls[1]
	// Each case breaks the run before a second call, so C4 leaves no
	// calls at all.
	cases := map[string]string{
		"comma separator":           first + "," + second,
		"comma on its own line":     first + "\n,\n" + second,
		"semicolon separator":       first + ";" + second,
		"word separator":            first + " then " + second,
		"vertical tab separator":    first + "\v" + second,
		"no-break space separator":  first + " " + second,
		"comment separator":         first + "\n// next\n" + second,
		"prose between":             first + "\nAnd also:\n" + second,
		"truncated second call":     first + "\n" + `{"name":"set_next_sleep","arguments":{"duration":"3`,
		"numeric name":              first + "\n" + `{"name":42,"arguments":{}}` + "\n" + second,
		"stringified arguments":     first + "\n" + `{"name":"set_next_sleep","arguments":"{\"duration\":\"30m\"}"}` + "\n" + second,
		"array arguments":           first + "\n" + `{"name":"set_next_sleep","arguments":["30m"]}` + "\n" + second,
		"string value":              first + "\n\"set_next_sleep\"\n" + second,
		"number value":              first + "\n42\n" + second,
		"empty array value":         first + "\n[]\n" + second,
		"array of calls after call": first + "\n[" + second + "]",
		"prose before":              "Publishing now.\n" + first + "\n" + second,
		"one call then prose":       first + "\nPublished.",
		"one call alone":            first,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := parseConcatenatedToolCallJSON(content, sequenceOffered); got != nil {
				t.Fatalf("parseConcatenatedToolCallJSON = %v, want nil", callNames(got))
			}
		})
	}
}

func TestParseTextToolCallsSkipsValuesWithoutAName(t *testing.T) {
	t.Parallel()

	first, second := sequenceCalls[0], sequenceCalls[1]
	tests := map[string]struct {
		content    string
		want       []string
		wantRepair []string
	}{
		"null between calls": {first + "\nnull\n" + second,
			[]string{"publish_output_state", "set_next_sleep"}, []string{"publish_output_state", "set_next_sleep"}},
		"led by null": {"null\n" + first + "\n" + second,
			[]string{"publish_output_state", "set_next_sleep"}, []string{"publish_output_state", "set_next_sleep"}},
		"object without a name": {first + "\n" + `{"arguments":{"duration":"30m"}}` + "\n" + second,
			[]string{"publish_output_state", "set_next_sleep"}, []string{"publish_output_state", "set_next_sleep"}},
		"empty name": {first + "\n" + `{"name":"","arguments":{}}` + "\n" + second,
			[]string{"publish_output_state", "set_next_sleep"}, []string{"publish_output_state", "set_next_sleep"}},
		"only one call among values": {"null\n" + first + "\n" + `{"arguments":{}}`, nil, nil},
		// A named object reads as a call, as it does alone: the
		// offered parse drops an unoffered name and repair keeps it.
		"named data objects": {`{"name":"kitchen","state":"on"}` + "\n" + `{"name":"garage","state":"off"}`,
			nil, []string{"kitchen", "garage"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := callNames(parseConcatenatedToolCallJSON(tt.content, sequenceOffered)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("calls = %v, want %v", got, tt.want)
			}
			if got := callNames(ParseTextToolCallsForRepair(tt.content, DefaultToolCallTextProfile())); !reflect.DeepEqual(got, tt.wantRepair) {
				t.Fatalf("repair calls = %v, want %v", got, tt.wantRepair)
			}
		})
	}
}

func TestParseTextToolCallsSequenceNamesFollowTheArrayForm(t *testing.T) {
	t.Parallel()

	profile := DefaultToolCallTextProfile()
	unoffered := `{"name":"request_capability","arguments":{"tag":"forge"}}`
	tests := []struct {
		name       string
		objs       []string
		wantOffer  []string
		wantRepair []string
	}{
		{
			name:       "unoffered call between offered ones is dropped",
			objs:       []string{sequenceCalls[0], unoffered, sequenceCalls[1]},
			wantOffer:  []string{"publish_output_state", "set_next_sleep"},
			wantRepair: []string{"publish_output_state", "request_capability", "set_next_sleep"},
		},
		{
			name:       "one offered call beside an unoffered one",
			objs:       []string{sequenceCalls[1], unoffered},
			wantOffer:  []string{"set_next_sleep"},
			wantRepair: []string{"set_next_sleep", "request_capability"},
		},
		{
			name:       "all unoffered leaves the calls to repair",
			objs:       []string{unoffered, `{"name":"forge_capability","arguments":{}}`},
			wantRepair: []string{"request_capability", "forge_capability"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			array := "[" + strings.Join(tt.objs, ",") + "]"
			content := strings.Join(tt.objs, "\n")
			if got, want := callNames(ParseTextToolCalls(content, sequenceOffered, profile)), callNames(ParseTextToolCalls(array, sequenceOffered, profile)); !reflect.DeepEqual(got, want) {
				t.Fatalf("sequence offered parse = %v, array form = %v", got, want)
			}
			if got := callNames(ParseTextToolCalls(content, sequenceOffered, profile)); !reflect.DeepEqual(got, tt.wantOffer) {
				t.Fatalf("offered parse = %v, want %v", got, tt.wantOffer)
			}
			if got := callNames(ParseTextToolCallsForRepair(content, profile)); !reflect.DeepEqual(got, tt.wantRepair) {
				t.Fatalf("repair parse = %v, want %v", got, tt.wantRepair)
			}
		})
	}
}

// A loop's whole reply written as its publish call, a newline and its
// sleep call: the shape a local model wrote on about a quarter of one
// loop's wakes, none of which ran either call.
func TestApplyTextToolCallFallbackRecoversNewlineSeparatedCalls(t *testing.T) {
	t.Parallel()

	content := fmt.Sprintf("%s\n%s", sequenceCalls[0], sequenceCalls[1])
	resp := &ChatResponse{Message: Message{Content: content}}
	got := ApplyTextToolCallFallback(resp, sequenceOffered, DefaultToolCallTextProfile())
	if got != TextToolActionRecovered || resp.ToolCallSource != ToolCallSourceTextFallback {
		t.Fatalf("action = %q, source = %q; want %q, %q", got, resp.ToolCallSource, TextToolActionRecovered, ToolCallSourceTextFallback)
	}
	if names := callNames(resp.Message.ToolCalls); !reflect.DeepEqual(names, []string{"publish_output_state", "set_next_sleep"}) {
		t.Fatalf("calls = %v", names)
	}
	if digest := resp.Message.ToolCalls[0].Function.Arguments["digest"]; digest != "one}{two\n{three}" {
		t.Fatalf("digest argument = %q", digest)
	}
	if resp.Message.Content != "" {
		t.Fatalf("content = %q, want empty", resp.Message.Content)
	}
}

func callNames(calls []ToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Function.Name)
	}
	return names
}
