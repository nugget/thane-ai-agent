package delegate

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// mockLLMClient returns pre-configured responses in sequence.
type mockLLMClient struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	callIndex int
	calls     []mockCall
}

type mockCall struct {
	Model    string
	Messages []llm.Message
	Tools    []map[string]any
}

func (m *mockLLMClient) Chat(ctx context.Context, model string, messages []llm.Message, toolDefs []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, messages, toolDefs, nil)
}

func (m *mockLLMClient) ChatStream(_ context.Context, model string, messages []llm.Message, toolDefs []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, mockCall{Model: model, Messages: messages, Tools: toolDefs})

	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("mock: no more responses (call %d)", m.callIndex)
	}

	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

func (m *mockLLMClient) Ping(_ context.Context) error { return nil }

func newTestRegistry() *tools.Registry {
	r := tools.NewEmptyRegistry()
	r.Register(&tools.Tool{
		Name:        "get_state",
		Description: "Get entity state",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			entityID, _ := args["entity_id"].(string)
			return fmt.Sprintf("Entity: %s\nState: on", entityID), nil
		},
	})
	r.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "search results", nil
		},
	})
	// All delegate-family tools are registered so tests can verify
	// the full recursion guard, not just the deprecated alias. See
	// #820 — thane_now and thane_assign were originally missed by
	// the exclusion list. Iterating over the production
	// delegateFamilyToolNames slice keeps fixture coverage in lock-step
	// with the real exclusion list, so a future family addition
	// automatically extends the test surface.
	for _, name := range delegateFamilyToolNames {
		r.Register(&tools.Tool{
			Name:        name,
			Description: "delegate-family tool — should be excluded from delegate registries",
			Parameters:  map[string]any{},
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return "this should never be called by a delegate", nil
			},
		})
	}
	r.SetTagIndex(map[string][]string{
		"ha":  {"get_state"},
		"web": {"web_search"},
	})
	return r
}

type mockLoopRunner struct {
	onRun func(req looppkg.Request)
	resp  *looppkg.Response
	err   error
}

func (m *mockLoopRunner) Run(_ context.Context, req looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
	if m.onRun != nil {
		m.onRun(req)
	}
	if req.OnProgress != nil {
		req.OnProgress(events.KindLoopToolDone, map[string]any{"tool": "get_state"})
	}
	return m.resp, m.err
}

func TestExecute_LoopBackedPathUsesLaunch(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content:                  "delegate answer",
			Model:                    "deepslate/google/gemma-3-4b",
			FinishReason:             "stop",
			InputTokens:              120,
			OutputTokens:             42,
			CacheCreationInputTokens: 11,
			CacheReadInputTokens:     7,
			Iterations:               2,
			Exhausted:                false,
		},
	}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	result, err := exec.Execute(context.Background(), "Check the office light", "ha", "Be concise", nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "delegate answer" {
		t.Fatalf("Content = %q, want delegate answer", result.Content)
	}
	if result.Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("Model = %q", result.Model)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if result.InputTokens != 120 || result.OutputTokens != 42 {
		t.Fatalf("tokens = %d/%d", result.InputTokens, result.OutputTokens)
	}
	if result.CacheCreationInputTokens != 11 || result.CacheReadInputTokens != 7 {
		t.Fatalf("cache tokens = %d/%d", result.CacheCreationInputTokens, result.CacheReadInputTokens)
	}
	if result.Exhausted {
		t.Fatal("Exhausted = true, want false")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "get_state" || !result.ToolCalls[0].Success {
		t.Fatalf("ToolCalls = %#v", result.ToolCalls)
	}
	if captured.ConversationID == "" {
		t.Fatal("ConversationID = empty, want delegate conversation")
	}
	if captured.Model != "spark/gpt-oss:20b" {
		t.Fatalf("captured Model = %q, want spark/gpt-oss:20b", captured.Model)
	}
	if captured.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want loop-built prompt", captured.SystemPrompt)
	}
	if captured.PromptMode != agentctx.PromptModeTask {
		t.Fatalf("PromptMode = %q, want task", captured.PromptMode)
	}
	if !captured.SuppressAlwaysContext {
		t.Fatal("SuppressAlwaysContext = false, want true for task-mode delegate")
	}
	if len(captured.Messages) != 1 || !strings.Contains(captured.Messages[0].Content, "Check the office light") || !strings.Contains(captured.Messages[0].Content, "Be concise") {
		t.Fatalf("Messages = %#v", captured.Messages)
	}
	if captured.Hints["source"] != "delegate" {
		t.Fatalf("source hint = %q, want delegate", captured.Hints["source"])
	}
	if captured.Hints[router.HintDelegationGating] != "disabled" {
		t.Fatalf("delegation gating hint = %q, want disabled", captured.Hints[router.HintDelegationGating])
	}
	if captured.SkipTagFilter {
		t.Fatal("SkipTagFilter = true, want false for ha profile default tag")
	}
	if !containsString(captured.InitialTags, "ha") {
		t.Fatalf("InitialTags = %#v, want ha", captured.InitialTags)
	}
	if len(captured.AllowedTools) != 0 {
		t.Fatalf("AllowedTools = %#v, want loop tag filtering", captured.AllowedTools)
	}
	if captured.UsageRole != "delegate" || captured.UsageTaskName != "ha" {
		t.Fatalf("usage role/task = %q/%q", captured.UsageRole, captured.UsageTaskName)
	}
}

