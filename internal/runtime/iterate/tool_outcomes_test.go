package iterate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// scriptedExecutor answers each call from a per-tool script, in call
// order; a tool whose script runs out keeps answering with its last
// step. A step with a non-empty err fails the call.
type scriptedExecutor struct {
	mu      sync.Mutex
	scripts map[string][]scriptStep
	calls   map[string]int
}

type scriptStep struct {
	result string
	err    string
	// rejected marks err as a refusal of these arguments
	// ([toolargs.Rejected]); without it err is about no argument.
	rejected []string
	// targetRefused returns err as a refusal of the target the call
	// named ([tools.ErrTargetRefused]).
	targetRefused bool
	// failure, when set, is returned as the call's error in place of
	// err, for a refusal shaped the way a real writer shapes one.
	failure error
}

func (s *scriptedExecutor) Execute(_ context.Context, name, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = make(map[string]int)
	}
	steps := s.scripts[name]
	if len(steps) == 0 {
		return "ok", nil
	}
	idx := s.calls[name]
	s.calls[name]++
	if idx >= len(steps) {
		idx = len(steps) - 1
	}
	step := steps[idx]
	if step.failure != nil {
		return "", step.failure
	}
	if step.err != "" {
		err := toolargs.Rejected(errors.New(step.err), step.rejected...)
		if step.targetRefused {
			return "", &tools.ErrTargetRefused{Err: err}
		}
		return "", err
	}
	return step.result, nil
}

// numberedCall builds a tool call with a unique ID so each call's
// result can be found in the message history.
func numberedCall(n int, name string, args map[string]any) llm.ToolCall {
	tc := makeToolCall(name, args)
	tc.ID = fmt.Sprintf("tc_%d", n)
	return tc
}

// runScript drives the engine through one tool call per iteration, then
// a closing text reply, and returns the result plus each call's tool
// result content in call order.
func runScript(t *testing.T, cfg Config, calls []llm.ToolCall) (*Result, []string) {
	t.Helper()
	responses := make([]*llm.ChatResponse, 0, len(calls)+1)
	for _, tc := range calls {
		responses = append(responses, toolCallResponse(tc))
	}
	responses = append(responses, textResponse("done"))
	cfg.LLM = &mockLLM{responses: responses}
	cfg.MaxIterations = len(calls) + 2

	result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byID := make(map[string]string)
	for _, m := range result.Messages {
		if m.Role == "tool" {
			byID[m.ToolCallID] = m.Content
		}
	}
	contents := make([]string, len(calls))
	for i, tc := range calls {
		contents[i] = byID[tc.ID]
	}
	return result, contents
}

func publishArgs(statusLine, teaser, digest string) map[string]any {
	return map[string]any{"status_line": statusLine, "teaser": teaser, "digest": digest}
}

// TestToolOutcomesTally pins the per-Run tally: executed calls, failures,
// successes, and repeat-guard refusals, which count as failures of the
// tool they refused because they are rejected attempts at the same work.
func TestToolOutcomesTally(t *testing.T) {
	const tool = "publish_output_trip"
	rejected := scriptStep{err: "status_line is 190 characters and the limit is 160"}
	cases := []struct {
		name      string
		script    []scriptStep
		calls     []map[string]any
		available bool
		want      ToolOutcome
	}{
		{
			// A later success does not erase the rejection text: LastError
			// is the most recent failure, not the most recent call.
			name:      "rejections then success",
			script:    []scriptStep{rejected, rejected, {result: "published"}},
			calls:     []map[string]any{publishArgs("a", "t", "d"), publishArgs("b", "t", "d"), publishArgs("c", "t", "d")},
			available: true,
			want: ToolOutcome{Calls: 3, Failures: 2, Successes: 1,
				LastError: "status_line is 190 characters and the limit is 160"},
		},
		{
			name:   "identical rejected calls past the repeat guard",
			script: []scriptStep{rejected},
			calls: []map[string]any{
				publishArgs("a", "t", "d"), publishArgs("a", "t", "d"), publishArgs("a", "t", "d"),
				publishArgs("a", "t", "d"), publishArgs("a", "t", "d"),
			},
			available: true,
			want: ToolOutcome{Calls: 3, Failures: 5, Blocked: 2,
				LastError: "status_line is 190 characters and the limit is 160"},
		},
		{
			name:      "unavailable tool is a failed call",
			calls:     []map[string]any{publishArgs("a", "t", "d")},
			available: false,
			want: ToolOutcome{Calls: 1, Failures: 1,
				LastError: (&tools.ErrToolUnavailable{ToolName: tool}).Error()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedExecutor{scripts: map[string][]scriptStep{tool: tc.script}}
			cfg := baseCfg(nil, nil)
			cfg.Executor = exec
			cfg.MaxIllegalStrikes = 10
			cfg.CheckToolAvail = func(string) bool { return tc.available }
			calls := make([]llm.ToolCall, len(tc.calls))
			for i, args := range tc.calls {
				calls[i] = numberedCall(i, tool, args)
			}
			result, _ := runScript(t, cfg, calls)

			// No TargetKey is set, so the breakdown is one empty target.
			if got, want := result.ToolOutcomes[tool], withEmptyTarget(tc.want); !reflect.DeepEqual(got, want) {
				t.Errorf("outcome = %+v, want %+v", got, want)
			}
		})
	}
}

