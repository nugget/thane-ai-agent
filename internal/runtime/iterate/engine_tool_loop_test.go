package iterate

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// The contract for ending a run on refused repeats (Engine.Run):
//
//   - L1: an iteration in which every call was refused as a repeat
//     (past MaxToolRepeat identical calls) is a strike. An iteration
//     with any call that was not refused — one that ran, failed, or was
//     blocked as unavailable — clears the strikes.
//   - L2: at MaxToolLoopStrikes strikes in a row the run ends. It is
//     exhausted with reason tool_loop, the striking iteration is the last
//     tool iteration and records that reason, and one forced-text call
//     follows with no tools offered, whose reply is the content.
//   - L3: short of that the run goes on as before: a model that changes
//     its arguments, calls something else or replies is not stopped, and
//     the other endings (illegal_tool, a text reply) are unchanged.
//   - L4: every call, the ending iteration's included, has exactly one
//     tool result, in order.
//   - L5: a zero MaxToolLoopStrikes means DefaultMaxToolLoopStrikes.

// loopStep is one model reply after the opening calls, each of which
// runs the repeated call once so that every later copy is refused.
type loopStep string

const (
	stepRefused     loopStep = "R"  // the repeated call alone: refused
	stepRefusedTwo  loopStep = "RR" // the repeated call twice in one batch: both refused
	stepRan         loopStep = "X"  // a call with new arguments that succeeds
	stepFailed      loopStep = "F"  // a call with new arguments that fails
	stepMixed       loopStep = "M"  // the repeated call beside a new call that succeeds
	stepUnavailable loopStep = "U"  // a call to a tool that is not available
)

var allLoopSteps = []loopStep{stepRefused, stepRefusedTwo, stepRan, stepFailed, stepMixed, stepUnavailable}

// loopScriptLLM replies from a script while tools are offered and with
// "forced" when they are not, which only the forced-text call does.
type loopScriptLLM struct {
	mu      sync.Mutex
	script  []*llm.ChatResponse
	next    int
	offered []bool
}

func (m *loopScriptLLM) Chat(ctx context.Context, model string, msgs []llm.Message, tls []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, msgs, tls, nil)
}

func (m *loopScriptLLM) ChatStream(_ context.Context, _ string, _ []llm.Message, tls []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offered = append(m.offered, tls != nil)
	if tls == nil {
		return textResponse("forced"), nil
	}
	if m.next >= len(m.script) {
		return nil, errors.New("script exhausted")
	}
	resp := m.script[m.next]
	m.next++
	return resp, nil
}

func (m *loopScriptLLM) Ping(context.Context) error { return nil }

// loopScript builds the model's replies: MaxToolRepeat runs of the
// repeated call, one reply per step, then a closing text reply. Every
// call has its own ID and every new call its own arguments, so only
// copies of the repeated call are ever refused.
func loopScript(steps []loopStep, repeat int) []*llm.ChatResponse {
	id := 0
	call := func(name string, args map[string]any) llm.ToolCall {
		id++
		tc := makeToolCall(name, args)
		tc.ID = fmt.Sprintf("call_%d", id)
		return tc
	}
	repeated := func() llm.ToolCall { return call("search", map[string]any{"q": "same"}) }
	fresh := func(name string) llm.ToolCall { return call(name, map[string]any{"n": id}) }

	var script []*llm.ChatResponse
	for range repeat {
		script = append(script, toolCallResponse(repeated()))
	}
	for _, s := range steps {
		var calls []llm.ToolCall
		switch s {
		case stepRefused:
			calls = []llm.ToolCall{repeated()}
		case stepRefusedTwo:
			calls = []llm.ToolCall{repeated(), repeated()}
		case stepRan:
			calls = []llm.ToolCall{fresh("search")}
		case stepFailed:
			calls = []llm.ToolCall{fresh("broken")}
		case stepMixed:
			calls = []llm.ToolCall{repeated(), fresh("search")}
		case stepUnavailable:
			calls = []llm.ToolCall{fresh("banned")}
		}
		script = append(script, toolCallResponse(calls...))
	}
	return append(script, textResponse("done"))
}

