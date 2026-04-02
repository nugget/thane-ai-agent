package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/awareness"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/iterate"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/usage"
)

// delegateToolName is the tool name excluded from delegate registries to prevent recursion.
const delegateToolName = "thane_delegate"

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

// ToolCallOutcome records the name and success/failure of a single tool
// invocation during delegate execution.
type ToolCallOutcome struct {
	Name    string `json:"name"`
	Success bool   `json:"success"`
}

// Result is the outcome of a delegated task execution.
type Result struct {
	Content       string            `json:"content"`
	Model         string            `json:"model"`
	Iterations    int               `json:"iterations"`
	InputTokens   int               `json:"input_tokens"`
	OutputTokens  int               `json:"output_tokens"`
	Exhausted     bool              `json:"exhausted"`
	ExhaustReason string            `json:"exhaust_reason,omitempty"`
	ToolCalls     []ToolCallOutcome `json:"tool_calls,omitempty"`
	Duration      time.Duration     `json:"duration"`
}

// labelExpander expands temp file labels in task descriptions. Defined
// as an interface to avoid a circular import between delegate and tools.
type labelExpander interface {
	ExpandLabels(convID, text string) string
}

// tagContextFunc builds capability context for a set of active tags.
// The function should snapshot any mutable state (e.g., live providers)
// at call time. Wired in main.go as a closure over the loop and assembler.
type tagContextFunc func(ctx context.Context, activeTags map[string]bool) string

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
	usageStore       *usage.Store
	pricing          map[string]config.PricingEntry
	alwaysActiveTags []string
	lensProvider     func() []string // returns active global lenses (nil = none)
	forgeContext     string
	tagCtxFunc       tagContextFunc // nil-safe — replaces forgeContext when set
	eventBus         *events.Bus
	contentWriter    *logging.ContentWriter
	loopRunner       *agent.Loop
	loopRegistry     *looppkg.Registry
	sessionArchiver  agent.SessionArchiver
	conversations    *memory.SQLiteStore
}

// NewExecutor creates a delegate executor.
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

// SetTimezone configures the IANA timezone for Current Conditions
// in the delegate system prompt.
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

// SetUsageRecorder configures persistent token usage recording for
// delegate executions. When set, every delegate completion is persisted
// for cost attribution.
func (e *Executor) SetUsageRecorder(store *usage.Store, pricing map[string]config.PricingEntry) {
	e.usageStore = store
	e.pricing = pricing
}

// SetEventBus configures the event bus for delegate lifecycle events.
// When set, each Execute call publishes spawn and complete events so
// the dashboard can render delegates as ephemeral child nodes.
func (e *Executor) SetEventBus(bus *events.Bus) {
	e.eventBus = bus
}

