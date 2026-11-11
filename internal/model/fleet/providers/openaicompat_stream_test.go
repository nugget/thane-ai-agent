package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// holdToolNames is the offer the hold tests make when tools are offered.
var holdToolNames = []string{"tag_activate", "tag_deactivate"}

// holdRetentionMsg is the INFO record handleStreaming writes when the
// text fallback changes a streamed reply's content.
const holdRetentionMsg = "text tool fallback changed stream content"

// TestCouldBeTextToolCall pins the hold check on the prefixes that
// decide it, form by form.
func TestCouldBeTextToolCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		want    bool
	}{
		{"", true},
		{" \n\t", true},
		{"{", true},
		{`  {"temperature": 72} is the reading`, true},
		{"[", true},
		{"[ \n", true},
		{"[{", true},
		{"[ \n {", true},
		{"[x", false},
		{"[1]", false},
		{"[null", false},
		{"null{", false},
		{"<", true},
		{"<tool_c", true},
		{"<tool_call>", true},
		{"<tool_call> is the tag", true},
		{"\n<tool_call>{", true},
		{"<b", false},
		{"<toolb", false},
		{"<tool_calls", false},
		{"`", true},
		{"``", true},
		{"```", true},
		{"```j", true},
		{"```json", true},
		{"```json ", true},
		{"```  to", true},
		{"```tool\r", true},
		{"```json\n", true},
		{"``` tool \n{", true},
		{"```\n", true},
		{"```\nanything at all", true},
		{"```json x", false},
		{"```js ", false},
		{"```jsx", false},
		{"```go", false},
		{"```js\n", false},
		{"`l", false},
		{"``i", false},
		{"Hello", false},
		{"  Hi", false},
		{"tag_activate {", false},
	}
	for _, tt := range tests {
		if got := couldBeTextToolCall(tt.content); got != tt.want {
			t.Errorf("couldBeTextToolCall(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

// holdRun is what one stream sent its callback.
type holdRun struct {
	resp   *llm.ChatResponse
	err    error
	tokens []string
	// sentBefore[i] is how many bytes had been sent when the reader
	// first asked for frame i.
	sentBefore []int
}

func (r holdRun) sent() string { return strings.Join(r.tokens, "") }

// holdFrame renders one SSE frame from a JSON-encodable value.
func holdFrame(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return "data: " + string(data) + "\n\n"
}

// contentFrames renders each delta as a content frame, then the
// finish_reason frame and [DONE]; the finish frame's index is
// len(parts), the first frame after the content.
func contentFrames(t *testing.T, parts []string) []string {
	t.Helper()
	frames := make([]string, 0, len(parts)+2)
	for _, p := range parts {
		frames = append(frames, holdFrame(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": p}}}}))
	}
	frames = append(frames, holdFrame(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}))
	return append(frames, "data: [DONE]\n\n")
}

// frameReader hands the stream reader one SSE frame per Read, calling
// before(i) when frame i is first asked for. bufio.Scanner reads again
// only after it has returned every line it holds, so when before(i)
// runs, every frame ahead of i has been processed and its tokens sent.
type frameReader struct {
	frames []string
	next   int
	rest   string
	before func(frame int)
}

func (r *frameReader) Read(p []byte) (int, error) {
	if r.rest == "" {
		if r.next == len(r.frames) {
			return 0, io.EOF
		}
		r.before(r.next)
		r.rest = r.frames[r.next]
		r.next++
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

// streamHold runs frames through handleStreaming and records every
// token it sends and when.
func streamHold(frames []string, validToolNames []string, log *slog.Logger) holdRun {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var run holdRun
	sent := 0
	body := &frameReader{frames: frames, before: func(int) { run.sentBefore = append(run.sentBefore, sent) }}
	c := NewOpenAICompatClient("", "", "test", "res", log, 0)
	run.resp, run.err = c.handleStreaming(context.Background(), "m", validToolNames, body, func(ev llm.StreamEvent) {
		if ev.Kind == llm.KindToken {
			run.tokens = append(run.tokens, ev.Token)
			sent += len(ev.Token)
		}
	}, streamTrace{log: log, started: time.Now()})
	return run
}

// holdChunkings splits content every way the hold tests need: whole,
// one rune per delta (so the check sees every prefix), in two at every
// rune boundary, and at seeded random boundaries.
func holdChunkings(content string, seed uint64) [][]string {
	var cuts []int
	for i := 1; i < len(content); i++ {
		if utf8.RuneStart(content[i]) {
			cuts = append(cuts, i)
		}
	}
	split := func(at []int) []string {
		parts := make([]string, 0, len(at)+1)
		prev := 0
		for _, c := range at {
			parts = append(parts, content[prev:c])
			prev = c
		}
		return append(parts, content[prev:])
	}
	out := [][]string{{content}, split(cuts)}
	for _, c := range cuts {
		out = append(out, split([]int{c}))
	}
	rng := rand.New(rand.NewPCG(seed, 1))
	for range 4 {
		var at []int
		for _, c := range cuts {
			if rng.IntN(4) == 0 {
				at = append(at, c)
			}
		}
		out = append(out, split(at))
	}
	return out
}

// fallbackLeaves is the response the text fallback makes of content
// alone, computed apart from any stream.
func fallbackLeaves(t *testing.T, content string, validToolNames []string) *llm.ChatResponse {
	t.Helper()
	resp := &llm.ChatResponse{}
	resp.Message.Content = content
	if err := applyTextToolFallback(resp, validToolNames); err != nil {
		t.Fatalf("applyTextToolFallback(%q): %v", content, err)
	}
	return resp
}

// wholeReplyCalls generates whole-reply calls in every form the
// fallback reads from the start of a reply, with arguments that carry
// multi-byte text, with and without surrounding whitespace.
func wholeReplyCalls() []string {
	const args = `{"room":"Café ☕","note":"日本語 — ok"}`
	second := `{"name":"tag_deactivate","arguments":{}}`
	type payload struct {
		json   string
		single bool // a shape the fallback reads only on its own
	}
	payloads := []payload{
		{json: `{"name":"tag_activate","arguments":` + args + `}`},
		{json: `{"name":"request_capability","arguments":` + args + `}`},
		{json: `{"name":"tag_activate"}`},
		// Suppressed: a call's keys, but arguments sent as a string.
		{json: `{"name":"tag_activate","arguments":"{\"room\":\"Café ☕\"}"}`, single: true},
	}
	forms := []struct {
		wrap  func(p string) string
		multi bool // wraps more than the one payload
	}{
		{wrap: func(p string) string { return p }},
		{wrap: func(p string) string { return "[" + p + "]" }, multi: true},
		{wrap: func(p string) string { return "[\n  " + p + ",\n  " + second + "\n]" }, multi: true},
		{wrap: func(p string) string { return p + second }, multi: true},
		{wrap: func(p string) string { return "<tool_call>" + p + "</tool_call>" }},
		{wrap: func(p string) string { return "<tool_call>\n" + p + "\n</tool_call>" }},
		{wrap: func(p string) string { return "<tool_call>" + p }},
		{wrap: func(p string) string { return "```\n" + p + "\n```" }},
		{wrap: func(p string) string { return "```json\n" + p + "\n```" }},
		{wrap: func(p string) string { return "```tool\n" + p + "\n```" }},
		{wrap: func(p string) string { return "``` json \n" + p + "\n```" }},
		{wrap: func(p string) string { return "```\n<tool_call>" + p + "</tool_call>\n```" }},
		{wrap: func(p string) string { return "```json\n[" + p + "]\n```" }, multi: true},
	}
	var out []string
	for _, p := range payloads {
		for _, f := range forms {
			if p.single && f.multi {
				continue
			}
			for _, lead := range []string{"", " ", "\n\n", "\t \n"} {
				for _, trail := range []string{"", "\n"} {
					out = append(out, lead+f.wrap(p.json)+trail)
				}
			}
		}
	}
	return out
}

// TestStreamHoldsWholeReplyCalls is the invariant against the fallback
// itself: every whole-reply call the fallback recovers or suppresses,
// split into deltas every way that matters, with tools offered and
// without, sends nothing before the stream ends and, in all, exactly the
// content the fallback left, which is nothing.
func TestStreamHoldsWholeReplyCalls(t *testing.T) {
	t.Parallel()
	cleared := []llm.TextToolAction{llm.TextToolActionRecovered, llm.TextToolActionRepairRecoveredUnoffered, llm.TextToolActionSuppressed}
	for i, content := range wholeReplyCalls() {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			t.Parallel()
			for _, names := range [][]string{holdToolNames, nil} {
				want := fallbackLeaves(t, content, names)
				if !slices.Contains(cleared, want.TextToolAction) || want.Message.Content != "" {
					t.Fatalf("corpus: %q is not a whole-reply call with tools %v: action %q left %q", content, names, want.TextToolAction, want.Message.Content)
				}
				for _, parts := range holdChunkings(content, uint64(i)) {
					run := streamHold(contentFrames(t, parts), names, nil)
					if run.err != nil {
						t.Fatalf("tools %v, deltas %q: %v", names, parts, run.err)
					}
					if live := run.sentBefore[len(parts)]; live != 0 {
						t.Fatalf("tools %v, deltas %q: %d bytes sent before the stream ended: %q", names, parts, live, run.tokens)
					}
					if run.sent() != want.Message.Content || run.resp.TextToolAction != want.TextToolAction {
						t.Fatalf("tools %v, deltas %q: sent %q, action %q; want %q, action %q", names, parts, run.tokens, run.resp.TextToolAction, want.Message.Content, want.TextToolAction)
					}
				}
			}
		})
	}
}

// TestStreamReleasesOrdinaryReplies pins the other side: a reply that
// is not a call streams exactly its final content, starting with the
// delta that rules a call out (decidedBy is the shortest prefix that
// does) as one token holding everything before it, and every later
// delta as it arrives. A reply nothing rules out before the end
// (decidedBy "") is sent whole, once, when the stream ends.
func TestStreamReleasesOrdinaryReplies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content   string
		decidedBy string
	}{
		{content: "All four lights are off.", decidedBy: "A"},
		{content: "  \n Hi there.\n", decidedBy: "  \n H"},
		{content: "Café ☕ is open.", decidedBy: "C"},
		{content: "日本語のテキスト", decidedBy: "日"},
		{content: "[the docs](https://example.com) say so.", decidedBy: "[t"},
		{content: "[ x ] a checkbox", decidedBy: "[ x"},
		{content: "[1] Footnote text.", decidedBy: "[1"},
		{content: "```go\nfunc main() {}\n```\nDone.", decidedBy: "```g"},
		{content: "```js\nconsole.log(1)\n```", decidedBy: "```js\n"},
		{content: "```json5\n{a: 1}\n```", decidedBy: "```json5"},
		{content: "```js is shorthand.\n```", decidedBy: "```js "},
		{content: "``` tool chain\n```", decidedBy: "``` tool c"},
		{content: "`ls -la` lists files.", decidedBy: "`l"},
		{content: "``inline`` code", decidedBy: "``i"},
		{content: "<b>bold</b> text", decidedBy: "<b"},
		{content: "<toolbox is the parent>", decidedBy: "<toolb"},
		{content: "<tool_calls> is plural", decidedBy: "<tool_calls"},
		{content: `tag_activate is the tool for that.`, decidedBy: "t"},
		// Held to the end, then sent as they came.
		{content: `{"temperature": 72}`},
		{content: "{x} starts this reply."},
		{content: "\n  {\"a\": 1}\n"},
		{content: "[{\"name\": 1}]"},
		{content: "```json\n{\"key\": 1}\n```\nThat sets the key."},
		{content: "```\nplain output\n```"},
		{content: "<tool_call> is the tag we use."},
	}
	for i, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			t.Parallel()
			if !strings.HasPrefix(tt.content, tt.decidedBy) {
				t.Fatalf("decidedBy %q is not a prefix of the content", tt.decidedBy)
			}
			for _, names := range [][]string{holdToolNames, nil} {
				if want := fallbackLeaves(t, tt.content, names); want.TextToolAction != llm.TextToolActionNone {
					t.Fatalf("corpus: fallback changed %q (action %q)", tt.content, want.TextToolAction)
				}
				for _, parts := range holdChunkings(tt.content, uint64(i)) {
					run := streamHold(contentFrames(t, parts), names, nil)
					if run.err != nil {
						t.Fatalf("deltas %q: %v", parts, run.err)
					}
					if run.sent() != run.resp.Message.Content || run.sent() != tt.content {
						t.Fatalf("deltas %q: sent %q, final content %q, want both %q", parts, run.tokens, run.resp.Message.Content, tt.content)
					}
					var wantTokens []string
					if tt.decidedBy == "" {
						if live := run.sentBefore[len(parts)]; live != 0 {
							t.Fatalf("deltas %q: %d bytes sent before the end", parts, live)
						}
						wantTokens = []string{tt.content}
					} else {
						k, at := 0, len(parts[0])
						for at < len(tt.decidedBy) {
							k++
							at += len(parts[k])
						}
						wantTokens = append([]string{strings.Join(parts[:k+1], "")}, parts[k+1:]...)
						if run.sentBefore[k] != 0 || run.sentBefore[k+1] != at {
							t.Fatalf("deltas %q: sent %d bytes before delta %d and %d after it, want 0 and %d", parts, run.sentBefore[k], k, run.sentBefore[k+1], at)
						}
					}
					if !slices.Equal(run.tokens, wantTokens) {
						t.Fatalf("deltas %q: tokens %q, want %q", parts, run.tokens, wantTokens)
					}
				}
			}
		})
	}
}

