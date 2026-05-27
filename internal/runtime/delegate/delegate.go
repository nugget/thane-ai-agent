package delegate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// delegateFamilyToolNames are the tool names excluded from delegate
// registries to prevent recursion. All members of the delegate family
// must appear here: a delegate that can call any of them can spawn
// another delegate, which is exactly the structural recursion the
// exclusion is meant to prevent. The family currently includes
// thane_now (sync) and thane_assign (async). When adding a new
// family member, add its name here in the same change.
var delegateFamilyToolNames = []string{
	"thane_now",
	"thane_assign",
}

func delegateToolExclusions() []string {
	return mergeTagLists(delegateFamilyToolNames, tools.DirectHumanEgressToolNames())
}

// Exhaustion reason constants are defined in the [iterate] package.
// These aliases preserve backward compatibility for consumers that
// reference them via the delegate package.
const (
	ExhaustMaxIterations = iterate.ExhaustMaxIterations
	ExhaustTokenBudget   = iterate.ExhaustTokenBudget
	ExhaustWallClock     = iterate.ExhaustWallClock
	ExhaustNoOutput      = iterate.ExhaustNoOutput
	ExhaustIllegalTool   = iterate.ExhaustIllegalTool
)

type executionOptions struct {
	inheritCallerTags bool
	explicitTagScope  bool
	promptMode        agentctx.PromptMode
}

func defaultExecutionOptions() executionOptions {
	return executionOptions{
		inheritCallerTags: true,
		promptMode:        agentctx.PromptModeTask,
	}
}

func (o executionOptions) effectivePromptMode() agentctx.PromptMode {
	if o.promptMode == "" {
		return agentctx.PromptModeTask
	}
	return o.promptMode
}

func mergeDelegateScopeTags(ctx context.Context, explicitTags []string, inheritCallerTags bool, explicitTagScope bool) (scopeTags []string, inheritedTags []string, droppedTags []string, explicitScopeRequested bool) {
	scope := make(map[string]bool)
	inherited := make(map[string]bool)
	dropped := make(map[string]bool)
	explicitScopeRequested = explicitTagScope

	if inheritCallerTags {
		for _, tag := range tools.InheritableCapabilityTagsFromContext(ctx) {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			if nonDelegableCapabilityTag(tag) {
				dropped[tag] = true
				continue
			}
			scope[tag] = true
			inherited[tag] = true
		}
	}

	for _, tag := range explicitTags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		explicitScopeRequested = true
		if nonDelegableCapabilityTag(tag) {
			dropped[tag] = true
			continue
		}
		scope[tag] = true
	}

	scopeTags = make([]string, 0, len(scope))
	for tag := range scope {
		scopeTags = append(scopeTags, tag)
	}
	sort.Strings(scopeTags)
	inheritedTags = make([]string, 0, len(inherited))
	for tag := range inherited {
		inheritedTags = append(inheritedTags, tag)
	}
	sort.Strings(inheritedTags)

	droppedTags = make([]string, 0, len(dropped))
	for tag := range dropped {
		droppedTags = append(droppedTags, tag)
	}
	sort.Strings(droppedTags)
	return scopeTags, inheritedTags, droppedTags, explicitScopeRequested
}

func nonDelegableCapabilityTag(tag string) bool {
	switch tag {
	case "message_channel", "owner":
		return true
	default:
		return false
	}
}

func applyRunPolicyDefaultTags(scopeTags []string, policy *RunPolicy, explicitScopeRequested bool) (mergedTags []string, appliedDefaults []string) {
	mergedTags = append([]string(nil), scopeTags...)
	if explicitScopeRequested || policy == nil || len(policy.DefaultTags) == 0 {
		return mergedTags, nil
	}

	seen := make(map[string]bool, len(mergedTags)+len(policy.DefaultTags))
	for _, tag := range mergedTags {
		seen[tag] = true
	}
	for _, tag := range policy.DefaultTags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] || nonDelegableCapabilityTag(tag) {
			continue
		}
		seen[tag] = true
		mergedTags = append(mergedTags, tag)
		appliedDefaults = append(appliedDefaults, tag)
	}
	sort.Strings(mergedTags)
	sort.Strings(appliedDefaults)
	return mergedTags, appliedDefaults
}

