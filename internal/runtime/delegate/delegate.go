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
// exclusion is meant to prevent. The family currently includes the
// deprecated thane_delegate alias plus its replacements thane_now
// (sync) and thane_assign (async). When adding a new family member,
// add its name here in the same change.
var delegateFamilyToolNames = []string{
	"thane_delegate",
	"thane_now",
	"thane_assign",
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

func applyProfileDefaultTags(scopeTags []string, profile *Profile, explicitScopeRequested bool) (mergedTags []string, appliedDefaults []string) {
	mergedTags = append([]string(nil), scopeTags...)
	if explicitScopeRequested || profile == nil || len(profile.DefaultTags) == 0 {
		return mergedTags, nil
	}

	seen := make(map[string]bool, len(mergedTags)+len(profile.DefaultTags))
	for _, tag := range mergedTags {
		seen[tag] = true
	}
	for _, tag := range profile.DefaultTags {
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

func (e *Executor) delegateToolRegistry(scopeTags []string, explicitScopeRequested bool) *tools.Registry {
	if len(scopeTags) > 0 || explicitScopeRequested {
		merged := mergeTagLists(scopeTags, e.alwaysActiveTags)
		var reg *tools.Registry
		if len(merged) > 0 {
			reg = e.parentReg.FilterByTags(merged)
		} else {
			reg = e.parentReg.FilteredCopy(nil)
		}
		return reg.FilteredCopyExcluding(delegateFamilyToolNames)
	}
	return e.parentReg.FilteredCopyExcluding(delegateFamilyToolNames)
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
	logger           *slog.Logger
	llm              llm.Client
	router           *router.Router
	parentReg        *tools.Registry
	profiles         map[string]*Profile
	timezone         string
	defaultModel     string
	archiver         *memory.ArchiveStore
	tempFiles        labelExpander
	alwaysActiveTags []string
	lensProvider     func() []string // returns active global lenses (nil = none)
	eventBus         *events.Bus
	loopRunner       looppkg.Runner
	loopRegistry     *looppkg.Registry
	completionSink   looppkg.CompletionSink
	sessionArchiver  agent.SessionArchiver
	conversations    *memory.SQLiteStore
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
		profiles:     builtinProfiles(),
		defaultModel: defaultModel,
	}
}

// ApplyProfileOverrides applies configuration overrides to builtin
// profiles. Only positive fields in each override replace the builtin
// defaults. Negative values are logged as warnings and ignored.
// Unknown profile names are silently ignored (config may reference
// profiles that don't exist yet).
func (e *Executor) ApplyProfileOverrides(overrides map[string]ProfileOverride) {
	for name, o := range overrides {
		p, ok := e.profiles[name]
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

// ProfileOverride holds optional overrides for a delegate profile.
// Only positive values are applied; zero and negative fields are ignored.
type ProfileOverride struct {
	ToolTimeout time.Duration
	MaxDuration time.Duration
	MaxIter     int
	MaxTokens   int
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

// SetAlwaysActiveTags configures the capability tags that are
// automatically included in every tag-scoped delegation, regardless
// of which tags the caller requests. This mirrors the agent loop's
// always_active tag behavior.
func (e *Executor) SetAlwaysActiveTags(tags []string) {
	e.alwaysActiveTags = tags
}

// SetLensProvider configures a function that returns the currently
// active global lenses. These are merged into every delegate execution's
// effective tag set so lens-tagged KB articles and talents apply.
func (e *Executor) SetLensProvider(fn func() []string) {
	e.lensProvider = fn
}

// ConfigureLoopExecution configures loop-backed delegate execution. When both
// runner and registry are set, Execute launches a one-shot child loop through
// the shared loops-ng path, giving delegates the same telemetry path as other
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

// ProfileNames returns the names of all registered profiles.
func (e *Executor) ProfileNames() []string {
	names := make([]string, 0, len(e.profiles))
	for name := range e.profiles {
		names = append(names, name)
	}
	return names
}

// Execute runs a delegated task with the given profile and guidance.
// Capability tags define the delegate's tool and context scope. Elective
// caller tags are inherited by default; compatibility profiles may add
// default tags only when the caller did not request an explicit scope.
func (e *Executor) Execute(ctx context.Context, task, profileName, guidance string, tags []string) (*Result, error) {
	return e.execute(ctx, task, profileName, guidance, tags, defaultExecutionOptions())
}

func (e *Executor) execute(ctx context.Context, task, profileName, guidance string, tags []string, opts executionOptions) (*Result, error) {
	if e.loopRunner == nil || e.loopRegistry == nil {
		return nil, fmt.Errorf("delegate execution requires loops-ng wiring; call ConfigureLoopExecution before Execute")
	}
	return e.executeViaLoop(ctx, task, profileName, guidance, tags, opts)
}

// StartBackground launches a detached delegate loop that reports its
// completion back into the current conversation.
func (e *Executor) StartBackground(ctx context.Context, task, profileName, guidance string, tags []string) (string, error) {
	return e.startBackground(ctx, task, profileName, guidance, tags, defaultExecutionOptions())
}

func (e *Executor) startBackground(ctx context.Context, task, profileName, guidance string, tags []string, opts executionOptions) (string, error) {
	if e.loopRunner == nil || e.loopRegistry == nil {
		return "", fmt.Errorf("background delegation requires loops-ng execution")
	}
	if e.completionSink == nil {
		return "", fmt.Errorf("background delegation requires a completion sink")
	}

	prep, err := e.prepareExecution(ctx, task, profileName, guidance, tags, opts)
	if err != nil {
		return "", err
	}

	completion, targetConversationID, targetChannel := tools.LoopCompletionTargetFromContext(ctx)
	if completion == looppkg.CompletionConversation && targetConversationID == "" {
		return "", fmt.Errorf("background delegation requires a target conversation")
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
		return "", fmt.Errorf("delegate failed to start in background: %w", err)
	}

	prep.log.Info("delegate background started",
		"loop_id", launchResult.LoopID,
		"completion_mode", completion,
		"completion_conversation_id", targetConversationID,
		"completion_channel", looppkg.CloneCompletionChannelTarget(targetChannel),
		"run_timeout", loopMaxDuration.Round(time.Second),
	)

	e.finishDetachedLoopExecution(launchResult.LoopID, prep)
	return launchResult.LoopID, nil
}

type preparedExecution struct {
	id               string
	conversationID   string
	archiveSessionID string
	parentLoopID     string
	channelBinding   *memory.ChannelBinding
	profile          *Profile
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
		return nil, fmt.Errorf("delegate failed: joined launch completed without response")
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
	hints := make(map[string]string, len(prep.routeHints)+2)
	for k, v := range prep.routeHints {
		hints[k] = v
	}
	hints["source"] = "delegate"
	hints[router.HintDelegationGating] = "disabled"

	return looppkg.Launch{
		Spec: looppkg.Spec{
			Name:        loopName,
			Operation:   operation,
			Completion:  completion,
			MaxDuration: loopMaxDuration,
			Tags:        append([]string(nil), prep.filterTags...),
			Profile: router.LoopProfile{
				Instructions: prompts.DelegateRunInstructions,
			},
			Metadata: map[string]string{
				"category":          "delegate",
				"delegate_id":       prep.id,
				"delegate_task":     truncate(task, 500),
				"delegate_profile":  prep.profile.Name,
				"delegate_guidance": truncate(guidance, 500),
				"prompt_mode":       string(prep.promptMode),
			},
		},
		Task:                     prep.userMessage,
		ParentID:                 prep.parentLoopID,
		ConversationID:           prep.conversationID,
		ChannelBinding:           prep.channelBinding.Clone(),
		Model:                    prep.model,
		Hints:                    hints,
		ExcludeTools:             append([]string(nil), prep.excludeTools...),
		SkipTagFilter:            !prep.tagFilterActive,
		InitialTags:              append([]string(nil), prep.effectiveTags...),
		MaxIterations:            prep.maxIterations,
		MaxOutputTokens:          prep.maxOutputTokens,
		ToolTimeout:              prep.toolTimeout,
		UsageRole:                "delegate",
		UsageTaskName:            prep.profile.Name,
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

func (e *Executor) effectiveDelegateRouterHints(ctx context.Context, profile *Profile) map[string]string {
	base := profile.RouterHints
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

	profile := e.profiles[profileName]
	if profile == nil {
		profile = e.profiles["general"]
	}
	scopeTags, inheritedTags, droppedTags, explicitScopeRequested := mergeDelegateScopeTags(ctx, tags, opts.inheritCallerTags, opts.explicitTagScope)
	if len(droppedTags) > 0 {
		log.Warn("delegate capability tags skipped", "dropped_tags", droppedTags)
	}
	scopeTags, profileDefaultTags := applyProfileDefaultTags(scopeTags, profile, explicitScopeRequested)

	reg := e.delegateToolRegistry(scopeTags, explicitScopeRequested)
	toolDefs := reg.List()
	filterTags := mergeTagLists(scopeTags, e.alwaysActiveTags)
	var excludeTools []string
	if explicitScopeRequested && len(filterTags) == 0 {
		excludeTools = e.parentReg.AllToolNames()
		sort.Strings(excludeTools)
	}
	tagFilterActive := len(scopeTags) > 0 || explicitScopeRequested

	log = log.With("profile", profile.Name)
	ctx = logging.WithLogger(ctx, log)
	log.Info("delegate started",
		"task", truncate(task, 200),
		"guidance", truncate(guidance, 200),
		"tags", scopeTags,
		"inherited_tags", inheritedTags,
		"profile_default_tags", profileDefaultTags,
		"tools_available", len(toolDefs),
	)

	effectiveTagsMap := make(map[string]bool, len(scopeTags)+len(e.alwaysActiveTags))
	for _, t := range scopeTags {
		effectiveTagsMap[t] = true
	}
	for _, t := range e.alwaysActiveTags {
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

	maxIterations := profile.MaxIter
	if maxIterations <= 0 {
		maxIterations = defaultMaxIter
	}
	maxOutputTokens := profile.MaxTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = defaultMaxTokens
	}
	maxDuration := profile.MaxDuration
	if maxDuration <= 0 {
		maxDuration = defaultMaxDuration
	}
	toolTimeout := profile.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = defaultToolTimeout
	}

	return &preparedExecution{
		id:               did,
		conversationID:   conversationID,
		archiveSessionID: archiveSessionID,
		parentLoopID:     tools.LoopIDFromContext(ctx),
		channelBinding:   tools.ChannelBindingFromContext(ctx),
		profile:          profile,
		routeHints:       e.effectiveDelegateRouterHints(ctx, profile),
		log:              log,
		userMessage:      userMsg.String(),
		model:            e.selectModel(ctx, task, profile, len(toolDefs)),
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

// selectModel picks a model for the delegate via the router or falls back to the default.
func (e *Executor) selectModel(ctx context.Context, task string, profile *Profile, toolCount int) string {
	log := logging.Logger(ctx)
	needsStreaming := e.loopRunner != nil && e.loopRegistry != nil
	if explicitModel, _ := router.OverlayDelegateHints(nil, tools.HintsFromContext(ctx)); explicitModel != "" {
		log.Debug("delegate model selected by inherited policy",
			"model", explicitModel,
		)
		return explicitModel
	}
	hints := e.effectiveDelegateRouterHints(ctx, profile)
	if e.router != nil {
		model, _ := e.router.Route(ctx, router.Request{
			Query:          task,
			NeedsTools:     toolCount > 0,
			NeedsStreaming: needsStreaming,
			ToolCount:      toolCount,
			Priority:       router.PriorityBackground,
			Hints:          hints,
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

// truncate shortens a string to maxLen characters, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