// TestToolOutcomesNilWithoutTools keeps the tally absent, not empty, on a
// run that called no tool.
func TestToolOutcomesNilWithoutTools(t *testing.T) {
	cfg := baseCfg(nil, &mockExecutor{})
	result, _ := runScript(t, cfg, nil)
	if result.ToolOutcomes != nil {
		t.Errorf("ToolOutcomes = %#v, want nil", result.ToolOutcomes)
	}
}

// TestUnchangedArgumentsNote pins where the note lands: on a failing
// call that resent a value the previous failure for the same target also
// carried, naming exactly the unchanged keys both errors marked as
// refused ([toolargs.Rejected]). Only the marks count, never the text: an
// operational failure whose text mentions an argument, a valid field
// resent beside the one being fixed, and a field only one of the two
// errors refused all go unnamed, as does every call around a success.
func TestUnchangedArgumentsNote(t *testing.T) {
	const publish = "publish_output_trip"
	teaserOver := scriptStep{err: "teaser is 400 characters and the limit is 280", rejected: []string{"teaser"}}
	published := scriptStep{result: "published"}
	longDigest := strings.Repeat("d", 2100)
	cases := []struct {
		name string
		// tool is the tool called, publish when empty.
		tool   string
		script []scriptStep
		calls  []map[string]any
		// wantKeys is, per call, the key list the note must name, or ""
		// when the call's result must carry no note.
		wantKeys []string
	}{
		{
			// Copilot's case on PR #1579: the lookup's error text names the
			// name argument, but a closed database is not a refusal of a
			// valid name, so matching the prose told the model to change it.
			name:     "an operational error that mentions an unchanged argument gets no note",
			tool:     "contact_lookup",
			script:   []scriptStep{{err: `resolve contact: find by name or nickname "Alice": sql: database is closed`}},
			calls:    []map[string]any{{"name": "Alice"}, {"name": "Alice"}},
			wantKeys: []string{"", ""},
		},
		{
			name: "two rejections of an unchanged digest name it",
			script: []scriptStep{
				{err: "digest is 2100 characters and the limit is 2048 (52 over)", rejected: []string{"digest"}},
			},
			calls:    []map[string]any{publishArgs("first", "t", longDigest), publishArgs("second", "t", longDigest)},
			wantKeys: []string{"", "digest"},
		},
		{
			// digest is resent unchanged too, but no error marks it: it is
			// a valid projection, not part of what was refused.
			name:     "second rejection names only the unchanged key both errors reject",
			script:   []scriptStep{teaserOver, teaserOver},
			calls:    []map[string]any{publishArgs("first", "long", "d"), publishArgs("second", "long", "d")},
			wantKeys: []string{"", "teaser"},
		},
		{
			name: "every unchanged key both errors reject is listed",
			script: []scriptStep{{
				err:      "status_line is 190 characters and the limit is 120; teaser is 560 characters and the limit is 500",
				rejected: []string{"status_line", "teaser"},
			}},
			calls:    []map[string]any{publishArgs("same", "same", "first"), publishArgs("same", "same", "second")},
			wantKeys: []string{"", "status_line, teaser"},
		},
		{
			// The shape every structured writer returns: one frame around a
			// join of per-field violations, one of them marking nothing.
			name: "joined violations inside a frame give the union",
			script: []scriptStep{{failure: fmt.Errorf("projections are invalid and nothing was written: %w", errors.Join(
				toolargs.Rejected(errors.New("status_line is 190 characters and the limit is 120"), "status_line"),
				errors.New("dossier must carry exactly one title"),
				toolargs.Rejected(errors.New("teaser is 560 characters and the limit is 500"), "teaser"),
			))}},
			calls:    []map[string]any{publishArgs("same", "same", "first"), publishArgs("same", "same", "second")},
			wantKeys: []string{"", "status_line, teaser"},
		},
		{
			// The incident's retry shape: the failing field was reworded
			// (still over), and the valid ones were resent unchanged.
			name: "partial fix leaves the valid resent fields unnamed",
			script: []scriptStep{
				{err: "status_line is 190 characters and the limit is 120 (70 over)", rejected: []string{"status_line"}},
				{err: "status_line is 150 characters and the limit is 120 (30 over)", rejected: []string{"status_line"}},
			},
			calls: []map[string]any{
				publishArgs(strings.Repeat("a", 190), "valid teaser", "valid digest"),
				publishArgs(strings.Repeat("b", 150), "valid teaser", "valid digest"),
			},
			wantKeys: []string{"", ""},
		},
		{
			name: "an unmarked error is not about the values",
			script: []scriptStep{
				{err: "home assistant is currently unreachable (reconnecting in background)"},
			},
			calls:    []map[string]any{publishArgs("a", "t", "d"), publishArgs("a", "t", "d")},
			wantKeys: []string{"", ""},
		},
		{
			name: "an unchanged key no error marks gets no note",
			script: []scriptStep{
				{err: "digest is 2100 characters and the limit is 2048", rejected: []string{"digest"}},
			},
			calls:    []map[string]any{publishArgs("same", "same", "first"), publishArgs("same", "same", "second")},
			wantKeys: []string{"", ""},
		},
		{
			// The teaser was refused the first time and resent unchanged,
			// but the second refusal was about the status line alone.
			name: "a key only the previous error rejected gets no note",
			script: []scriptStep{
				teaserOver,
				{err: "status_line is 190 characters and the limit is 120", rejected: []string{"status_line"}},
			},
			calls:    []map[string]any{publishArgs("a", "long", "d"), publishArgs("b", "long", "d")},
			wantKeys: []string{"", ""},
		},
		{
			// The first refusal was about a missing read, not the teaser, so
			// the teaser was never refused before this call.
			name: "a key only the current error rejects gets no note",
			script: []scriptStep{
				{err: "No change was made: Thane has no record of this loop reading trip.md"},
				teaserOver,
			},
			calls:    []map[string]any{publishArgs("a", "long", "d"), publishArgs("a", "long", "d")},
			wantKeys: []string{"", ""},
		},
		{
			// A required projection left out twice is refused twice, but
			// no value was resent, so there is nothing unchanged to name.
			name:     "a rejected key the call never sent gets no note",
			script:   []scriptStep{{err: "digest is required", rejected: []string{"digest"}}},
			calls:    []map[string]any{{"status_line": "a", "full": "f"}, {"status_line": "b", "full": "f"}},
			wantKeys: []string{"", ""},
		},
		{
			name:     "every key changed",
			script:   []scriptStep{teaserOver, teaserOver},
			calls:    []map[string]any{publishArgs("a", "b", "c"), publishArgs("x", "y", "z")},
			wantKeys: []string{"", ""},
		},
		{
			name:     "success after a rejection carries no note",
			script:   []scriptStep{teaserOver, published},
			calls:    []map[string]any{publishArgs("a", "long", "d"), publishArgs("b", "long", "d")},
			wantKeys: []string{"", ""},
		},
		{
			name:     "a success in between resets the comparison",
			script:   []scriptStep{teaserOver, published, teaserOver},
			calls:    []map[string]any{publishArgs("a", "long", "d"), publishArgs("b", "short", "d"), publishArgs("a", "long", "d")},
			wantKeys: []string{"", "", ""},
		},
		{
			name: "nested values compare by content, not field order",
			script: []scriptStep{
				{err: "full is 100000 bytes and the ceiling is 98304", rejected: []string{"full"}},
			},
			calls: []map[string]any{
				{"status_line": "a", "full": map[string]any{"x": 1, "y": []any{"p", "q"}}},
				{"status_line": "b", "full": map[string]any{"y": []any{"p", "q"}, "x": 1}},
			},
			wantKeys: []string{"", "full"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.tool
			if tool == "" {
				tool = publish
			}
			cfg := baseCfg(nil, nil)
			cfg.Executor = &scriptedExecutor{scripts: map[string][]scriptStep{tool: tc.script}}
			calls := make([]llm.ToolCall, len(tc.calls))
			for i, args := range tc.calls {
				calls[i] = numberedCall(i, tool, args)
			}
			_, contents := runScript(t, cfg, calls)

			for i, keys := range tc.wantKeys {
				// The note's text before its key list, taken from the
				// prompt itself so a rewording cannot make this check
				// pass vacuously.
				notePrefix, _, _ := strings.Cut(prompts.UnchangedArgumentsNote(tool, "\x00"), "\x00")
				hasNote := strings.Contains(contents[i], notePrefix)
				if keys == "" {
					if hasNote {
						t.Errorf("call %d carries a note it should not: %q", i, contents[i])
					}
					continue
				}
				want := prompts.UnchangedArgumentsNote(tool, keys)
				if !strings.HasSuffix(contents[i], "\n\n"+want) {
					t.Errorf("call %d result = %q, want it to end with note %q", i, contents[i], want)
				}
			}
		})
	}
}