// delegateToolRegistry builds the catalog a delegate's executor sees
// during its in-process setup phase (e.g. model-routing sizing in
// selectModel). Two phases:
//
//  1. Optional tag narrowing. When the caller requested an explicit
//     scope or supplied non-empty scope tags, narrow the parent
//     registry by the union of scope tags and operator core tags.
//     FilterByTags preserves Tool.Core members so the tag-navigation
//     surface stays reachable. When the union is empty
//     (explicit-empty-scope with no core tags configured),
//     FilteredCopy(nil) yields a zero-tool registry — FilteredCopy
//     does not preserve Tool.Core, so the explicit-empty-scope
//     delegate sees no tools at all. That's the documented contract
//     covered by TestExecute_LoopBackedExplicitEmptyTagsExposeNoTools.
//
//  2. Recursion guard. Strip the delegate family
//     ([delegateToolExclusions]) so a delegate can't spawn another
//     delegate. This step runs unconditionally — both the narrowed
//     and unnarrowed paths funnel through it, single source of truth
//     for the exclusion set.
func (e *Executor) delegateToolRegistry(scopeTags []string, explicitScopeRequested bool) *tools.Registry {
	reg := e.parentReg
	if len(scopeTags) > 0 || explicitScopeRequested {
		merged := mergeTagLists(scopeTags, e.coreTags)
		if len(merged) > 0 {
			reg = reg.FilterByTags(merged)
		} else {
			reg = reg.FilteredCopy(nil)
		}
	}
	return reg.FilteredCopyExcluding(delegateToolExclusions())
}

func mergeTagLists(tagGroups ...[]string) []string {
	seen := make(map[string]bool)
	for _, tags := range tagGroups {
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				seen[tag] = true
			}
		}
	}
	merged := make([]string, 0, len(seen))
	for tag := range seen {
		merged = append(merged, tag)
	}
	sort.Strings(merged)
	return merged
}

// ToolCallOutcome records the name and success/failure of a single tool
// invocation during delegate execution.
type ToolCallOutcome struct {
	Name    string `json:"name"`
	Success bool   `json:"success"`
}

// Result is the outcome of a delegated task execution.
type Result struct {
	// RunPolicyName is the [RunPolicy.Name] selected for this run.
	// The JSON tag stays "profile" because the operator-facing
	// vocabulary, dashboards, and log-parsing scripts predate the
	// internal Profile→RunPolicy rename; changing the wire field
	// would break those surfaces without delivering value.
	RunPolicyName string `json:"profile"`
	// Content is the assistant text the delegate produced. Empty when
	// the delegate exhausted without producing output (Exhausted is
	// true and ExhaustReason names which budget ran out), or when the
	// delegate completed but produced only tool calls with no final
	// answer (Exhausted=true, ExhaustReason=ExhaustNoOutput). Check
	// Exhausted before treating an empty Content as success.
	Content                  string            `json:"content"`
	Model                    string            `json:"model"`
	Iterations               int               `json:"iterations"`
	InputTokens              int               `json:"input_tokens"`
	OutputTokens             int               `json:"output_tokens"`
	CacheCreationInputTokens int               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int               `json:"cache_read_input_tokens"`
	Exhausted                bool              `json:"exhausted"`
	ExhaustReason            string            `json:"exhaust_reason,omitempty"`
	ToolCalls                []ToolCallOutcome `json:"tool_calls,omitempty"`
	Duration                 time.Duration     `json:"duration"`
}

// labelExpander expands temp file labels in task descriptions. Defined
// as an interface to avoid a circular import between delegate and tools.
type labelExpander interface {
	ExpandLabels(convID, text string) string
}

// Executor runs delegate sub-agent tasks.
type Executor struct {
	logger          *slog.Logger
	llm             llm.Client
	router          *router.Router
	parentReg       *tools.Registry
	runPolicies     map[string]*RunPolicy
	timezone        string
	defaultModel    string
	archiver        *memory.ArchiveStore
	tempFiles       labelExpander
	coreTags        []string
	lensProvider    func() []string // returns active global lenses (nil = none)
	eventBus        *events.Bus
	loopRunner      looppkg.Runner
	loopRegistry    *looppkg.Registry
	completionSink  looppkg.CompletionSink
	sessionArchiver agent.SessionArchiver
	conversations   *memory.SQLiteStore
}

// NewExecutor creates a delegate executor. The returned executor is not
// usable until [Executor.ConfigureLoopExecution] has been called with a
// non-nil runner and registry; without those, [Executor.Execute] and
// [Executor.StartBackground] will fail. The remaining Configure* and
// Set* methods supply optional cross-cutting wiring.
func NewExecutor(logger *slog.Logger, llmClient llm.Client, rtr *router.Router, parentReg *tools.Registry, defaultModel string) *Executor {
	return &Executor{
		logger:       logger,
		llm:          llmClient,
		router:       rtr,
		parentReg:    parentReg,
		runPolicies:  builtinRunPolicies(),
		defaultModel: defaultModel,
	}
}