func TestExecute_LoopBackedInheritsCallerTags(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content: "delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}

	exec := NewExecutor(slog.Default(), nil, nil, taggedDelegateTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	ctx := tools.WithInheritableCapabilityTags(context.Background(), []string{"web", "message_channel"})
	if _, err := exec.Execute(ctx, "Search for something", "general", "", nil); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if captured.SkipTagFilter {
		t.Fatal("SkipTagFilter = true, want false for inherited tag-scoped delegate")
	}
	if !containsString(captured.InitialTags, "web") {
		t.Fatalf("InitialTags = %#v, want web", captured.InitialTags)
	}
	if containsString(captured.InitialTags, "message_channel") {
		t.Fatalf("InitialTags = %#v, should not inherit message_channel", captured.InitialTags)
	}
	if len(captured.AllowedTools) != 0 {
		t.Fatalf("AllowedTools = %#v, want loop tag filtering", captured.AllowedTools)
	}
}

func TestExecute_LoopBackedExplicitEmptyTagsExposeNoTools(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content: "delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	_, err := exec.execute(context.Background(), "No tools needed", "ha", "", []string{}, executionOptions{
		inheritCallerTags: false,
		explicitTagScope:  true,
	})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	wantExcluded := append([]string{"get_state", "web_search"}, delegateFamilyToolNames...)
	for _, want := range wantExcluded {
		if !containsString(captured.ExcludeTools, want) {
			t.Fatalf("ExcludeTools = %#v, want %s", captured.ExcludeTools, want)
		}
	}
}

// TestExecute_LoopBackedTagScopedExcludesDelegateFamily exercises the
// common delegate-launch path: caller specifies non-empty tags, so the
// AllToolNames-based wholesale exclusion at the explicit-empty-scope
// branch does not apply. This is the path the production failures
// from 2026-04-30 traversed — three consecutive delegates whose
// effective_tools log payloads still contained thane_now, thane_assign,
// and thane_delegate. The recursion guard must extend through this
// branch as well, not only the explicit-empty-scope one.
func TestExecute_LoopBackedTagScopedExcludesDelegateFamily(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content: "delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	_, err := exec.execute(context.Background(), "task", "ha", "", []string{"web"}, executionOptions{
		explicitTagScope: true,
	})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	for _, want := range delegateFamilyToolNames {
		if !containsString(captured.ExcludeTools, want) {
			t.Fatalf("ExcludeTools = %#v, want %s — delegate-family recursion guard must hold on the tag-scoped path too", captured.ExcludeTools, want)
		}
	}
}