// TestToolOutcomeLastErrorKeepsEveryViolation keeps LastError long enough
// that a multi-field refusal, shared frame and all, still names every
// field on the loop_status row, while a pathological error stays bounded.
func TestToolOutcomeLastErrorKeepsEveryViolation(t *testing.T) {
	const tool = "publish_output_trip"
	const frame = "faceted document projections are invalid and nothing was written; correct every listed field in your next call — a listed field resent unchanged is refused the same way: "
	const lever = "; rewording alone rarely closes a gap this large — remove whole items totalling at least 60 characters: anything resolved, superseded, or already said elsewhere leaves this field entirely"
	twoFields := frame + "status_line is 190 characters and the limit is 120 (70 over)" + lever + "\n" +
		"teaser is 560 characters and the limit is 500 (60 over)" + lever
	cases := []struct {
		name string
		err  string
		want string
	}{
		{name: "a two-field refusal keeps both fields", err: twoFields, want: twoFields},
		{
			name: "a pathological error is still bounded",
			err:  strings.Repeat("x", 2*maxToolOutcomeErrorRunes),
			want: strings.Repeat("x", maxToolOutcomeErrorRunes) + "…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg(nil, nil)
			cfg.Executor = &scriptedExecutor{scripts: map[string][]scriptStep{tool: {{err: tc.err}}}}
			result, _ := runScript(t, cfg, []llm.ToolCall{numberedCall(0, tool, publishArgs("a", "t", "d"))})

			if got := result.ToolOutcomes[tool].LastError; got != tc.want {
				t.Errorf("LastError = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderKeyListBounds keeps the note bounded however many or however
// long the model's argument names are.
func TestRenderKeyListBounds(t *testing.T) {
	long := strings.Repeat("k", maxUnchangedKeyRunes+10)
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{name: "short list", keys: []string{"digest", "teaser"}, want: "digest, teaser"},
		{name: "long name clipped", keys: []string{long}, want: strings.Repeat("k", maxUnchangedKeyRunes) + "…"},
		{
			name: "count folded past the cap",
			keys: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
			want: "a, b, c, d, e, f, g, h (+2 more)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderKeyList(tc.keys); got != tc.want {
				t.Errorf("renderKeyList = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRepeatGuardWording pins which refusal each kind of turn reads.
// Only a turn someone is waiting on may be told to stop and answer the
// user; every other turn — the default — gets text that names no user
// and asks for changed arguments rather than abandoned work.
func TestRepeatGuardWording(t *testing.T) {
	const tool = "publish_output_trip"
	cases := []struct {
		name         string
		replyAwaited bool
		want         string
		mustNot      []string
	}{
		{
			name:         "reply awaited keeps the interactive text",
			replyAwaited: true,
			want:         prompts.RepeatedToolCallInteractive(tool, 4),
		},
		{
			name:    "no reply awaited gets the neutral text",
			want:    prompts.RepeatedToolCall(tool, 4),
			mustNot: []string{"user", "Stop calling tools", "response"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg(nil, nil)
			cfg.Executor = &scriptedExecutor{scripts: map[string][]scriptStep{tool: {{err: "rejected"}}}}
			cfg.ReplyAwaited = tc.replyAwaited
			calls := make([]llm.ToolCall, 4)
			for i := range calls {
				calls[i] = numberedCall(i, tool, publishArgs("same", "same", "same"))
			}
			_, contents := runScript(t, cfg, calls)

			if contents[3] != tc.want {
				t.Errorf("guard text = %q, want %q", contents[3], tc.want)
			}
			for _, s := range tc.mustNot {
				if strings.Contains(contents[3], s) {
					t.Errorf("guard text %q must not contain %q", contents[3], s)
				}
			}
		})
	}
}