// ApplyRunPolicyOverrides applies configuration overrides to the
// builtin run policies. Only positive fields in each override replace
// the builtin defaults; zero and negative fields are ignored. Unknown
// policy names are silently ignored (config may reference names that
// don't exist yet). The map key matches the operator's YAML key under
// `delegate.profiles.<name>`.
func (e *Executor) ApplyRunPolicyOverrides(overrides map[string]RunPolicyOverride) {
	for name, o := range overrides {
		p, ok := e.runPolicies[name]
		if !ok {
			continue
		}
		if o.ToolTimeout > 0 {
			p.ToolTimeout = o.ToolTimeout
		}
		if o.MaxDuration > 0 {
			p.MaxDuration = o.MaxDuration
		}
		if o.MaxIter > 0 {
			p.MaxIter = o.MaxIter
		}
		if o.MaxTokens > 0 {
			p.MaxTokens = o.MaxTokens
		}
	}
}

// RunPolicyOverride is the partial-update shape operators use to
// adjust one or more budgets on a builtin run policy without having
// to restate the whole policy. The struct is intentionally
// all-zero-valued by default — operators only fill the fields they
// want to override, and [Executor.ApplyRunPolicyOverrides] leaves
// builtin values in place wherever the override is zero.
type RunPolicyOverride struct {
	// ToolTimeout, when positive, replaces the builtin policy's
	// per-tool-call timeout. Zero leaves the builtin unchanged.
	ToolTimeout time.Duration
	// MaxDuration, when positive, replaces the builtin policy's
	// wall-clock cap on the whole delegate loop. Zero leaves the
	// builtin unchanged.
	MaxDuration time.Duration
	// MaxIter, when positive, replaces the builtin policy's maximum
	// tool-calling iteration count. Zero leaves the builtin
	// unchanged.
	MaxIter int
	// MaxTokens, when positive, replaces the builtin policy's
	// cumulative output-token budget. Zero leaves the builtin
	// unchanged.
	MaxTokens int
}

// SetTimezone configures the IANA timezone used in delegate logging and
// archive metadata.
func (e *Executor) SetTimezone(tz string) {
	e.timezone = tz
}

// SetArchiver configures the archive store for session lifecycle
// tracking. When set, each [Executor.Execute] call creates a first-class
// archive session with parent linkage, and archives its messages and
// tool calls for inspection in the web dashboard.
func (e *Executor) SetArchiver(a *memory.ArchiveStore) {
	e.archiver = a
}

// SetTempFileStore configures temp file label expansion for delegate
// task descriptions. When set, occurrences of "temp:LABEL" in the task
// and guidance strings are replaced with actual file paths before the
// delegate LLM sees the message.
func (e *Executor) SetTempFileStore(tfs interface {
	ExpandLabels(convID, text string) string
}) {
	e.tempFiles = tfs
}

// SetEventBus configures the event bus for delegate lifecycle events.
// When set, each Execute call publishes spawn and complete events so
// the dashboard can render delegates as ephemeral child nodes.
func (e *Executor) SetEventBus(bus *events.Bus) {
	e.eventBus = bus
}

// SetCoreTags configures the capability tags that are automatically
// included in every tag-scoped delegation, regardless of which tags
// the caller requests. These are the operator's core tag set —
// pinned in every scope so the delegate's surface stays consistent
// with the parent loop's baseline.
func (e *Executor) SetCoreTags(tags []string) {
	e.coreTags = tags
}

// SetLensProvider configures a function that returns the currently
// active global lenses. These are merged into every delegate execution's
// effective tag set so lens-tagged KB articles and talents apply.
func (e *Executor) SetLensProvider(fn func() []string) {
	e.lensProvider = fn
}

// ConfigureLoopExecution configures loop-backed delegate execution. When both
// runner and registry are set, Execute launches a one-shot child loop through
// the shared loops path, giving delegates the same telemetry path as other
// loop-driven work.
func (e *Executor) ConfigureLoopExecution(runner looppkg.Runner, registry *looppkg.Registry) {
	e.loopRunner = runner
	e.loopRegistry = registry
}

// ConfigureLoopCompletionSink configures the detached completion sink used by
// background delegate launches that report back into a conversation.
func (e *Executor) ConfigureLoopCompletionSink(sink looppkg.CompletionSink) {
	e.completionSink = sink
}

// ConfigureSessionLifecycle configures archival and cleanup for loop-backed
// delegate conversations. The archiver preserves parent session linkage and
// archived transcripts; the conversation store is cleared after completion so
// ephemeral delegate turns do not accumulate in working memory.
func (e *Executor) ConfigureSessionLifecycle(archiver agent.SessionArchiver, store *memory.SQLiteStore) {
	e.sessionArchiver = archiver
	e.conversations = store
}