// TestDelegateToolRegistry_ExcludesFullDelegateFamily is the regression
// test for #820. Delegate registries must exclude every member of the
// thane_* family — thane_delegate (deprecated), thane_now, and
// thane_assign — to prevent a delegate from spawning another delegate
// via the new front doors. Both branches of delegateToolRegistry are
// exercised: the tag-scoped branch (FilterByTags result) and the
// fall-through branch (no scope).
func TestDelegateToolRegistry_ExcludesFullDelegateFamily(t *testing.T) {
	t.Parallel()

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")

	t.Run("tag-scoped branch", func(t *testing.T) {
		reg := exec.delegateToolRegistry([]string{"web"}, false)
		names := reg.AllToolNames()
		for _, want := range delegateFamilyToolNames {
			if containsString(names, want) {
				t.Errorf("tag-scoped delegate registry contains %q; full family must be excluded (registry: %v)", want, names)
			}
		}
		// Sanity: the tag-matching tool should still be present.
		if !containsString(names, "web_search") {
			t.Errorf("tag-scoped delegate registry missing tag-matching tool web_search (registry: %v)", names)
		}
	})

	t.Run("fall-through branch", func(t *testing.T) {
		reg := exec.delegateToolRegistry(nil, false)
		names := reg.AllToolNames()
		for _, want := range delegateFamilyToolNames {
			if containsString(names, want) {
				t.Errorf("fall-through delegate registry contains %q; full family must be excluded (registry: %v)", want, names)
			}
		}
		// Sanity: non-family tools survive.
		if !containsString(names, "get_state") {
			t.Errorf("fall-through delegate registry missing non-family tool get_state (registry: %v)", names)
		}
	})
}

func TestExecute_LoopBackedExplicitEmptyTagsWithAlwaysActiveTagsDoNotBypassFiltering(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content: "delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())
	exec.SetAlwaysActiveTags([]string{"web"})

	_, err := exec.execute(context.Background(), "No tools needed", "ha", "", []string{}, executionOptions{
		inheritCallerTags: false,
		explicitTagScope:  true,
	})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	if captured.SkipTagFilter {
		t.Fatal("SkipTagFilter = true, want false for explicit empty tag scope with always-active tags")
	}
	if !containsString(captured.InitialTags, "web") {
		t.Fatalf("InitialTags = %#v, want always-active web tag", captured.InitialTags)
	}
	if containsString(captured.InitialTags, "ha") {
		t.Fatalf("InitialTags = %#v, should not include ha profile default for explicit empty tag scope", captured.InitialTags)
	}
	// ExcludeTools may carry the delegate-family recursion guard, but
	// must not include any of the regular tag-gated tools — the
	// always-active tags should expand the filter scope so tag-based
	// filtering does the work via InitialTags, not via wholesale
	// AllToolNames exclusion.
	for _, got := range captured.ExcludeTools {
		if !containsString(delegateFamilyToolNames, got) {
			t.Fatalf("ExcludeTools = %#v, includes non-family tool %q — tag filtering should route through InitialTags", captured.ExcludeTools, got)
		}
	}
}