// expectedLoopEnding applies L1 and the illegal-call strikes to steps:
// the index of the step that ends the run and why, or -1 and "" when the
// closing text reply does.
func expectedLoopEnding(steps []loopStep, limit int) (int, string) {
	strikes, illegal := 0, 0
	for k, s := range steps {
		if s == stepRefused || s == stepRefusedTwo {
			strikes++
			if strikes >= limit {
				return k, ExhaustToolLoop
			}
		} else {
			strikes = 0
		}
		switch s {
		case stepUnavailable:
			illegal++
			if illegal >= DefaultMaxIllegalStrikes {
				return k, ExhaustIllegalTool
			}
		case stepRan, stepMixed:
			illegal = 0
		}
	}
	return -1, ""
}

func stepRuns(s loopStep) int {
	switch s {
	case stepRan, stepFailed, stepMixed:
		return 1
	}
	return 0
}

func stepRefuses(s loopStep) bool {
	return s == stepRefused || s == stepRefusedTwo || s == stepMixed
}

// checkLoopRun runs steps with MaxToolLoopStrikes set to limit (zero
// for the default) and checks the result against the contract.
func checkLoopRun(steps []loopStep, limit int) error {
	want := limit
	if want == 0 {
		want = DefaultMaxToolLoopStrikes
	}
	endAt, wantReason := expectedLoopEnding(steps, want)
	ran := len(steps)
	if endAt >= 0 {
		ran = endAt + 1
	}

	mock := &loopScriptLLM{script: loopScript(steps, DefaultMaxToolRepeat)}
	exec := &mockExecutor{errors: map[string]error{"broken": errors.New("rejected: change n")}}
	cfg := baseCfg(nil, exec)
	cfg.LLM = mock
	cfg.CheckToolAvail = func(name string) bool { return name != "banned" }
	cfg.MaxToolLoopStrikes = limit
	cfg.MaxIterations = DefaultMaxToolRepeat + len(steps) + 1

	result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
	if err != nil {
		return fmt.Errorf("run error: %w", err)
	}

	// The run ends where the contract says, and how.
	wantCalls := DefaultMaxToolRepeat + ran + 1
	if len(mock.offered) != wantCalls {
		return fmt.Errorf("%d model calls, want %d", len(mock.offered), wantCalls)
	}
	if lastOffered := mock.offered[len(mock.offered)-1]; lastOffered == (endAt >= 0) {
		return fmt.Errorf("last call offered tools = %v, want %v", lastOffered, endAt < 0)
	}
	if result.Exhausted != (endAt >= 0) || result.ExhaustReason != wantReason {
		return fmt.Errorf("exhausted = %v (%q), want %v (%q)", result.Exhausted, result.ExhaustReason, endAt >= 0, wantReason)
	}
	wantContent := "done"
	if endAt >= 0 {
		wantContent = "forced"
	}
	if result.Content != wantContent {
		return fmt.Errorf("content = %q, want %q", result.Content, wantContent)
	}

	// Refused calls never run; every other available call does.
	wantRuns := DefaultMaxToolRepeat
	for _, s := range steps[:ran] {
		wantRuns += stepRuns(s)
	}
	if len(exec.calls) != wantRuns {
		return fmt.Errorf("%d calls ran, want %d", len(exec.calls), wantRuns)
	}

	// Each step's iteration names its refusals, and the ending names its reason.
	if len(result.Iterations) != wantCalls {
		return fmt.Errorf("%d iteration records, want %d", len(result.Iterations), wantCalls)
	}
	for k, s := range steps[:ran] {
		want := ""
		if stepRefuses(s) {
			want = ExhaustToolLoop
		}
		if k == endAt {
			want = wantReason
		}
		if got := result.Iterations[DefaultMaxToolRepeat+k].BreakReason; got != want {
			return fmt.Errorf("step %d (%s) break reason = %q, want %q", k, s, got, want)
		}
	}
	if last := result.Iterations[len(result.Iterations)-1]; last.BreakReason != wantReason {
		return fmt.Errorf("closing iteration break reason = %q, want %q", last.BreakReason, wantReason)
	}

	return checkOneResultPerCall(result.Messages)
}