// RunPolicyNames returns the names of all registered run policies.
// The names match the operator's YAML keys under
// `delegate.profiles.<name>`.
func (e *Executor) RunPolicyNames() []string {
	names := make([]string, 0, len(e.runPolicies))
	for name := range e.runPolicies {
		names = append(names, name)
	}
	return names
}

// Execute runs a delegated task under the named run policy. Capability
// tags define the delegate's tool and context scope. Elective caller
// tags are inherited by default; the policy's DefaultTags merge in
// only when the caller did not request an explicit scope.
//
// profileName is the YAML lookup key (`delegate.profiles.<name>`) for
// the run policy and is preserved as the argument name to match the
// operator-facing config vocabulary.
func (e *Executor) Execute(ctx context.Context, task, profileName, guidance string, tags []string) (*Result, error) {
	return e.execute(ctx, task, profileName, guidance, tags, defaultExecutionOptions())
}

func (e *Executor) execute(ctx context.Context, task, profileName, guidance string, tags []string, opts executionOptions) (*Result, error) {
	if e.loopRunner == nil || e.loopRegistry == nil {
		return nil, fmt.Errorf("delegate execution requires loops wiring; call ConfigureLoopExecution before Execute")
	}
	return e.executeViaLoop(ctx, task, profileName, guidance, tags, opts)
}

// StartBackground launches a detached delegate loop that reports its
// completion back into the current conversation.
func (e *Executor) StartBackground(ctx context.Context, task, profileName, guidance string, tags []string) (string, error) {
	loopID, _, err := e.startBackground(ctx, task, profileName, guidance, tags, defaultExecutionOptions())
	return loopID, err
}

func (e *Executor) startBackground(ctx context.Context, task, profileName, guidance string, tags []string, opts executionOptions) (string, string, error) {
	if e.loopRunner == nil || e.loopRegistry == nil {
		return "", "", fmt.Errorf("background delegation requires loops execution")
	}
	if e.completionSink == nil {
		return "", "", fmt.Errorf("background delegation requires a completion sink")
	}

	prep, err := e.prepareExecution(ctx, task, profileName, guidance, tags, opts)
	if err != nil {
		return "", "", err
	}

	completion, targetConversationID, targetChannel := tools.LoopCompletionTargetFromContext(ctx)
	if completion == looppkg.CompletionConversation && targetConversationID == "" {
		return "", prep.runPolicy.Name, fmt.Errorf("background delegation requires a target conversation")
	}

	loopName := "delegate-" + promptfmt.ShortIDPrefix(prep.id)
	loopMaxDuration := prep.maxDuration + 5*time.Second
	if loopMaxDuration <= 0 {
		loopMaxDuration = prep.maxDuration
	}

	launchResult, err := e.loopRegistry.Launch(ctx, e.buildLoopLaunch(prep, task, guidance, looppkg.OperationBackgroundTask, completion, targetConversationID, targetChannel, loopName, loopMaxDuration, nil), looppkg.Deps{
		Runner:         e.loopRunner,
		Logger:         prep.log,
		EventBus:       e.eventBus,
		CompletionSink: e.completionSink,
	})
	if err != nil {
		e.finishLoopExecution(prep)
		return "", prep.runPolicy.Name, fmt.Errorf("delegate failed to start in background: %w", err)
	}

	prep.log.Info("delegate background started",
		"loop_id", launchResult.LoopID,
		"completion_mode", completion,
		"completion_conversation_id", targetConversationID,
		"completion_channel", looppkg.CloneCompletionChannelTarget(targetChannel),
		"run_timeout", loopMaxDuration.Round(time.Second),
	)

	e.finishDetachedLoopExecution(launchResult.LoopID, prep)
	return launchResult.LoopID, prep.runPolicy.Name, nil
}

type preparedExecution struct {
	id               string
	conversationID   string
	archiveSessionID string
	parentLoopID     string
	channelBinding   *memory.ChannelBinding
	runPolicy        *RunPolicy
	routeHints       map[string]string
	log              *slog.Logger
	userMessage      string
	model            string
	scopeTags        []string
	filterTags       []string
	excludeTools     []string
	tagFilterActive  bool
	effectiveTags    []string
	maxIterations    int
	maxOutputTokens  int
	maxDuration      time.Duration
	toolTimeout      time.Duration
	promptMode       agentctx.PromptMode
}