// TestEmptyResponseError_PropagatesChildLastError pins the error
// surface for the case where a delegate's joined launch finishes
// without a Response. Production failures on 2026-04-30 surfaced as
// "delegate failed: joined launch completed without response" — a
// generic message that hid the child loop's underlying tool-call
// parse error from gpt-oss:120b. The parent had no way to decide
// whether to retry or change strategy. With FinalStatus populated,
// the helper must surface the child's LastError verbatim.
func TestEmptyResponseError_PropagatesChildLastError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result looppkg.LaunchResult
		want   string
	}{
		{
			name:   "no final status falls back to opaque message",
			result: looppkg.LaunchResult{},
			want:   "delegate failed: joined launch completed without response",
		},
		{
			name: "final status with empty last error falls back to opaque message",
			result: looppkg.LaunchResult{
				FinalStatus: &looppkg.Status{LastError: ""},
			},
			want: "delegate failed: joined launch completed without response",
		},
		{
			name: "final status with last error surfaces the child error verbatim",
			result: looppkg.LaunchResult{
				FinalStatus: &looppkg.Status{
					LastError: `loop LLM call: API error 500: error parsing tool call: raw='{"account":"github":"primary"}'`,
				},
			},
			want: `delegate failed: loop LLM call: API error 500: error parsing tool call: raw='{"account":"github":"primary"}'`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := emptyResponseError(tc.result)
			if got == nil || got.Error() != tc.want {
				t.Fatalf("emptyResponseError() = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestEmptyResponseError_TruncatesOversizedLastError ensures a child
// loop's runaway LastError (e.g. a stack trace, a multi-KB response
// body dump) is truncated before it becomes the parent's tool-call
// result. Without this, a single misbehaving error could inflate
// every subsequent parent prompt by however much the loop runtime
// happened to capture.
func TestEmptyResponseError_TruncatesOversizedLastError(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", childLastErrorMaxLen*4)
	err := emptyResponseError(looppkg.LaunchResult{
		FinalStatus: &looppkg.Status{LastError: huge},
	})
	if err == nil {
		t.Fatal("emptyResponseError() = nil, want truncated error")
	}
	const prefix = "delegate failed: "
	got := err.Error()
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("error = %q, want prefix %q", got, prefix)
	}
	// The body after the prefix is the truncated LastError, possibly
	// with a "..." marker appended by truncate().
	body := got[len(prefix):]
	if len(body) > childLastErrorMaxLen+len("...") {
		t.Fatalf("body length = %d, want <= %d (cap %d + ellipsis)", len(body), childLastErrorMaxLen+len("..."), childLastErrorMaxLen)
	}
	if !strings.HasSuffix(body, "...") {
		t.Fatalf("body = %q, want truncation marker", body)
	}
}

// TestMergeExcludeToolNames pins the dedup invariant for the
// composed exclusion list. The launched-loop path always appends
// delegateFamilyToolNames, and the explicit-empty-scope branch
// independently sources AllToolNames (which already contains the
// family). Both contributing without dedup produces noise; the
// merge helper must collapse to a single sorted set.
func TestMergeExcludeToolNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		groups [][]string
		want   []string
	}{
		{
			name:   "empty input returns nil",
			groups: nil,
			want:   nil,
		},
		{
			name:   "single group sorted and deduped",
			groups: [][]string{{"b", "a", "b", ""}},
			want:   []string{"a", "b"},
		},
		{
			name: "overlapping groups dedup to a single sorted slice",
			groups: [][]string{
				{"get_state", "thane_now", "thane_assign"},
				{"thane_delegate", "thane_now", "thane_assign"},
			},
			want: []string{"get_state", "thane_assign", "thane_delegate", "thane_now"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mergeExcludeToolNames(tc.groups...)
			if len(got) != len(tc.want) {
				t.Fatalf("mergeExcludeToolNames() = %#v, want %#v", got, tc.want)
			}
			for i, name := range tc.want {
				if got[i] != name {
					t.Fatalf("mergeExcludeToolNames()[%d] = %q, want %q (got=%#v)", i, got[i], name, got)
				}
			}
		})
	}
}

// TestExecute_LoopBackedExplicitEmptyScopeNoDuplicateExcludes pins the
// dedup contract through the actual launch path. When AllToolNames
// (which contains the family) and delegateFamilyToolNames both
// contribute, ExcludeTools must contain each family name exactly once.
func TestExecute_LoopBackedExplicitEmptyScopeNoDuplicateExcludes(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content: "delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	_, err := exec.execute(context.Background(), "No tools needed", "ha", "", []string{}, executionOptions{
		inheritCallerTags: false,
		explicitTagScope:  true,
	})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}

	for _, name := range delegateFamilyToolNames {
		count := 0
		for _, got := range captured.ExcludeTools {
			if got == name {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("ExcludeTools = %#v contains %q %d times, want exactly 1 (recursion guard must dedup against AllToolNames)", captured.ExcludeTools, name, count)
		}
	}
}