// sseServer serves frames as one OpenAI-compatible SSE response.
func sseServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// chatStreamTokens runs one streaming request through a real
// OpenAICompatClient and collects the tokens it sends.
func chatStreamTokens(t *testing.T, frames []string, tools []map[string]any) (*llm.ChatResponse, []string, error) {
	t.Helper()
	srv := sseServer(t, frames)
	c := NewOpenAICompatClient(srv.URL, "", "test", "res", nil, 0)
	var tokens []string
	resp, err := c.ChatStream(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, tools, func(ev llm.StreamEvent) {
		if ev.Kind == llm.KindToken {
			tokens = append(tokens, ev.Token)
		}
	})
	return resp, tokens, err
}

// TestOpenAICompatStreamHoldOverHTTP pins the hold through the real
// client, with tools offered and without: the force-text and
// timeout-recovery calls offer none, and the fallback then accepts a
// call to any name.
func TestOpenAICompatStreamHoldOverHTTP(t *testing.T) {
	t.Parallel()
	offered := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "tag_activate", "parameters": map[string]any{"type": "object"}}},
	}
	nativeCall := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"tag_activate","arguments":"{}"}}]}}]}`
	tests := []struct {
		name       string
		parts      []string
		extra      []string // frames between the content and the end
		tools      []map[string]any
		wantTokens []string
		wantCalls  []string
		wantText   string
	}{
		{name: "call with tools offered", parts: []string{`{"name":"tag_`, `activate","arguments":{"tag":"forge"}}`},
			tools: offered, wantCalls: []string{"tag_activate"}},
		{name: "call to any name without tools", parts: []string{"<tool_", "call>", `{"name":"request_capability","arguments":{}}`},
			wantCalls: []string{"request_capability"}},
		{name: "prose with tools offered", parts: []string{"Hel", "lo ", "there"}, tools: offered,
			wantTokens: []string{"Hel", "lo ", "there"}, wantText: "Hello there"},
		{name: "prose without tools", parts: []string{" ", "[the docs]", "(x)"},
			wantTokens: []string{" [the docs]", "(x)"}, wantText: " [the docs](x)"},
		{name: "JSON that is not a call", parts: []string{`{"temperature":`, ` 72}`}, tools: offered,
			wantTokens: []string{`{"temperature": 72}`}, wantText: `{"temperature": 72}`},
		{name: "call-shaped text beside a native call", parts: []string{`{"name":"tag_deactivate","arguments":{}}`}, extra: []string{nativeCall},
			tools: offered, wantTokens: []string{`{"name":"tag_deactivate","arguments":{}}`}, wantCalls: []string{"tag_activate"}, wantText: `{"name":"tag_deactivate","arguments":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			frames := contentFrames(t, tt.parts)
			for _, f := range tt.extra {
				frames = slices.Insert(frames, len(tt.parts), "data: "+f+"\n\n")
			}
			resp, tokens, err := chatStreamTokens(t, frames, tt.tools)
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			if !slices.Equal(tokens, tt.wantTokens) {
				t.Errorf("tokens = %q, want %q", tokens, tt.wantTokens)
			}
			if resp.Message.Content != tt.wantText {
				t.Errorf("content = %q, want %q", resp.Message.Content, tt.wantText)
			}
			var calls []string
			for _, tc := range resp.Message.ToolCalls {
				calls = append(calls, tc.Function.Name)
			}
			if !slices.Equal(calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", calls, tt.wantCalls)
			}
		})
	}
}