func (e *Executor) executeViaLoop(ctx context.Context, task, profileName, guidance string, tags []string, opts executionOptions) (*Result, error) {
	prep, err := e.prepareExecution(ctx, task, profileName, guidance, tags, opts)
	if err != nil {
		return nil, err
	}

	loopName := "delegate-" + promptfmt.ShortIDPrefix(prep.id)
	loopMaxDuration := prep.maxDuration + 5*time.Second
	if loopMaxDuration <= 0 {
		loopMaxDuration = prep.maxDuration
	}

	var toolCallsMu sync.Mutex
	var toolCalls []ToolCallOutcome

	runStart := time.Now()
	launchResult, err := e.loopRegistry.Launch(ctx, e.buildLoopLaunch(prep, task, guidance, looppkg.OperationRequestReply, looppkg.CompletionReturn, "", nil, loopName, loopMaxDuration, func(kind string, data map[string]any) {
		if kind != events.KindLoopToolDone {
			return
		}
		name, _ := data["tool"].(string)
		errMsg, _ := data["error"].(string)
		toolCallsMu.Lock()
		toolCalls = append(toolCalls, ToolCallOutcome{
			Name:    name,
			Success: errMsg == "",
		})
		toolCallsMu.Unlock()
	}), looppkg.Deps{
		Runner:   e.loopRunner,
		Logger:   prep.log,
		EventBus: e.eventBus,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			e.finishLoopExecution(prep)
			toolCallsMu.Lock()
			defer toolCallsMu.Unlock()
			return &Result{
				RunPolicyName: prep.runPolicy.Name,
				Content:       "Delegate was unable to complete the task within its time limit.",
				Model:         prep.model,
				Exhausted:     true,
				ExhaustReason: ExhaustWallClock,
				ToolCalls:     append([]ToolCallOutcome(nil), toolCalls...),
				Duration:      time.Since(runStart),
			}, nil
		}
		if errors.Is(err, context.Canceled) {
			shouldFinish := e.stopLoopForCancellation(launchResult.LoopID, prep)
			if shouldFinish {
				e.finishLoopExecution(prep)
			}
			return nil, fmt.Errorf("delegate cancelled: %w", ctx.Err())
		}
		e.finishLoopExecution(prep)
		return nil, fmt.Errorf("delegate failed: %w", err)
	}

	e.finishLoopExecution(prep)
	resp := launchResult.Response
	if resp == nil {
		return nil, emptyResponseError(launchResult)
	}

	toolCallsMu.Lock()
	defer toolCallsMu.Unlock()

	exhausted := resp.Exhausted
	exhaustReason := resp.FinishReason
	if resp.Content == "" && len(toolCalls) > 0 && !exhausted {
		exhausted = true
		exhaustReason = ExhaustNoOutput
	}

	return &Result{
		RunPolicyName:            prep.runPolicy.Name,
		Content:                  resp.Content,
		Model:                    resp.Model,
		Iterations:               resp.Iterations,
		InputTokens:              resp.InputTokens,
		OutputTokens:             resp.OutputTokens,
		CacheCreationInputTokens: resp.CacheCreationInputTokens,
		CacheReadInputTokens:     resp.CacheReadInputTokens,
		Exhausted:                exhausted,
		ExhaustReason:            exhaustReason,
		ToolCalls:                append([]ToolCallOutcome(nil), toolCalls...),
		Duration:                 time.Since(runStart),
	}, nil
}

func (e *Executor) buildLoopLaunch(prep *preparedExecution, task, guidance string, operation looppkg.Operation, completion looppkg.Completion, completionConversationID string, completionChannel *looppkg.CompletionChannelTarget, loopName string, loopMaxDuration time.Duration, onProgress func(kind string, data map[string]any)) looppkg.Launch {
	factors := make(map[string]string, len(prep.routeHints)+1)
	for k, v := range prep.routeHints {
		factors[k] = v
	}
	factors["source"] = "delegate"

	return looppkg.Launch{
		Spec: looppkg.Spec{
			Name:        loopName,
			Operation:   operation,
			Completion:  completion,
			MaxDuration: loopMaxDuration,
			Tags:        append([]string(nil), prep.filterTags...),
			Profile: router.LoopProfile{
				Model:            prep.model,
				DelegationGating: "disabled",
				Instructions:     prompts.DelegateRunInstructions,
			},
			Metadata: map[string]string{
				"category":          "delegate",
				"delegate_id":       prep.id,
				"delegate_task":     truncate(task, 500),
				"delegate_profile":  prep.runPolicy.Name,
				"delegate_guidance": truncate(guidance, 500),
				"prompt_mode":       string(prep.promptMode),
			},
		},
		Task:                     prep.userMessage,
		ParentID:                 prep.parentLoopID,
		ConversationID:           prep.conversationID,
		ChannelBinding:           prep.channelBinding.Clone(),
		RoutingFactors:           factors,
		ExcludeTools:             append([]string(nil), prep.excludeTools...),
		SkipTagFilter:            !prep.tagFilterActive,
		InitialTags:              append([]string(nil), prep.effectiveTags...),
		MaxIterations:            prep.maxIterations,
		MaxOutputTokens:          prep.maxOutputTokens,
		ToolTimeout:              prep.toolTimeout,
		UsageRole:                "delegate",
		UsageTaskName:            prep.runPolicy.Name,
		PromptMode:               prep.promptMode,
		RunTimeout:               prep.maxDuration,
		CompletionConversationID: completionConversationID,
		CompletionChannel:        looppkg.CloneCompletionChannelTarget(completionChannel),
		OnProgress:               onProgress,
		// Task-focused delegates are bounded child tasks. They get
		// tagged providers and KB articles for their declared profile,
		// but not the always-on ambient context (presence, episodic
		// memory, notification history, etc.) that is meant for the
		// main loop's experiential continuity. Full-context delegates
		// opt back into that richer prompt shape intentionally.
		SuppressAlwaysContext: prep.promptMode != agentctx.PromptModeFull,
	}
}