func TestExecute_EmptyTask(t *testing.T) {
	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")
	exec.ConfigureLoopExecution(&mockLoopRunner{}, looppkg.NewRegistry())
	_, err := exec.Execute(context.Background(), "", "general", "", nil)

	if err == nil {
		t.Fatal("Execute() with empty task should return error")
	}
}

// TestExecute_RequiresLoopExecutionWiring guards the contract that
// NewExecutor returns an executor that is not usable until
// ConfigureLoopExecution has been called. The error message must point
// callers at the missing wiring step so future refactors do not silently
// reintroduce a fallback path or a less actionable error.
func TestExecute_RequiresLoopExecutionWiring(t *testing.T) {
	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")
	// No ConfigureLoopExecution call.

	_, err := exec.Execute(context.Background(), "Check the office light", "general", "", nil)
	if err == nil {
		t.Fatal("Execute() without loops-ng wiring should return error")
	}
	if !strings.Contains(err.Error(), "ConfigureLoopExecution") {
		t.Errorf("error %q should mention ConfigureLoopExecution", err)
	}
}

// TestStartBackground_RequiresLoopExecutionWiring is the StartBackground
// counterpart to TestExecute_RequiresLoopExecutionWiring. Same rationale:
// the contract is that loops-ng wiring is mandatory and the failure mode
// must be actionable.
func TestStartBackground_RequiresLoopExecutionWiring(t *testing.T) {
	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")

	_, err := exec.StartBackground(context.Background(), "Check the office light", "general", "", nil)
	if err == nil {
		t.Fatal("StartBackground() without loops-ng wiring should return error")
	}
	if !strings.Contains(err.Error(), "loops-ng") {
		t.Errorf("error %q should mention loops-ng wiring", err)
	}
}

func TestToolHandler_EmptyTask(t *testing.T) {
	exec := NewExecutor(slog.Default(), &mockLLMClient{}, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{})

	if err != nil {
		t.Fatalf("ToolHandler() error = %v, want nil", err)
	}
	if !strings.Contains(result, "Error: task is required") {
		t.Errorf("result = %q, want to contain 'task is required'", result)
	}
}

func TestToolHandler_DefaultProfile(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  50,
				OutputTokens: 10,
			},
		},
	}

	exec := NewExecutor(slog.Default(), mock, nil, newTestRegistry(), "test-model")
	handler := ToolHandler(exec)

	result, err := handler(context.Background(), map[string]any{
		"task": "Do something",
	})

	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "profile=general") {
		t.Errorf("result = %q, want to contain 'profile=general'", result)
	}
}

func TestBuiltinProfiles_GeneralForcesLocalOnly(t *testing.T) {
	profiles := builtinProfiles()
	general, ok := profiles["general"]
	if !ok {
		t.Fatal("missing 'general' profile")
	}

	if general.RouterHints == nil {
		t.Fatal("general profile RouterHints is nil, want HintLocalOnly=true")
	}
	if general.RouterHints[router.HintLocalOnly] != "true" {
		t.Errorf("general profile HintLocalOnly = %q, want %q",
			general.RouterHints[router.HintLocalOnly], "true")
	}
	if general.RouterHints[router.HintQualityFloor] != "5" {
		t.Errorf("general profile HintQualityFloor = %q, want %q",
			general.RouterHints[router.HintQualityFloor], "5")
	}
	if general.RouterHints[router.HintPreferSpeed] != "true" {
		t.Errorf("general profile HintPreferSpeed = %q, want %q",
			general.RouterHints[router.HintPreferSpeed], "true")
	}
}