// SetForgeContext configures the forge account context block that is
// appended to delegate system prompts. This gives delegates immediate
// knowledge of configured forge accounts so they don't waste iterations
// guessing account names.
func (e *Executor) SetForgeContext(ctx string) {
	e.forgeContext = ctx
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

// SetTagContextFunc configures the tag context builder function for
// injecting capability context (static files, tagged KB articles,
// and live providers) into delegate system prompts. When set, this
// replaces the ad-hoc forge context injection.
func (e *Executor) SetTagContextFunc(fn tagContextFunc) {
	e.tagCtxFunc = fn
}

// SetContentWriter configures content retention for delegate executions.
func (e *Executor) SetContentWriter(w *logging.ContentWriter) {
	e.contentWriter = w
}

// ConfigureLoopExecution configures real loop-backed delegate execution. When
// both runner and registry are set, Execute spawns a one-shot child loop whose
// handler calls the core agent runner, giving delegates the same telemetry path
// as other loop-driven work.
func (e *Executor) ConfigureLoopExecution(runner *agent.Loop, registry *looppkg.Registry) {
	e.loopRunner = runner
	e.loopRegistry = registry
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
// When tags is non-empty, the delegate's tool registry is scoped to
// only tools belonging to the given capability tags (plus any
// always-active tags). When tags is nil, the profile's AllowedTools
// controls tool selection (existing behavior).
func (e *Executor) Execute(ctx context.Context, task, profileName, guidance string, tags []string, pathPrefixes map[string]string) (*Result, error) {
	if e.loopRunner != nil && e.loopRegistry != nil {
		return e.executeViaLoop(ctx, task, profileName, guidance, tags, pathPrefixes)
	}
	return e.executeLegacy(ctx, task, profileName, guidance, tags, pathPrefixes)
}

func (e *Executor) executeLegacy(ctx context.Context, task, profileName, guidance string, tags []string, pathPrefixes map[string]string) (delegateResult *Result, delegateErr error) {
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	// Generate a unique delegate ID for log correlation.
	delegateID, _ := uuid.NewV7()
	did := delegateID.String()

	// Create an archive session for this delegate execution so it
	// appears in the session inspector alongside user conversations.
	convID := "delegate-" + did[:8]

	// Context logger inherits upstream fields (request_id, session,
	// conversation from the agent loop) and adds delegate-specific trace
	// fields so all downstream code (tool implementations) gets the
	// full trace chain.
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

		sess, err := e.archiver.StartSessionWithOptions(convID, opts...)
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

	// Build filtered tool registry. Tag-scoped delegations take
	// precedence over the profile's AllowedTools list.
	var reg *tools.Registry
	if len(tags) > 0 {
		merged := append(tags, e.alwaysActiveTags...)
		reg = e.parentReg.FilterByTags(merged)
		reg = reg.FilteredCopyExcluding([]string{delegateToolName})
	} else if len(profile.AllowedTools) > 0 {
		reg = e.parentReg.FilteredCopy(profile.AllowedTools)
	} else {
		reg = e.parentReg.FilteredCopyExcluding([]string{delegateToolName})
	}

	toolDefs := reg.List()
	toolNames := reg.AllToolNames()
	sort.Strings(toolNames)

	log = log.With("profile", profile.Name)
	ctx = logging.WithLogger(ctx, log)

	log.Info("delegate started",
		"task", truncate(task, 200),
		"guidance", truncate(guidance, 200),
		"tags", tags,
		"tools_available", len(toolDefs),
	)

	// Publish spawn event so the dashboard can render an ephemeral child node.
	parentLoopID := tools.LoopIDFromContext(ctx)
	e.eventBus.Publish(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceDelegate,
		Kind:      events.KindSpawn,
		Data: map[string]any{
			"delegate_id":    did,
			"parent_loop_id": parentLoopID,
			"profile":        profile.Name,
			"task":           truncate(task, 200),
			"guidance":       truncate(guidance, 200),
			"tags":           tags,
			"name":           "delegate-" + did[:8],
		},
	})

	// Build system prompt.
	var sb strings.Builder
	sb.WriteString(profile.SystemPrompt)
	sb.WriteString("\n\n")
	sb.WriteString(awareness.CurrentConditions(e.timezone))

	// Inject tag context (static files, KB articles, live providers).
	// Falls back to the legacy forge-only injection when the tag
	// context function is not configured.
	// Build effective tag set: explicit tags + always-active + global lenses.
	merged := make(map[string]bool, len(tags)+len(e.alwaysActiveTags))
	for _, t := range tags {
		merged[t] = true
	}
	for _, t := range e.alwaysActiveTags {
		merged[t] = true
	}
	if e.lensProvider != nil {
		for _, lens := range e.lensProvider() {
			merged[lens] = true
		}
	}
	if e.tagCtxFunc != nil && len(merged) > 0 {
		if tagCtx := e.tagCtxFunc(ctx, merged); tagCtx != "" {
			sb.WriteString("\n\n## Capability Context\n\n")
			sb.WriteString(tagCtx)
		}
	} else if e.forgeContext != "" {
		sb.WriteString("\n\n")
		sb.WriteString(e.forgeContext)
	}

	// Inject path prefix documentation so the delegate knows the shortcuts.
	if prefixPrompt := formatPrefixPrompt(pathPrefixes, time.Now()); prefixPrompt != "" {
		sb.WriteString("\n\n")
		sb.WriteString(prefixPrompt)
	}

	// Expand temp file labels so the delegate sees real paths.
	if e.tempFiles != nil {
		convID := tools.ConversationIDFromContext(ctx)
		task = e.tempFiles.ExpandLabels(convID, task)
		if guidance != "" {
			guidance = e.tempFiles.ExpandLabels(convID, guidance)
		}
	}

	// Build user message.
	var userMsg strings.Builder
	userMsg.WriteString(task)
	if guidance != "" {
		userMsg.WriteString("\n\nGuidance: ")
		userMsg.WriteString(guidance)
	}

	messages := []llm.Message{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: userMsg.String()},
	}

	// Select model via router.
	model := e.selectModel(ctx, task, profile, len(toolDefs))

	startTime := time.Now()
	var totalInput, totalOutput int
	var toolCalls []ToolCallOutcome

	// Publish complete event on all exit paths so the dashboard removes
	// the ephemeral node. The defer captures the named return delegateResult.
	defer func() {
		data := map[string]any{
			"delegate_id":    did,
			"parent_loop_id": parentLoopID,
			"duration_ms":    time.Since(startTime).Milliseconds(),
		}
		if delegateResult != nil {
			data["iterations"] = delegateResult.Iterations
			data["exhausted"] = delegateResult.Exhausted
			data["exhaust_reason"] = delegateResult.ExhaustReason
		}
		if delegateErr != nil {
			data["error"] = delegateErr.Error()
		}
		e.eventBus.Publish(events.Event{
			Timestamp: time.Now(),
			Source:    events.SourceDelegate,
			Kind:      events.KindComplete,
			Data:      data,
		})
	}()

	maxIter := profile.MaxIter
	if maxIter <= 0 {
		maxIter = defaultMaxIter
	}
	maxTokens := profile.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	maxDuration := profile.MaxDuration
	if maxDuration <= 0 {
		maxDuration = defaultMaxDuration
	}

	// Enforce wall clock as a context deadline so in-flight HTTP calls
	// are cancelled when time expires. Without this, a blocking
	// ChatStream call bypasses the manual wall clock checks (which only
	// run at iteration boundaries). See issue #219.
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	// Safety net: log if Execute returns without recording completion.
	// With the context deadline fix this should rarely fire, but it
	// guards against unforeseen code paths. Context cancellation
	// (including timeouts) is an expected exit and should not log.
	var completed bool
	defer func() {
		if !completed && ctx.Err() == nil {
			log.Error("delegate terminated without completion record",
				"elapsed", time.Since(startTime).Round(time.Second),
			)
		}
	}()

	// Build the iterate.Config, delegating tool execution to
	// DeadlineExecutor with per-tool timeouts.
	toolTimeout := profile.ToolTimeout
	if toolTimeout == 0 {
		toolTimeout = defaultToolTimeout
	}

	// toolCancelFuncs holds per-tool cancel functions created by
	// OnBeforeToolExec, cancelled after each tool completes.
	var currentToolCancel context.CancelFunc

	// iterCount tracks completed iterations so the wall-clock deadline
	// path can record accurate counts even when engine.Run returns an error.
	var iterCount int

	iterCfg := iterate.Config{
		MaxIterations: maxIter,
		Model:         model,
		LLM:           e.llm,
		ToolDefs:      func(int) []map[string]any { return toolDefs },
		Executor: &iterate.DeadlineExecutor{
			Exec: func(execCtx context.Context, name, argsJSON string) (string, error) {
				// Expand caller-defined path prefixes in file tool arguments.
				if len(pathPrefixes) > 0 && fileTools[name] {
					argsJSON = expandPathPrefixes(name, argsJSON, pathPrefixes)
				}
				return reg.Execute(execCtx, name, argsJSON)
			},
		},
		// Accumulate token counts as each LLM call completes so they
		// remain accurate even if engine.Run returns an error (e.g.
		// wall-clock deadline fires mid-run).
		OnLLMResponse: func(_ context.Context, resp *llm.ChatResponse, _ int) {
			totalInput += resp.InputTokens
			totalOutput += resp.OutputTokens
			iterCount++
		},
		OnBeforeToolExec: func(execCtx context.Context, _ int, _ llm.ToolCall) context.Context {
			toolCtx := tools.WithConversationID(execCtx, convID)
			toolCtx, currentToolCancel = context.WithTimeout(toolCtx, toolTimeout)
			return toolCtx
		},
		OnToolCallDone: func(_ context.Context, name, _, errMsg string) {
			if currentToolCancel != nil {
				currentToolCancel()
				currentToolCancel = nil
			}
			toolCalls = append(toolCalls, ToolCallOutcome{
				Name:    name,
				Success: errMsg == "",
			})
		},
		CheckBudget:    func(totalOut int) bool { return totalOut >= maxTokens },
		CheckToolAvail: func(name string) bool { return reg.Get(name) != nil },
		OnLLMError: func(_ context.Context, err error, m string,
			_ []llm.Message, _ []map[string]any,
			_ llm.StreamCallback) (*llm.ChatResponse, string, error) {
			// Translate context deadline into wall-clock exhaustion so
			// the engine returns an exhaustion result.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, "", err
			}
			return nil, "", err
		},
	}

	engine := &iterate.Engine{}
	iterResult, err := engine.Run(ctx, iterCfg, messages)

	// Handle context errors that propagated through the engine.
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Wall clock exhaustion — return result, not error.
			// Engine.Run returns a partial Result alongside the error;
			// use its Messages if available for a complete execution trace.
			archiveMsgs := messages
			if iterResult != nil && len(iterResult.Messages) > 0 {
				archiveMsgs = iterResult.Messages
			}
			completed = true
			var partialIterations []iterate.IterationRecord
			if iterResult != nil {
				partialIterations = iterResult.Iterations
				// ctx is past its deadline — use a fresh context so content
				// retention writes don't fail immediately. Build RequestContent
				// directly so we can override the exhaustion metadata: the
				// partial iterate.Result comes from the engine's error path
				// and lacks an ExhaustReason, but the delegate knows it's
				// ExhaustWallClock.
				if e.contentWriter != nil {
					go func() {
						retainCtx, retainCancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer retainCancel()
						e.contentWriter.WriteRequest(retainCtx, logging.RequestContent{
							RequestID:        did,
							SystemPrompt:     messages[0].Content,
							UserContent:      userMsg.String(),
							Model:            iterResult.Model,
							AssistantContent: iterResult.Content,
							IterationCount:   iterResult.IterationCount,
							InputTokens:      iterResult.InputTokens,
							OutputTokens:     iterResult.OutputTokens,
							ToolsUsed:        iterResult.ToolsUsed,
							Exhausted:        true,
							ExhaustReason:    ExhaustWallClock,
							Messages:         iterResult.Messages,
						})
					}()
				}
			}
			e.recordCompletion(&completionRecord{
				log:              log,
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        iterCount,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        true,
				exhaustReason:    ExhaustWallClock,
				startTime:        startTime,
				messages:         archiveMsgs,
				resultContent:    "Delegate was unable to complete the task within its time limit.",
				errMsg:           err.Error(),
				toolCalls:        toolCalls,
				iterations:       partialIterations,
			})
			return &Result{
				Content:       "Delegate was unable to complete the task within its time limit.",
				Model:         model,
				InputTokens:   totalInput,
				OutputTokens:  totalOutput,
				Exhausted:     true,
				ExhaustReason: ExhaustWallClock,
				ToolCalls:     toolCalls,
				Duration:      time.Since(startTime),
			}, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			completed = true
			return nil, fmt.Errorf("delegate cancelled: %w", ctx.Err())
		}
		completed = true
		return nil, fmt.Errorf("delegate failed: %w", err)
	}

	// Convert iterate.Result → delegate.Result.
	// Empty content after tool iterations is a no-output exhaustion.
	exhaustReason := iterResult.ExhaustReason
	exhausted := iterResult.Exhausted
	if iterResult.Content == "" && iterResult.IterationCount > 1 && !exhausted {
		exhausted = true
		exhaustReason = ExhaustNoOutput
	}

	completed = true
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.retainContent(bgCtx, did, messages[0].Content, userMsg.String(), iterResult)
	}()
	e.recordCompletion(&completionRecord{
		log:              log,
		delegateID:       did,
		conversationID:   convID,
		archiveSessionID: archiveSessionID,
		task:             task,
		guidance:         guidance,
		profileName:      profile.Name,
		model:            iterResult.Model,
		totalIter:        iterResult.IterationCount,
		maxIter:          maxIter,
		totalInput:       iterResult.InputTokens,
		totalOutput:      iterResult.OutputTokens,
		exhausted:        exhausted,
		exhaustReason:    exhaustReason,
		startTime:        startTime,
		messages:         iterResult.Messages,
		resultContent:    iterResult.Content,
		toolCalls:        toolCalls,
		iterations:       iterResult.Iterations,
	})
	return &Result{
		Content:       iterResult.Content,
		Model:         iterResult.Model,
		Iterations:    iterResult.IterationCount,
		InputTokens:   iterResult.InputTokens,
		OutputTokens:  iterResult.OutputTokens,
		Exhausted:     exhausted,
		ExhaustReason: exhaustReason,
		ToolCalls:     toolCalls,
		Duration:      time.Since(startTime),
	}, nil
}