// TestOpenAICompatRefusedStreamSendsNothingHeld pins that a stream the
// client refuses never releases what it was holding. Text that had
// already been released stays sent, as before.
func TestOpenAICompatRefusedStreamSendsNothingHeld(t *testing.T) {
	t.Parallel()
	held := `data: {"choices":[{"delta":{"content":"{\"name\":\"tag_activate\",\"arguments\":{}}"}}]}` + "\n\n"
	tests := []struct {
		name       string
		frames     []string
		wantTokens []string
	}{
		{name: "no terminal marker", frames: []string{held}},
		{name: "whitespace only", frames: []string{`data: {"choices":[{"delta":{"content":"\n  "}}]}` + "\n\n", "data: [DONE]\n\n"}},
		{name: "no frames"},
		{name: "decode error", frames: []string{held, "data: {not json\n\n", "data: [DONE]\n\n"}},
		{name: "error frame", frames: []string{held, `data: {"error":{"message":"overloaded"}}` + "\n\n"}},
		{name: "tool-call decode error", frames: []string{held,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"tag_activate","arguments":"{bad"}}]}}]}` + "\n\n",
			"data: [DONE]\n\n"}},
		{name: "released prose then no terminal marker", frames: []string{`data: {"choices":[{"delta":{"content":"Partial"}}]}` + "\n\n"},
			wantTokens: []string{"Partial"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, tokens, err := chatStreamTokens(t, tt.frames, nil)
			if err == nil {
				t.Fatal("ChatStream succeeded, want a refusal")
			}
			if !slices.Equal(tokens, tt.wantTokens) {
				t.Errorf("tokens = %q, want %q", tokens, tt.wantTokens)
			}
		})
	}
}

// TestStreamRetainsRewrittenContent pins the INFO record that keeps a
// reply the fallback changed: fixing the leak must not erase the only
// place that text was seen. streamed_removed marks the accepted gaps,
// where text the fallback removed had already streamed; each gap the
// handleStreaming Godoc names has a row here.
func TestStreamRetainsRewrittenContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		parts          []string
		wantRecord     bool
		wantAction     llm.TextToolAction
		wantStreamed   bool
		wantFinalToken string
	}{
		{name: "whole-reply call", parts: []string{`{"name":"tag_activate",`, `"arguments":{}}`},
			wantRecord: true, wantAction: llm.TextToolActionRecovered},
		{name: "suppressed call", parts: []string{`{"name":"tag_activate","arguments":"{}"}`},
			wantRecord: true, wantAction: llm.TextToolActionSuppressed},
		{name: "held JSON with a trailing call", parts: []string{`{"a": 1} then `, `{"name":"tag_deactivate"}`},
			wantRecord: true, wantAction: llm.TextToolActionStrippedTrailing, wantFinalToken: `{"a": 1} then`},
		{name: "prose then a tagged call", parts: []string{"Loading forge. ", "<tool_call>", `{"name":"tag_activate","arguments":{}}`, "</tool_call>"},
			wantRecord: true, wantAction: llm.TextToolActionRecovered, wantStreamed: true},
		{name: "a tagged call inside another language's fence", parts: []string{"```python\n", "<tool_call>", `{"name":"tag_activate","arguments":{}}`, "</tool_call>\n```"},
			wantRecord: true, wantAction: llm.TextToolActionRecovered, wantStreamed: true},
		{name: "prose then a trailing call", parts: []string{"Resetting. ", `{"name":"tag_deactivate"}`},
			wantRecord: true, wantAction: llm.TextToolActionStrippedTrailing, wantStreamed: true},
		{name: "tool name then JSON arguments", parts: []string{"tag_activate ", `{"tag":"forge"}`},
			wantRecord: true, wantAction: llm.TextToolActionRecovered, wantStreamed: true},
		{name: "array led by null", parts: []string{"[null, ", `{"name":"tag_activate","arguments":{}}]`},
			wantRecord: true, wantAction: llm.TextToolActionRecovered, wantStreamed: true},
		{name: "concatenation led by null", parts: []string{"null", `{"name":"tag_activate","arguments":{}}{"name":"tag_deactivate","arguments":{}}`},
			wantRecord: true, wantAction: llm.TextToolActionRecovered, wantStreamed: true},
		{name: "prose", parts: []string{"All ", "off."}},
		{name: "held JSON that is not a call", parts: []string{`{"temperature":`, ` 72}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: llm.LevelTrace}))
			run := streamHold(contentFrames(t, tt.parts), holdToolNames, log)
			if run.err != nil {
				t.Fatalf("handleStreaming: %v", run.err)
			}
			if tt.wantFinalToken != "" && (len(run.tokens) != 1 || run.tokens[0] != tt.wantFinalToken) {
				t.Errorf("tokens = %q, want the one token %q", run.tokens, tt.wantFinalToken)
			}
			var records []map[string]any
			for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
				var rec map[string]any
				if err := json.Unmarshal(line, &rec); err != nil {
					t.Fatalf("decode log line %q: %v", line, err)
				}
				if rec["msg"] == holdRetentionMsg {
					records = append(records, rec)
				}
			}
			if !tt.wantRecord {
				if len(records) != 0 {
					t.Fatalf("records = %v, want none", records)
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("records = %v, want one", records)
			}
			rec := records[0]
			want := map[string]any{
				"level":            "INFO",
				"text_tool_action": string(tt.wantAction),
				"original_content": strings.Join(tt.parts, ""),
				"streamed_removed": tt.wantStreamed,
			}
			for k, v := range want {
				if rec[k] != v {
					t.Errorf("record %s = %v, want %v", k, rec[k], v)
				}
			}
		})
	}
}