func TestBuiltinProfiles_HAForcesLocalOnly(t *testing.T) {
	profiles := builtinProfiles()
	ha, ok := profiles["ha"]
	if !ok {
		t.Fatal("missing 'ha' profile")
	}

	if ha.RouterHints[router.HintLocalOnly] != "true" {
		t.Errorf("ha profile HintLocalOnly = %q, want %q",
			ha.RouterHints[router.HintLocalOnly], "true")
	}
	if ha.RouterHints[router.HintMission] != "device_control" {
		t.Errorf("ha profile HintMission = %q, want %q",
			ha.RouterHints[router.HintMission], "device_control")
	}
	if ha.RouterHints[router.HintQualityFloor] != "4" {
		t.Errorf("ha profile HintQualityFloor = %q, want %q",
			ha.RouterHints[router.HintQualityFloor], "4")
	}
	if ha.RouterHints[router.HintPreferSpeed] != "true" {
		t.Errorf("ha profile HintPreferSpeed = %q, want %q",
			ha.RouterHints[router.HintPreferSpeed], "true")
	}
	if len(ha.DefaultTags) != 1 || ha.DefaultTags[0] != "ha" {
		t.Fatalf("ha profile DefaultTags = %#v, want [ha]", ha.DefaultTags)
	}
}

func TestExecute_LoopBackedDelegateRequiresStreamingCapableModel(t *testing.T) {
	rtr := router.NewRouter(slog.Default(), router.Config{
		DefaultModel: "local-model",
		LocalFirst:   true,
		Models: []router.Model{
			{Name: "local-model", Provider: "ollama", SupportsTools: true, SupportsStreaming: false, Speed: 8, Quality: 5, CostTier: 0, ContextWindow: 8192},
			{Name: "cloud-model", Provider: "anthropic", SupportsTools: true, SupportsStreaming: true, Speed: 6, Quality: 10, CostTier: 3, ContextWindow: 8192},
		},
		MaxAuditLog: 10,
	})

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content:      "streaming-capable delegate answer",
			Model:        "cloud-model",
			FinishReason: "stop",
		},
	}

	exec := NewExecutor(slog.Default(), nil, rtr, newTestRegistry(), "local-model")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	result, err := exec.Execute(context.Background(), "Inspect the current working directory", "general", "", nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if captured.Model != "cloud-model" {
		t.Fatalf("captured model = %q, want cloud-model", captured.Model)
	}
	if result.Content != "streaming-capable delegate answer" {
		t.Fatalf("Content = %q, want streaming-capable delegate answer", result.Content)
	}
}

// delegateCompletionSink is a test stub for the loops-ng completion
// delivery interface used by background delegate tests.
type delegateCompletionSink struct {
	deliveries chan looppkg.CompletionDelivery
}

func TestDefaultBudgets(t *testing.T) {
	if defaultMaxIter != 15 {
		t.Errorf("defaultMaxIter = %d, want 15", defaultMaxIter)
	}
	if defaultMaxTokens != 25000 {
		t.Errorf("defaultMaxTokens = %d, want 25000", defaultMaxTokens)
	}
	if defaultMaxDuration != 90*time.Second {
		t.Errorf("defaultMaxDuration = %v, want 90s", defaultMaxDuration)
	}
}

// TestNowToolHandler_RoutesToSyncPath verifies that thane_now invokes
// the sync execution path via the shared executor and returns the
// formatted SUCCESS header.
func TestNowToolHandler_RoutesToSyncPath(t *testing.T) {
	t.Parallel()

	runner := &mockLoopRunner{
		resp: &looppkg.Response{
			Content: "answer",
			Model:   "test-model",
		},
	}
	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "test-model")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	result, err := NowToolHandler(exec)(context.Background(), map[string]any{
		"task": "What's the office temperature?",
	})
	if err != nil {
		t.Fatalf("NowToolHandler error: %v", err)
	}
	if !strings.Contains(result, "[Delegate SUCCEEDED:") {
		t.Fatalf("expected SUCCESS header, got: %s", result)
	}
	if !strings.Contains(result, "answer") {
		t.Fatalf("expected delegate content in result, got: %s", result)
	}
}