func (e *Executor) effectiveDelegateRouterHints(ctx context.Context, policy *RunPolicy) map[string]string {
	base := policy.RouterHints
	if base == nil {
		base = map[string]string{}
	}
	_, merged := router.OverlayDelegateHints(base, tools.HintsFromContext(ctx))
	return merged
}

func (e *Executor) stopLoopForCancellation(loopID string, prep *preparedExecution) bool {
	if loopID == "" {
		return true
	}
	l := e.loopRegistry.Get(loopID)
	if l == nil {
		return true
	}
	done := l.Done()
	l.Stop()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		if prep != nil {
			prep.log.Warn("delegate loop did not stop before cancellation cleanup; skipping archive and clear to avoid racing active writes",
				"loop_id", loopID,
			)
		}
		return false
	}
}

func (e *Executor) finishDetachedLoopExecution(loopID string, prep *preparedExecution) {
	if prep == nil {
		return
	}
	if loopID == "" || e.loopRegistry == nil {
		e.finishLoopExecution(prep)
		return
	}

	l := e.loopRegistry.Get(loopID)
	if l == nil {
		e.finishLoopExecution(prep)
		return
	}

	go func(done <-chan struct{}) {
		if done != nil {
			<-done
		}
		e.finishLoopExecution(prep)
	}(l.Done())
}

func (e *Executor) finishLoopExecution(prep *preparedExecution) {
	if prep == nil {
		return
	}
	if e.sessionArchiver != nil && e.conversations != nil {
		msgs := e.conversations.GetMessages(prep.conversationID)
		if err := e.sessionArchiver.ArchiveConversation(prep.conversationID, msgs, "delegate"); err != nil {
			prep.log.Warn("failed to archive delegate conversation", "error", err)
		}
		sessionID := e.sessionArchiver.ActiveSessionID(prep.conversationID)
		if sessionID == "" {
			sessionID = prep.archiveSessionID
		}
		if sessionID != "" {
			if err := e.sessionArchiver.EndSession(sessionID, "delegate"); err != nil {
				prep.log.Warn("failed to end delegate session", "error", err)
			}
		}
		if err := e.conversations.Clear(prep.conversationID); err != nil {
			prep.log.Warn("failed to clear delegate conversation", "error", err)
		}
		return
	}
	if e.archiver != nil && prep.archiveSessionID != "" {
		if err := e.archiver.EndSession(prep.archiveSessionID, "delegate"); err != nil {
			prep.log.Warn("failed to end delegate archive session", "error", err)
		}
	}
}