// checkOneResultPerCall holds L4: each assistant message's calls are
// answered by that many tool messages, in the same order.
func checkOneResultPerCall(msgs []llm.Message) error {
	for i, m := range msgs {
		for j, tc := range m.ToolCalls {
			k := i + 1 + j
			if k >= len(msgs) || msgs[k].Role != "tool" || msgs[k].ToolCallID != tc.ID {
				return fmt.Errorf("call %s at message %d has no result in place", tc.ID, i)
			}
		}
	}
	return nil
}

func formatSteps(steps []loopStep) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = string(s)
	}
	return strings.Join(parts, " ")
}

// Every sequence of up to five steps, against the three smallest limits.
func TestEngineEndsTheRunOnRefusedStreaksForEverySequence(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			t.Parallel()
			failures := 0
			var walk func(steps []loopStep)
			walk = func(steps []loopStep) {
				if len(steps) > 0 && failures < 10 {
					if err := checkLoopRun(steps, limit); err != nil {
						failures++
						t.Errorf("[%s]: %v", formatSteps(steps), err)
					}
				}
				if len(steps) == 5 {
					return
				}
				for _, s := range allLoopSteps {
					walk(append(steps[:len(steps):len(steps)], s))
				}
			}
			walk(nil)
		})
	}
}

// Longer sequences, weighted toward refusals, at the default limit.
func TestEngineEndsTheRunOnRefusedStreaksAtTheDefaultLimit(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(1630, 1))
	weighted := []loopStep{
		stepRefused, stepRefused, stepRefused, stepRefused, stepRefusedTwo,
		stepRan, stepFailed, stepFailed, stepMixed, stepUnavailable,
	}
	failures := 0
	for range 4000 {
		steps := make([]loopStep, 1+rng.IntN(24))
		for i := range steps {
			steps[i] = weighted[rng.IntN(len(weighted))]
		}
		if err := checkLoopRun(steps, 0); err != nil {
			failures++
			t.Errorf("[%s]: %v", formatSteps(steps), err)
			if failures == 10 {
				return
			}
		}
	}
}

// The shapes the refusals took in production over thirty days: three runs
// that went on until max_iterations or a full context window, and runs
// that recovered after streaks of up to four refused iterations.
func TestEngineToolLoopProductionShapes(t *testing.T) {
	t.Parallel()

	repeat := func(s loopStep, n int) []loopStep {
		out := make([]loopStep, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	join := func(parts ...[]loopStep) []loopStep {
		var out []loopStep
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name       string
		steps      []loopStep
		wantCalls  int
		wantReason string
	}{
		{
			// It ran 51 iterations, 44 of them refused, to max_iterations.
			name:       "a sleep call resent after it ran three times",
			steps:      repeat(stepRefused, 44),
			wantCalls:  DefaultMaxToolRepeat + 5 + 1,
			wantReason: ExhaustToolLoop,
		},
		{
			// It filled the context window after 24 refused iterations in a row.
			name:       "a rejected write varied, then resent unchanged",
			steps:      join(repeat(stepFailed, 10), []loopStep{stepRefused}, repeat(stepFailed, 3), repeat(stepRefused, 24)),
			wantCalls:  DefaultMaxToolRepeat + 10 + 1 + 3 + 5 + 1,
			wantReason: ExhaustToolLoop,
		},
		{
			name:      "two refusal streaks, three and four long, then a write that landed",
			steps:     join(repeat(stepRefused, 3), repeat(stepFailed, 3), repeat(stepRefused, 4), []loopStep{stepRan}),
			wantCalls: DefaultMaxToolRepeat + 11 + 1,
		},
		{
			name:      "refused twice, then a reply",
			steps:     repeat(stepRefused, 2),
			wantCalls: DefaultMaxToolRepeat + 2 + 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// checkLoopRun holds the engine to the contract's reading of
			// the steps; this pins that reading for the shapes seen.
			endAt, reason := expectedLoopEnding(tc.steps, DefaultMaxToolLoopStrikes)
			ran := len(tc.steps)
			if endAt >= 0 {
				ran = endAt + 1
			}
			if reason != tc.wantReason || DefaultMaxToolRepeat+ran+1 != tc.wantCalls {
				t.Fatalf("ending %q after %d model calls, want %q after %d", reason, DefaultMaxToolRepeat+ran+1, tc.wantReason, tc.wantCalls)
			}
			if err := checkLoopRun(tc.steps, 0); err != nil {
				t.Fatal(err)
			}
		})
	}
}