func TestNowToolHandler_ContextModeFull(t *testing.T) {
	t.Parallel()

	var captured looppkg.Request
	runner := &mockLoopRunner{
		onRun: func(req looppkg.Request) {
			captured = req
		},
		resp: &looppkg.Response{
			Content: "answer",
			Model:   "test-model",
		},
	}
	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "test-model")
	exec.ConfigureLoopExecution(runner, looppkg.NewRegistry())

	result, err := NowToolHandler(exec)(context.Background(), map[string]any{
		"task":         "Inspect the continuity-sensitive issue.",
		"context_mode": "full",
	})
	if err != nil {
		t.Fatalf("NowToolHandler error: %v", err)
	}
	if !strings.Contains(result, "[Delegate SUCCEEDED:") {
		t.Fatalf("expected SUCCESS header, got: %s", result)
	}
	if captured.PromptMode != agentctx.PromptModeFull {
		t.Fatalf("PromptMode = %q, want full", captured.PromptMode)
	}
	if captured.SuppressAlwaysContext {
		t.Fatal("SuppressAlwaysContext = true, want false for full-context delegate")
	}
}

func TestNowToolHandler_InvalidContextMode(t *testing.T) {
	t.Parallel()

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "test-model")
	exec.ConfigureLoopExecution(&mockLoopRunner{}, looppkg.NewRegistry())

	result, err := NowToolHandler(exec)(context.Background(), map[string]any{
		"task":         "Do something.",
		"context_mode": "everything",
	})
	if err != nil {
		t.Fatalf("NowToolHandler error: %v", err)
	}
	if !strings.Contains(result, "context_mode must be one of [task, full]") {
		t.Fatalf("unexpected result: %s", result)
	}
}

// TestAssignToolHandler_RoutesToAsyncPath verifies that thane_assign
// invokes the async (background) execution path and returns the
// STARTED header with a loop_id.
func TestAssignToolHandler_RoutesToAsyncPath(t *testing.T) {
	t.Parallel()

	runner := &mockLoopRunner{
		resp: &looppkg.Response{
			Content: "background answer",
			Model:   "test-model",
		},
	}
	registry := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		registry.ShutdownAll(shutdownCtx)
	})
	sink := &delegateCompletionSink{deliveries: make(chan looppkg.CompletionDelivery, 1)}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "test-model")
	exec.ConfigureLoopExecution(runner, registry)
	exec.ConfigureLoopCompletionSink(sink.DeliverCompletion)

	ctx := tools.WithConversationID(context.Background(), "conv-async")
	result, err := AssignToolHandler(exec)(ctx, map[string]any{
		"task": "Investigate the slow query.",
	})
	if err != nil {
		t.Fatalf("AssignToolHandler error: %v", err)
	}
	if !strings.Contains(result, "[Delegate STARTED:") {
		t.Fatalf("expected STARTED header, got: %s", result)
	}
	if !strings.Contains(result, "loop_id=") {
		t.Fatalf("expected loop_id in result, got: %s", result)
	}
}