type preparedExecution struct {
	id               string
	conversationID   string
	archiveSessionID string
	parentLoopID     string
	profile          *Profile
	log              *slog.Logger
	systemPrompt     string
	userMessage      string
	model            string
	toolNames        []string
	explicitTags     []string
	effectiveTags    []string
	maxIterations    int
	maxOutputTokens  int
	maxDuration      time.Duration
	toolTimeout      time.Duration
}

type loopOutcome struct {
	result *Result
	err    error
}

func (e *Executor) executeViaLoop(ctx context.Context, task, profileName, guidance string, tags []string, pathPrefixes map[string]string) (*Result, error) {
	prep, err := e.prepareExecution(ctx, task, profileName, guidance, tags, pathPrefixes)
	if err != nil {
		return nil, err
	}

	outcomeCh := make(chan loopOutcome, 1)
	loopName := "delegate-" + prep.id[:8]
	loopMaxDuration := prep.maxDuration + 5*time.Second
	if loopMaxDuration <= 0 {
		loopMaxDuration = prep.maxDuration
	}

	loopID, err := e.loopRegistry.SpawnLoop(ctx, looppkg.Config{
		Name:         loopName,
		MaxIter:      1,
		MaxDuration:  loopMaxDuration,
		SleepMin:     time.Millisecond,
		SleepMax:     time.Millisecond,
		SleepDefault: time.Millisecond,
		Jitter:       looppkg.Float64Ptr(0),
		ParentID:     prep.parentLoopID,
		Tags:         append([]string(nil), prep.effectiveTags...),
		Handler: func(hCtx context.Context, _ any) error {
			runCtx, cancel := context.WithTimeout(hCtx, prep.maxDuration)
			defer cancel()
			runStart := time.Now()

			var toolCalls []ToolCallOutcome
			progressStream := agent.BuildProgressStream(looppkg.ProgressFunc(hCtx))
			stream := func(evt agent.StreamEvent) {
				if progressStream != nil {
					progressStream(evt)
				}
				if evt.Kind == agent.KindToolCallDone {
					toolCalls = append(toolCalls, ToolCallOutcome{
						Name:    evt.ToolName,
						Success: evt.ToolError == "",
					})
				}
			}

			hints := make(map[string]string, len(prep.profile.RouterHints)+2)
			for k, v := range prep.profile.RouterHints {
				hints[k] = v
			}
			hints["source"] = "delegate"
			hints[router.HintDelegationGating] = "disabled"

			req := &agent.Request{
				ConversationID:  prep.conversationID,
				Messages:        []agent.Message{{Role: "user", Content: prep.userMessage}},
				Model:           prep.model,
				Hints:           hints,
				AllowedTools:    append([]string(nil), prep.toolNames...),
				SkipTagFilter:   len(prep.explicitTags) == 0,
				SeedTags:        append([]string(nil), prep.effectiveTags...),
				SystemPrompt:    prep.systemPrompt,
				MaxIterations:   prep.maxIterations,
				MaxOutputTokens: prep.maxOutputTokens,
				ToolTimeout:     prep.toolTimeout,
				UsageRole:       "delegate",
				UsageTaskName:   prep.profile.Name,
			}

			resp, runErr := e.loopRunner.Run(runCtx, req, stream)
			looppkg.ReportConversationID(hCtx, prep.conversationID)
			summary := looppkg.IterationSummary(hCtx)

			if runErr != nil {
				if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
					if summary != nil {
						summary["delegate_profile"] = prep.profile.Name
						summary["delegate_exhausted"] = true
						summary["delegate_finish_reason"] = ExhaustWallClock
					}
					outcomeCh <- loopOutcome{
						result: &Result{
							Content:       "Delegate was unable to complete the task within its time limit.",
							Model:         prep.model,
							Exhausted:     true,
							ExhaustReason: ExhaustWallClock,
							ToolCalls:     toolCalls,
							Duration:      time.Since(runStart),
						},
					}
					return nil
				}
				outcomeCh <- loopOutcome{err: fmt.Errorf("delegate failed: %w", runErr)}
				return runErr
			}

			looppkg.ReportAgentRun(hCtx, looppkg.AgentRunSummary{
				RequestID:    resp.RequestID,
				Model:        resp.Model,
				InputTokens:  resp.InputTokens,
				OutputTokens: resp.OutputTokens,
			})
			if summary != nil {
				summary["delegate_profile"] = prep.profile.Name
				summary["delegate_iterations"] = resp.Iterations
				summary["delegate_exhausted"] = resp.Exhausted
				if resp.FinishReason != "" {
					summary["delegate_finish_reason"] = resp.FinishReason
				}
			}

			exhausted := resp.Exhausted
			exhaustReason := resp.FinishReason
			if resp.Content == "" && len(toolCalls) > 0 && !exhausted {
				exhausted = true
				exhaustReason = ExhaustNoOutput
			}

			outcomeCh <- loopOutcome{
				result: &Result{
					Content:       resp.Content,
					Model:         resp.Model,
					Iterations:    resp.Iterations,
					InputTokens:   resp.InputTokens,
					OutputTokens:  resp.OutputTokens,
					Exhausted:     exhausted,
					ExhaustReason: exhaustReason,
					ToolCalls:     toolCalls,
					Duration:      time.Since(runStart),
				},
			}
			return nil
		},
		Metadata: map[string]string{
			"category":          "delegate",
			"delegate_id":       prep.id,
			"delegate_task":     truncate(task, 500),
			"delegate_profile":  prep.profile.Name,
			"delegate_guidance": truncate(guidance, 500),
		},
	}, looppkg.Deps{
		Logger:   prep.log,
		EventBus: e.eventBus,
	})
	if err != nil {
		e.finishLoopExecution(prep)
		return nil, fmt.Errorf("spawn delegate loop: %w", err)
	}

	select {
	case outcome := <-outcomeCh:
		e.finishLoopExecution(prep)
		if outcome.err != nil {
			return nil, outcome.err
		}
		return outcome.result, nil
	case <-ctx.Done():
		if l := e.loopRegistry.Get(loopID); l != nil {
			l.Stop()
		}
		e.finishLoopExecution(prep)
		return nil, fmt.Errorf("delegate cancelled: %w", ctx.Err())
	}
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
		if sid := e.sessionArchiver.ActiveSessionID(prep.conversationID); sid != "" {
			if err := e.sessionArchiver.EndSession(sid, "delegate"); err != nil {
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

func (e *Executor) prepareExecution(ctx context.Context, task, profileName, guidance string, tags []string, pathPrefixes map[string]string) (*preparedExecution, error) {
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	delegateID, _ := uuid.NewV7()
	did := delegateID.String()
	conversationID := "delegate-" + did[:8]

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

	var reg *tools.Registry
	if len(tags) > 0 {
		merged := append([]string(nil), tags...)
		merged = append(merged, e.alwaysActiveTags...)
		reg = e.parentReg.FilterByTags(merged)
		reg = reg.FilteredCopyExcluding([]string{delegateToolName})
	} else if len(profile.AllowedTools) > 0 {
		reg = e.parentReg.FilteredCopy(profile.AllowedTools)
	} else {
		reg = e.parentReg.FilteredCopyExcluding([]string{delegateToolName})
	}
	toolDefs := reg.List()
	toolNames := reg.AllToolNames()
	sort.Strings(toolNames)

	log = log.With("profile", profile.Name)
	ctx = logging.WithLogger(ctx, log)
	log.Info("delegate started",
		"task", truncate(task, 200),
		"guidance", truncate(guidance, 200),
		"tags", tags,
		"tools_available", len(toolDefs),
	)

	effectiveTagsMap := make(map[string]bool, len(tags)+len(e.alwaysActiveTags))
	for _, t := range tags {
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

	var sb strings.Builder
	sb.WriteString(profile.SystemPrompt)
	sb.WriteString("\n\n")
	sb.WriteString(awareness.CurrentConditions(e.timezone))
	if e.tagCtxFunc != nil && len(effectiveTagsMap) > 0 {
		if tagCtx := e.tagCtxFunc(ctx, effectiveTagsMap); tagCtx != "" {
			sb.WriteString("\n\n## Capability Context\n\n")
			sb.WriteString(tagCtx)
		}
	} else if e.forgeContext != "" {
		sb.WriteString("\n\n")
		sb.WriteString(e.forgeContext)
	}
	if prefixPrompt := formatPrefixPrompt(pathPrefixes, time.Now()); prefixPrompt != "" {
		sb.WriteString("\n\n")
		sb.WriteString(prefixPrompt)
	}

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
		profile:          profile,
		log:              log,
		systemPrompt:     sb.String(),
		userMessage:      userMsg.String(),
		model:            e.selectModel(ctx, task, profile, len(toolDefs)),
		toolNames:        toolNames,
		explicitTags:     append([]string(nil), tags...),
		effectiveTags:    effectiveTags,
		maxIterations:    maxIterations,
		maxOutputTokens:  maxOutputTokens,
		maxDuration:      maxDuration,
		toolTimeout:      toolTimeout,
	}, nil
}

// selectModel picks a model for the delegate via the router or falls back to the default.
func (e *Executor) selectModel(ctx context.Context, task string, profile *Profile, toolCount int) string {
	log := logging.Logger(ctx)
	if e.router != nil {
		model, _ := e.router.Route(ctx, router.Request{
			Query:      task,
			NeedsTools: toolCount > 0,
			ToolCount:  toolCount,
			Priority:   router.PriorityBackground,
			Hints:      profile.RouterHints,
		})
		if model != "" {
			log.Debug("delegate model selected by router",
				"model", model,
			)
			return model
		}
	}
	log.Debug("delegate using default model",
		"model", e.defaultModel,
	)
	return e.defaultModel
}

// completionRecord carries all data for logging and persistence of a
// delegate execution. The log field carries the context-enriched logger
// so recordCompletion and archiveSession inherit trace fields (request_id,
// session_id, conversation_id, subsystem, delegate_id, profile).
type completionRecord struct {
	log              *slog.Logger
	delegateID       string
	conversationID   string
	archiveSessionID string
	task             string
	guidance         string
	profileName      string
	model            string
	totalIter        int
	maxIter          int
	totalInput       int
	totalOutput      int
	exhausted        bool
	exhaustReason    string
	startTime        time.Time
	messages         []llm.Message
	resultContent    string
	errMsg           string
	toolCalls        []ToolCallOutcome
	iterations       []iterate.IterationRecord
}

// retainContent persists request-level content for a delegate execution.
// No-op when the content writer is nil (content retention disabled).
func (e *Executor) retainContent(ctx context.Context, delegateID, systemPrompt, userContent string, result *iterate.Result) {
	if e.contentWriter == nil || result == nil {
		return
	}
	e.contentWriter.WriteRequest(ctx, logging.RequestContent{
		RequestID:        delegateID,
		SystemPrompt:     systemPrompt,
		UserContent:      userContent,
		Model:            result.Model,
		AssistantContent: result.Content,
		IterationCount:   result.IterationCount,
		InputTokens:      result.InputTokens,
		OutputTokens:     result.OutputTokens,
		ToolsUsed:        result.ToolsUsed,
		Exhausted:        result.Exhausted,
		ExhaustReason:    result.ExhaustReason,
		Messages:         result.Messages,
	})
}

// recordCompletion logs and optionally persists a delegate execution.
func (e *Executor) recordCompletion(rec *completionRecord) {
	now := time.Now()
	elapsed := now.Sub(rec.startTime)

	rec.log.Info("delegate completed",
		"model", rec.model,
		"total_iter", rec.totalIter,
		"input_tokens", rec.totalInput,
		"output_tokens", rec.totalOutput,
		"exhausted", rec.exhausted,
		"exhaust_reason", rec.exhaustReason,
		"elapsed", elapsed.Round(time.Second),
	)

	// Archive session lifecycle: end the session and persist messages
	// and tool calls so they appear in the session inspector.
	if e.archiver != nil && rec.archiveSessionID != "" {
		e.archiveSession(rec, now)
	}

	// Record usage for cost tracking. Uses context.Background() because
	// the delegate's context may be cancelled (e.g., wall-clock exhaustion).
	if e.usageStore != nil {
		cost := usage.ComputeCost(rec.model, rec.totalInput, rec.totalOutput, e.pricing)
		usageRec := usage.Record{
			Timestamp:      now,
			RequestID:      rec.delegateID,
			ConversationID: rec.conversationID,
			Model:          rec.model,
			Provider:       usage.ResolveProvider(rec.model),
			InputTokens:    rec.totalInput,
			OutputTokens:   rec.totalOutput,
			CostUSD:        cost,
			Role:           "delegate",
			TaskName:       rec.profileName,
		}
		if err := e.usageStore.Record(context.Background(), usageRec); err != nil {
			rec.log.Warn("failed to record delegate usage", "error", err)
		}
	}
}

// archiveSession persists the delegate's messages, tool calls, and ends
// the archive session so the execution is visible in the session inspector.
func (e *Executor) archiveSession(rec *completionRecord, now time.Time) {
	sessionID := rec.archiveSessionID
	convID := rec.conversationID

	// Archive messages from the LLM conversation.
	var archived []memory.Message
	for i, m := range rec.messages {
		archived = append(archived, memory.Message{
			ConversationID: convID,
			SessionID:      sessionID,
			Role:           m.Role,
			Content:        m.Content,
			Timestamp:      rec.startTime.Add(time.Duration(i) * time.Millisecond),
			ToolCallID:     m.ToolCallID,
			ArchiveReason:  "delegate",
		})
	}
	if err := e.archiver.ArchiveMessages(archived); err != nil {
		rec.log.Warn("failed to archive delegate messages",
			"session_id", sessionID,
			"error", err,
		)
	}

	// Archive tool calls extracted from assistant messages.
	var archivedCalls []memory.ArchivedToolCall
	for _, m := range rec.messages {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			argsJSON := ""
			if tc.Function.Arguments != nil {
				argsBytes, _ := json.Marshal(tc.Function.Arguments)
				argsJSON = string(argsBytes)
			}

			// Find the matching tool result message for this call.
			var result, errStr string
			for _, rm := range rec.messages {
				if rm.Role == "tool" && rm.ToolCallID == tc.ID {
					if strings.HasPrefix(rm.Content, "Error: ") {
						errStr = strings.TrimSpace(strings.TrimPrefix(rm.Content, "Error: "))
					} else {
						result = rm.Content
					}
					break
				}
			}

			archivedCalls = append(archivedCalls, memory.ArchivedToolCall{
				ID:             tc.ID,
				ConversationID: convID,
				SessionID:      sessionID,
				ToolName:       tc.Function.Name,
				Arguments:      argsJSON,
				Result:         result,
				Error:          errStr,
				StartedAt:      now, // approximate
			})
		}
	}
	if err := e.archiver.ArchiveToolCalls(archivedCalls); err != nil {
		rec.log.Warn("failed to archive delegate tool calls",
			"session_id", sessionID,
			"error", err,
		)
	}

	// Archive iteration records and link tool calls to their iterations.
	if len(rec.iterations) > 0 {
		archived := make([]memory.ArchivedIteration, len(rec.iterations))
		for i, iter := range rec.iterations {
			archived[i] = memory.ArchivedIteration{
				SessionID:      sessionID,
				IterationIndex: iter.Index,
				Model:          iter.Model,
				InputTokens:    iter.InputTokens,
				OutputTokens:   iter.OutputTokens,
				ToolCallCount:  len(iter.ToolCallIDs),
				ToolCallIDs:    iter.ToolCallIDs,
				ToolsOffered:   iter.ToolsOffered,
				StartedAt:      iter.StartedAt,
				DurationMs:     iter.DurationMs,
				HasToolCalls:   iter.HasToolCalls,
				BreakReason:    iter.BreakReason,
			}
		}
		if err := e.archiver.ArchiveIterations(archived); err != nil {
			rec.log.Warn("failed to archive delegate iterations",
				"session_id", sessionID,
				"error", err,
			)
		}
		if err := e.archiver.LinkPendingIterationToolCalls(sessionID); err != nil {
			rec.log.Warn("failed to link delegate tool calls to iterations",
				"session_id", sessionID,
				"error", err,
			)
		}
	}

	endReason := "completed"
	if rec.exhausted && rec.exhaustReason != "" {
		endReason = rec.exhaustReason
	}
	if err := e.archiver.EndSessionAt(sessionID, endReason, now); err != nil {
		rec.log.Warn("failed to end delegate archive session",
			"session_id", sessionID,
			"error", err,
		)
	}
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