func (e *Executor) prepareExecution(ctx context.Context, task, profileName, guidance string, tags []string, opts executionOptions) (*preparedExecution, error) {
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	delegateID, _ := uuid.NewV7()
	did := delegateID.String()
	conversationID := "delegate-" + promptfmt.ShortIDPrefix(did)

	log := logging.Logger(ctx).With(
		"subsystem", logging.SubsystemDelegate,
		"delegate_id", did,
	)
	ctx = logging.WithLogger(ctx, log)

	var archiveSessionID string
	if e.archiver != nil {
		parentSessionID := tools.SessionIDFromContext(ctx)
		parentToolCallID := tools.ToolCallIDFromContext(ctx)

		var opts []memory.SessionOption
		if parentSessionID != "" {
			opts = append(opts, memory.WithParentSession(parentSessionID))
		}
		if parentToolCallID != "" {
			opts = append(opts, memory.WithParentToolCall(parentToolCallID))
		}
		if binding := tools.ChannelBindingFromContext(ctx); binding != nil {
			opts = append(opts, memory.WithChannelBinding(binding))
		}

		sess, err := e.archiver.StartSessionWithOptions(conversationID, opts...)
		if err != nil {
			log.Warn("failed to create archive session for delegate", "error", err)
		} else {
			archiveSessionID = sess.ID
		}
	}

	scopeTags, inheritedTags, droppedTags, explicitScopeRequested := mergeDelegateScopeTags(ctx, tags, opts.inheritCallerTags, opts.explicitTagScope)
	if len(droppedTags) > 0 {
		log.Warn("delegate capability tags skipped", "dropped_tags", droppedTags)
	}
	policy := e.runPolicyForScope(profileName, scopeTags)
	scopeTags, policyDefaultTags := applyRunPolicyDefaultTags(scopeTags, policy, explicitScopeRequested)

	reg := e.delegateToolRegistry(scopeTags, explicitScopeRequested)
	toolDefs := reg.List()
	filterTags := mergeTagLists(scopeTags, e.coreTags)
	// Delegate tool exclusions must hold on every code path. The
	// loop-backed launch rebuilds the catalog from the parent registry
	// filtered by the launched loop's request, so the exclusions have to
	// be expressed at request level to survive that rebuild — the
	// in-process delegateToolRegistry filter alone is not enough.
	//
	// The explicit-empty-scope branch (AllToolNames) already includes the
	// excluded names, so dedup before sorting to avoid duplicate entries
	// when both sources contribute.
	var excludeTools []string
	if explicitScopeRequested && len(filterTags) == 0 {
		excludeTools = e.parentReg.AllToolNames()
	}
	excludeTools = mergeExcludeToolNames(excludeTools, delegateToolExclusions())
	tagFilterActive := len(scopeTags) > 0 || explicitScopeRequested

	log = log.With("profile", policy.Name)
	ctx = logging.WithLogger(ctx, log)
	log.Info("delegate started",
		"task", truncate(task, 200),
		"guidance", truncate(guidance, 200),
		"tags", scopeTags,
		"inherited_tags", inheritedTags,
		"profile_default_tags", policyDefaultTags,
		"tools_available", len(toolDefs),
	)

	effectiveTagsMap := make(map[string]bool, len(scopeTags)+len(e.coreTags))
	for _, t := range scopeTags {
		effectiveTagsMap[t] = true
	}
	for _, t := range e.coreTags {
		effectiveTagsMap[t] = true
	}
	if e.lensProvider != nil {
		for _, lens := range e.lensProvider() {
			effectiveTagsMap[lens] = true
		}
	}
	effectiveTags := make([]string, 0, len(effectiveTagsMap))
	for tag := range effectiveTagsMap {
		effectiveTags = append(effectiveTags, tag)
	}
	sort.Strings(effectiveTags)

	if e.tempFiles != nil {
		parentConvID := tools.ConversationIDFromContext(ctx)
		task = e.tempFiles.ExpandLabels(parentConvID, task)
		if guidance != "" {
			guidance = e.tempFiles.ExpandLabels(parentConvID, guidance)
		}
	}

	var userMsg strings.Builder
	userMsg.WriteString(task)
	if guidance != "" {
		userMsg.WriteString("\n\nGuidance: ")
		userMsg.WriteString(guidance)
	}

	maxIterations := policy.MaxIter
	if maxIterations <= 0 {
		maxIterations = defaultMaxIter
	}
	maxOutputTokens := policy.MaxTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = defaultMaxTokens
	}
	maxDuration := policy.MaxDuration
	if maxDuration <= 0 {
		maxDuration = defaultMaxDuration
	}
	toolTimeout := policy.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = defaultToolTimeout
	}

	return &preparedExecution{
		id:               did,
		conversationID:   conversationID,
		archiveSessionID: archiveSessionID,
		parentLoopID:     tools.LoopIDFromContext(ctx),
		channelBinding:   tools.ChannelBindingFromContext(ctx),
		runPolicy:        policy,
		routeHints:       e.effectiveDelegateRouterHints(ctx, policy),
		log:              log,
		userMessage:      userMsg.String(),
		model:            e.selectModel(ctx, task, policy, len(toolDefs)),
		scopeTags:        append([]string(nil), scopeTags...),
		filterTags:       filterTags,
		excludeTools:     excludeTools,
		tagFilterActive:  tagFilterActive,
		effectiveTags:    effectiveTags,
		maxIterations:    maxIterations,
		maxOutputTokens:  maxOutputTokens,
		maxDuration:      maxDuration,
		toolTimeout:      toolTimeout,
		promptMode:       opts.effectivePromptMode(),
	}, nil
}