// TestToolHandler_AsyncModeLaunchesBackgroundDelegate verifies the
// deprecated thane_delegate(mode=async) compatibility path still
// reaches the async executor.
func TestToolHandler_AsyncModeLaunchesBackgroundDelegate(t *testing.T) {
	t.Parallel()

	runner := &mockLoopRunner{
		resp: &looppkg.Response{
			Content: "Background delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}
	registry := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		registry.ShutdownAll(shutdownCtx)
	})
	sink := &delegateCompletionSink{deliveries: make(chan looppkg.CompletionDelivery, 1)}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(runner, registry)
	exec.ConfigureLoopCompletionSink(sink.DeliverCompletion)

	handler := ToolHandler(exec)
	ctx := tools.WithConversationID(context.Background(), "conv-async")
	result, err := handler(ctx, map[string]any{
		"task": "Check the office light",
		"mode": "async",
	})
	if err != nil {
		t.Fatalf("ToolHandler() error = %v", err)
	}
	if !strings.Contains(result, "[Delegate STARTED:") {
		t.Fatalf("result = %q, want async started header", result)
	}
	if !strings.Contains(result, "current conversation or interactive channel") {
		t.Fatalf("result = %q, want generic async delivery description", result)
	}

	select {
	case delivery := <-sink.deliveries:
		if delivery.ConversationID != "conv-async" {
			t.Fatalf("ConversationID = %q, want conv-async", delivery.ConversationID)
		}
		if delivery.Mode != looppkg.CompletionConversation {
			t.Fatalf("Mode = %q, want conversation", delivery.Mode)
		}
		if delivery.Response == nil || delivery.Response.Content != "Background delegate answer" {
			t.Fatalf("Response = %#v, want background delegate answer", delivery.Response)
		}
		if !strings.Contains(delivery.Content, "Background task complete") {
			t.Fatalf("Content = %q, want background completion message", delivery.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for detached completion delivery")
	}
}

func TestStartBackground_UsesSignalChannelCompletionTarget(t *testing.T) {
	t.Parallel()

	registry := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		registry.ShutdownAll(shutdownCtx)
	})
	sink := &delegateCompletionSink{deliveries: make(chan looppkg.CompletionDelivery, 1)}

	exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
	exec.ConfigureLoopExecution(&mockLoopRunner{
		resp: &looppkg.Response{
			Content: "Signal delegate answer",
			Model:   "deepslate/google/gemma-3-4b",
		},
	}, registry)
	exec.ConfigureLoopCompletionSink(sink.DeliverCompletion)

	ctx := tools.WithConversationID(context.Background(), "signal-15551234567")
	ctx = tools.WithHints(ctx, map[string]string{
		"source": "signal",
		"sender": "+15551234567",
	})

	if _, err := exec.StartBackground(ctx, "Check the office light", "", "", nil); err != nil {
		t.Fatalf("StartBackground() error = %v", err)
	}

	select {
	case delivery := <-sink.deliveries:
		if delivery.Mode != looppkg.CompletionChannel {
			t.Fatalf("Mode = %q, want channel", delivery.Mode)
		}
		if delivery.Channel == nil {
			t.Fatal("Channel = nil, want target")
		}
		if delivery.Channel.Channel != "signal" || delivery.Channel.Recipient != "+15551234567" || delivery.Channel.ConversationID != "signal-15551234567" {
			t.Fatalf("Channel = %#v", delivery.Channel)
		}
		if delivery.Response == nil || delivery.Response.Content != "Signal delegate answer" {
			t.Fatalf("Response = %#v, want signal delegate answer", delivery.Response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal channel completion delivery")
	}
}

func (s *delegateCompletionSink) DeliverCompletion(_ context.Context, delivery looppkg.CompletionDelivery) error {
	s.deliveries <- delivery
	return nil
}

// TestExecute_NonCooperativeToolTimeout verifies that the delegate
// recovers from a tool handler that blocks forever without checking
// ctx.Done(). This was the root cause of issue #508 where delegates
// hung indefinitely on non-cooperative tools.
func taggedDelegateTestRegistry() *tools.Registry {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "results", nil
		},
	})
	reg.Register(&tools.Tool{
		Name:        "get_state",
		Description: "HA state",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "on", nil
		},
	})
	reg.Register(&tools.Tool{
		Name:        "send_reaction",
		Description: "React",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "reacted", nil
		},
	})
	reg.Register(&tools.Tool{
		Name:        "owner_contact",
		Description: "Owner contact",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "owner", nil
		},
	})
	reg.Register(&tools.Tool{
		Name:        "thane_delegate",
		Description: "Delegate",
		Parameters:  map[string]any{},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "should not be called", nil
		},
	})
	reg.SetTagIndex(map[string][]string{
		"web":             {"web_search"},
		"ha":              {"get_state"},
		"message_channel": {"send_reaction"},
		"owner":           {"owner_contact"},
	})
	return reg
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