// runPolicyForScope selects the appropriate run policy for a delegate
// invocation. profileName is the operator-facing lookup key from the
// caller; scopeTags drive automatic ha-profile inference when no
// explicit policy is named.
func (e *Executor) runPolicyForScope(profileName string, scopeTags []string) *RunPolicy {
	if profileName != "" && profileName != "general" {
		if policy := e.runPolicies[profileName]; policy != nil {
			return policy
		}
	}
	if hasAnyTag(scopeTags, "ha") {
		if policy := e.runPolicies["ha"]; policy != nil {
			return policy
		}
	}
	if policy := e.runPolicies[profileName]; policy != nil {
		return policy
	}
	return e.runPolicies["general"]
}

func hasAnyTag(tags []string, wants ...string) bool {
	for _, tag := range tags {
		for _, want := range wants {
			if tag == want {
				return true
			}
		}
	}
	return false
}

// selectModel picks a model for the delegate via the router or falls back to the default.
func (e *Executor) selectModel(ctx context.Context, task string, policy *RunPolicy, toolCount int) string {
	log := logging.Logger(ctx)
	needsStreaming := e.loopRunner != nil && e.loopRegistry != nil
	if explicitModel, _ := router.OverlayDelegateHints(nil, tools.HintsFromContext(ctx)); explicitModel != "" {
		log.Debug("delegate model selected by inherited policy",
			"model", explicitModel,
		)
		return explicitModel
	}
	hints := e.effectiveDelegateRouterHints(ctx, policy)
	if e.router != nil {
		model, _ := e.router.Route(ctx, router.Request{
			Query:          task,
			NeedsTools:     toolCount > 0,
			NeedsStreaming: needsStreaming,
			ToolCount:      toolCount,
			Priority:       router.PriorityBackground,
			RoutingFactors: hints,
		})
		if model != "" {
			log.Debug("delegate model selected by router",
				"model", model,
			)
			return model
		}
		if fallback := e.router.DefaultModel(); fallback != "" {
			log.Debug("delegate using router default model",
				"model", fallback,
			)
			return fallback
		}
	}
	log.Debug("delegate using default model",
		"model", e.defaultModel,
	)
	return e.defaultModel
}

// formatTokens formats a token count as a human-readable string (e.g., "1.2K").
func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fK", math.Round(float64(n)/100)/10)
}

// truncate shortens a string to maxLen bytes without splitting a UTF-8
// sequence, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	for maxLen > 0 && !utf8.RuneStart(s[maxLen]) {
		maxLen--
	}
	return s[:maxLen] + "..."
}

// childLastErrorMaxLen caps the size of a child loop's LastError
// string when it's surfaced through emptyResponseError. The child
// stores LastError as a raw err.Error() with no truncation, so a
// runaway error (a full stack trace, an LLM body dump) could
// otherwise inflate the parent's tool result and burn parent tokens.
// 1024 is comfortably enough for the error shapes we've actually seen
// in production (tool-call parse failures, API-5xx bodies) while
// keeping a hard ceiling on pathological cases.
const childLastErrorMaxLen = 1024

// emptyResponseError builds the error returned to the parent when a
// joined launch finishes without producing a Response. The naive form
// — "joined launch completed without response" — is opaque: it doesn't
// distinguish a transient timeout from a deterministic LLM-API
// failure, so a parent retrying blind burns tokens for no diagnostic
// gain. When the child loop captured a terminal error in its final
// status (e.g. tool-call parse failures from local models, LLM-API
// 5xx, exhausted retries), surface that string (truncated to
// childLastErrorMaxLen) so the parent can decide whether to retry,
// change strategy, or escalate.
func emptyResponseError(launchResult looppkg.LaunchResult) error {
	if launchResult.FinalStatus != nil && launchResult.FinalStatus.LastError != "" {
		return fmt.Errorf("delegate failed: %s", truncate(launchResult.FinalStatus.LastError, childLastErrorMaxLen))
	}
	return fmt.Errorf("delegate failed: joined launch completed without response")
}

// mergeExcludeToolNames combines two tool-name slices into a sorted,
// deduplicated slice. Used by the delegate launch path so the
// AllToolNames-based wholesale exclusion (from the explicit-empty-
// scope branch) and the always-applied delegateFamilyToolNames guard
// don't produce duplicate entries when both contribute.
func mergeExcludeToolNames(groups ...[]string) []string {
	if len(groups) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, name := range group {
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	merged := make([]string, 0, len(seen))
	for name := range seen {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged
}
