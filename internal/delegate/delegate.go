package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/conditions"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/usage"
)

// delegateToolName is the tool name excluded from delegate registries to prevent recursion.
const delegateToolName = "thane_delegate"

// Exhaustion reason constants.
const (
	ExhaustMaxIterations = "max_iterations"
	ExhaustTokenBudget   = "token_budget"
	ExhaustWallClock     = "wall_clock"
	ExhaustNoOutput      = "no_output"
	ExhaustIllegalTool   = "illegal_tool"
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

// Executor runs delegated tasks using a lightweight iteration loop.
type Executor struct {
	logger           *slog.Logger
	llm              llm.Client
	router           *router.Router
	parentReg        *tools.Registry
	profiles         map[string]*Profile
	timezone         string
	defaultModel     string
	store            *DelegationStore
	archiver         *memory.ArchiveStore
	tempFiles        labelExpander
	usageStore       *usage.Store
	pricing          map[string]config.PricingEntry
	alwaysActiveTags []string
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

// SetTimezone configures the IANA timezone for Current Conditions
// in the delegate system prompt.
func (e *Executor) SetTimezone(tz string) {
	e.timezone = tz
}

// SetStore configures the delegation persistence store. When set, every
// [Executor.Execute] completion is recorded for replay and evaluation.
func (e *Executor) SetStore(s *DelegationStore) {
	e.store = s
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

// SetAlwaysActiveTags configures the capability tags that are
// automatically included in every tag-scoped delegation, regardless
// of which tags the caller requests. This mirrors the agent loop's
// always_active tag behavior.
func (e *Executor) SetAlwaysActiveTags(tags []string) {
	e.alwaysActiveTags = tags
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
func (e *Executor) Execute(ctx context.Context, task, profileName, guidance string, tags []string) (*Result, error) {
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	// Generate a unique delegate ID for log correlation.
	delegateID, _ := uuid.NewV7()
	did := delegateID.String()

	// Create an archive session for this delegate execution so it
	// appears in the session inspector alongside user conversations.
	convID := "delegate-" + did[:8]
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
			e.logger.Warn("failed to create archive session for delegate",
				"delegate_id", did, "error", err)
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

	e.logger.Info("delegate started",
		"delegate_id", did,
		"task", truncate(task, 200),
		"profile", profile.Name,
		"guidance", truncate(guidance, 200),
		"tags", tags,
		"tools_available", len(toolDefs),
	)

	// Build system prompt.
	var sb strings.Builder
	sb.WriteString(profile.SystemPrompt)
	sb.WriteString("\n\n")
	sb.WriteString(conditions.CurrentConditions(e.timezone))

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
	model := e.selectModel(ctx, did, task, profile, len(toolDefs))

	startTime := time.Now()
	var totalInput, totalOutput int
	var toolCalls []ToolCallOutcome
	var iterations []iterationRecord

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
			e.logger.Error("delegate terminated without completion record",
				"delegate_id", did,
				"profile", profileName,
				"elapsed", time.Since(startTime).Round(time.Millisecond),
			)
		}
	}()

	for i := range maxIter {
		// Check context at iteration boundary. External cancellation
		// (caller cancelled) returns an error; our own deadline
		// (wall clock enforcement) returns an exhaustion result.
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				e.logger.Warn("delegate context deadline at iteration boundary",
					"delegate_id", did,
					"profile", profile.Name,
					"iter", i,
					"elapsed", time.Since(startTime).Round(time.Millisecond),
				)
				completed = true
				e.recordCompletion(&completionRecord{
					delegateID:       did,
					conversationID:   convID,
					archiveSessionID: archiveSessionID,
					task:             task,
					guidance:         guidance,
					profileName:      profile.Name,
					model:            model,
					totalIter:        i,
					maxIter:          maxIter,
					totalInput:       totalInput,
					totalOutput:      totalOutput,
					exhausted:        true,
					exhaustReason:    ExhaustWallClock,
					startTime:        startTime,
					messages:         messages,
					resultContent:    "Delegate was unable to complete the task within its time limit.",
					errMsg:           err.Error(),
					toolCalls:        toolCalls,
					iterations:       iterations,
				})
				return &Result{
					Content:       "Delegate was unable to complete the task within its time limit.",
					Model:         model,
					Iterations:    i,
					InputTokens:   totalInput,
					OutputTokens:  totalOutput,
					Exhausted:     true,
					ExhaustReason: ExhaustWallClock,
					ToolCalls:     toolCalls,
					Duration:      time.Since(startTime),
				}, nil
			}
			// External cancellation — propagate as error.
			return nil, fmt.Errorf("delegate cancelled: %w", err)
		}

		// Check wall clock limit (may fire before context deadline due
		// to scheduling jitter).
		if time.Since(startTime) > maxDuration {
			e.logger.Warn("delegate wall clock exceeded",
				"delegate_id", did,
				"profile", profile.Name,
				"elapsed", time.Since(startTime).Round(time.Millisecond),
				"max_duration", maxDuration,
			)
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        i,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        true,
				exhaustReason:    ExhaustWallClock,
				startTime:        startTime,
				messages:         messages,
				toolCalls:        toolCalls,
				iterations:       iterations,
			})
		}

		iterStart := time.Now()

		e.logger.Info("delegate llm call",
			"delegate_id", did,
			"profile", profile.Name,
			"iter", i,
			"model", model,
			"msgs", len(messages),
		)

		resp, err := e.llm.ChatStream(ctx, model, messages, toolDefs, nil)
		if err != nil {
			// If our context deadline fired (wall clock enforcement),
			// return an exhaustion result instead of an opaque error.
			// Cannot call forceTextResponse — context is already cancelled.
			// External cancellation (context.Canceled) is propagated as an error.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				e.logger.Warn("delegate context deadline exceeded during llm call",
					"delegate_id", did,
					"profile", profile.Name,
					"iter", i,
					"elapsed", time.Since(startTime).Round(time.Millisecond),
					"max_duration", maxDuration,
				)
				completed = true
				e.recordCompletion(&completionRecord{
					delegateID:       did,
					conversationID:   convID,
					archiveSessionID: archiveSessionID,
					task:             task,
					guidance:         guidance,
					profileName:      profile.Name,
					model:            model,
					totalIter:        i + 1,
					maxIter:          maxIter,
					totalInput:       totalInput,
					totalOutput:      totalOutput,
					exhausted:        true,
					exhaustReason:    ExhaustWallClock,
					startTime:        startTime,
					messages:         messages,
					resultContent:    "Delegate was unable to complete the task within its time limit.",
					errMsg:           err.Error(),
					toolCalls:        toolCalls,
					iterations:       iterations,
				})
				return &Result{
					Content:       "Delegate was unable to complete the task within its time limit.",
					Model:         model,
					Iterations:    i + 1,
					InputTokens:   totalInput,
					OutputTokens:  totalOutput,
					Exhausted:     true,
					ExhaustReason: ExhaustWallClock,
					ToolCalls:     toolCalls,
					Duration:      time.Since(startTime),
				}, nil
			}
			// External cancellation — use consistent error form.
			if errors.Is(ctx.Err(), context.Canceled) {
				completed = true
				return nil, fmt.Errorf("delegate cancelled: %w", ctx.Err())
			}
			completed = true
			return nil, fmt.Errorf("delegate llm call failed (iter %d): %w", i, err)
		}

		totalInput += resp.InputTokens
		totalOutput += resp.OutputTokens

		// Re-check wall clock after LLM call — it may have taken a while.
		if time.Since(startTime) > maxDuration {
			e.logger.Warn("delegate wall clock exceeded after llm call",
				"delegate_id", did,
				"profile", profile.Name,
				"elapsed", time.Since(startTime).Round(time.Millisecond),
				"max_duration", maxDuration,
			)
			messages = append(messages, resp.Message)
			iterations = append(iterations, iterationRecord{
				index:        i,
				model:        model,
				inputTokens:  resp.InputTokens,
				outputTokens: resp.OutputTokens,
				startedAt:    iterStart,
				durationMs:   time.Since(iterStart).Milliseconds(),
				hasToolCalls: len(resp.Message.ToolCalls) > 0,
				breakReason:  ExhaustWallClock,
			})
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        i + 1,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        true,
				exhaustReason:    ExhaustWallClock,
				startTime:        startTime,
				messages:         messages,
				toolCalls:        toolCalls,
				iterations:       iterations,
			})
		}

		e.logger.Info("delegate llm response",
			"delegate_id", did,
			"profile", profile.Name,
			"iter", i,
			"model", model,
			"input_tokens", resp.InputTokens,
			"output_tokens", resp.OutputTokens,
			"tool_calls", len(resp.Message.ToolCalls),
			"elapsed", time.Since(iterStart).Round(time.Millisecond),
		)

		// Record this iteration for the execution trace.
		iterRec := iterationRecord{
			index:        i,
			model:        resp.Model,
			inputTokens:  resp.InputTokens,
			outputTokens: resp.OutputTokens,
			startedAt:    iterStart,
			durationMs:   time.Since(iterStart).Milliseconds(),
			hasToolCalls: len(resp.Message.ToolCalls) > 0,
		}

		// No tool calls — we have the final response.
		if len(resp.Message.ToolCalls) == 0 {
			content := resp.Message.Content
			messages = append(messages, resp.Message)
			iterations = append(iterations, iterRec)

			// Empty content after tool-call iterations is a silent failure.
			// The model completed its tool work but never produced text output.
			if content == "" && i > 0 {
				e.logger.Warn("delegate produced empty result after tool calls",
					"delegate_id", did,
					"profile", profile.Name,
					"iter", i+1,
				)
				completed = true
				e.recordCompletion(&completionRecord{
					delegateID:       did,
					conversationID:   convID,
					archiveSessionID: archiveSessionID,
					task:             task,
					guidance:         guidance,
					profileName:      profile.Name,
					model:            model,
					totalIter:        i + 1,
					maxIter:          maxIter,
					totalInput:       totalInput,
					totalOutput:      totalOutput,
					exhausted:        true,
					exhaustReason:    ExhaustNoOutput,
					startTime:        startTime,
					messages:         messages,
					resultContent:    "",
					toolCalls:        toolCalls,
					iterations:       iterations,
				})
				return &Result{
					Content:       "",
					Model:         model,
					Iterations:    i + 1,
					InputTokens:   totalInput,
					OutputTokens:  totalOutput,
					Exhausted:     true,
					ExhaustReason: ExhaustNoOutput,
					ToolCalls:     toolCalls,
					Duration:      time.Since(startTime),
				}, nil
			}

			completed = true
			e.recordCompletion(&completionRecord{
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        i + 1,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        false,
				startTime:        startTime,
				messages:         messages,
				resultContent:    content,
				toolCalls:        toolCalls,
				iterations:       iterations,
			})
			return &Result{
				Content:      content,
				Model:        model,
				Iterations:   i + 1,
				InputTokens:  totalInput,
				OutputTokens: totalOutput,
				Exhausted:    false,
				ToolCalls:    toolCalls,
				Duration:     time.Since(startTime),
			}, nil
		}

		// Check token budget before executing tools.
		if totalOutput >= maxTokens {
			e.logger.Warn("delegate token budget exhausted",
				"delegate_id", did,
				"profile", profile.Name,
				"cumul_output", totalOutput,
				"max_tokens", maxTokens,
			)
			iterRec.breakReason = ExhaustTokenBudget
			iterRec.durationMs = time.Since(iterStart).Milliseconds()
			iterations = append(iterations, iterRec)
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        i + 1,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        true,
				exhaustReason:    ExhaustTokenBudget,
				startTime:        startTime,
				messages:         messages,
				toolCalls:        toolCalls,
				iterations:       iterations,
			})
		}

		// Execute tool calls.
		messages = append(messages, resp.Message)
		var illegalCall bool
		for _, tc := range resp.Message.ToolCalls {
			// Track tool call ID for iteration linkage.
			iterRec.toolCallIDs = append(iterRec.toolCallIDs, tc.ID)

			argsJSON := ""
			if tc.Function.Arguments != nil {
				argsBytes, _ := json.Marshal(tc.Function.Arguments)
				argsJSON = string(argsBytes)
			}

			toolStart := time.Now()

			e.logger.Info("delegate tool exec",
				"delegate_id", did,
				"profile", profile.Name,
				"iter", i,
				"tool", tc.Function.Name,
			)

			toolCtx := tools.WithConversationID(ctx, convID)
			toolTimeout := profile.ToolTimeout
			if toolTimeout == 0 {
				toolTimeout = defaultToolTimeout
			}
			toolCtx, toolCancel := context.WithTimeout(toolCtx, toolTimeout)
			result, err := reg.Execute(toolCtx, tc.Function.Name, argsJSON)
			toolCancel()

			toolCalls = append(toolCalls, ToolCallOutcome{
				Name:    tc.Function.Name,
				Success: err == nil,
			})

			if err != nil {
				// Illegal tool call — tool not in delegate's registry.
				var unavail *tools.ErrToolUnavailable
				if errors.As(err, &unavail) {
					illegalCall = true
					result = fmt.Sprintf(prompts.IllegalToolMessage, tc.Function.Name)
					e.logger.Warn("delegate illegal tool call",
						"delegate_id", did,
						"profile", profile.Name,
						"tool", tc.Function.Name,
					)
				} else if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
					// Per-tool timeout fired but parent context is still alive —
					// report as a tool error so the LLM can adapt.
					e.logger.Warn("delegate tool exec timed out",
						"delegate_id", did,
						"profile", profile.Name,
						"tool", tc.Function.Name,
						"timeout", toolTimeout,
					)
					result = fmt.Sprintf("Error: tool %s timed out after %s", tc.Function.Name, toolTimeout)
				} else if ctx.Err() != nil {
					// Parent context is done (our deadline or external
					// cancellation) — stop executing remaining tool calls.
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						e.logger.Warn("delegate context deadline exceeded during tool exec",
							"delegate_id", did,
							"profile", profile.Name,
							"tool", tc.Function.Name,
							"elapsed", time.Since(startTime).Round(time.Millisecond),
						)
						iterRec.breakReason = ExhaustWallClock
						iterRec.durationMs = time.Since(iterStart).Milliseconds()
						iterations = append(iterations, iterRec)
						completed = true
						e.recordCompletion(&completionRecord{
							delegateID:       did,
							conversationID:   convID,
							archiveSessionID: archiveSessionID,
							task:             task,
							guidance:         guidance,
							profileName:      profile.Name,
							model:            model,
							totalIter:        i + 1,
							maxIter:          maxIter,
							totalInput:       totalInput,
							totalOutput:      totalOutput,
							exhausted:        true,
							exhaustReason:    ExhaustWallClock,
							startTime:        startTime,
							messages:         messages,
							resultContent:    "Delegate was unable to complete the task within its time limit.",
							errMsg:           err.Error(),
							toolCalls:        toolCalls,
							iterations:       iterations,
						})
						return &Result{
							Content:       "Delegate was unable to complete the task within its time limit.",
							Model:         model,
							Iterations:    i + 1,
							InputTokens:   totalInput,
							OutputTokens:  totalOutput,
							Exhausted:     true,
							ExhaustReason: ExhaustWallClock,
							ToolCalls:     toolCalls,
							Duration:      time.Since(startTime),
						}, nil
					}
					// External cancellation — stop tool execution and
					// propagate as error.
					completed = true
					return nil, fmt.Errorf("delegate cancelled: %w", ctx.Err())
				} else {
					// Generic tool error — report to LLM and continue.
					result = "Error: " + err.Error()
					e.logger.Error("delegate tool exec failed",
						"delegate_id", did,
						"profile", profile.Name,
						"tool", tc.Function.Name,
						"error", err,
					)
				}
			} else {
				e.logger.Debug("delegate tool exec done",
					"delegate_id", did,
					"profile", profile.Name,
					"tool", tc.Function.Name,
					"result_len", len(result),
					"elapsed", time.Since(toolStart).Round(time.Millisecond),
				)
			}

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// If any tool call was illegal, force a text response immediately.
		if illegalCall {
			e.logger.Warn("delegate illegal tool call, forcing text response",
				"delegate_id", did,
				"profile", profile.Name,
			)
			iterRec.breakReason = ExhaustIllegalTool
			iterRec.durationMs = time.Since(iterStart).Milliseconds()
			iterations = append(iterations, iterRec)
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        i + 1,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        true,
				exhaustReason:    ExhaustIllegalTool,
				startTime:        startTime,
				messages:         messages,
				toolCalls:        toolCalls,
				iterations:       iterations,
			})
		}

		// Re-check wall clock after tool execution.
		if time.Since(startTime) > maxDuration {
			e.logger.Warn("delegate wall clock exceeded after tool exec",
				"delegate_id", did,
				"profile", profile.Name,
				"elapsed", time.Since(startTime).Round(time.Millisecond),
				"max_duration", maxDuration,
			)
			iterRec.breakReason = ExhaustWallClock
			iterRec.durationMs = time.Since(iterStart).Milliseconds()
			iterations = append(iterations, iterRec)
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:       did,
				conversationID:   convID,
				archiveSessionID: archiveSessionID,
				task:             task,
				guidance:         guidance,
				profileName:      profile.Name,
				model:            model,
				totalIter:        i + 1,
				maxIter:          maxIter,
				totalInput:       totalInput,
				totalOutput:      totalOutput,
				exhausted:        true,
				exhaustReason:    ExhaustWallClock,
				startTime:        startTime,
				messages:         messages,
				toolCalls:        toolCalls,
				iterations:       iterations,
			})
		}

		// Normal end of tool-call iteration — finalize and continue.
		iterRec.durationMs = time.Since(iterStart).Milliseconds()
		iterations = append(iterations, iterRec)
	}

	// Max iterations exhausted — force a text response.
	e.logger.Warn("delegate max iterations reached",
		"delegate_id", did,
		"profile", profile.Name,
		"max_iter", maxIter,
	)
	completed = true
	return e.forceTextResponse(ctx, model, messages, &completionRecord{
		delegateID:       did,
		conversationID:   convID,
		archiveSessionID: archiveSessionID,
		task:             task,
		guidance:         guidance,
		profileName:      profile.Name,
		model:            model,
		totalIter:        maxIter,
		maxIter:          maxIter,
		totalInput:       totalInput,
		totalOutput:      totalOutput,
		exhausted:        true,
		exhaustReason:    ExhaustMaxIterations,
		startTime:        startTime,
		messages:         messages,
		toolCalls:        toolCalls,
		iterations:       iterations,
	})
}

// forceTextResponse makes a final LLM call with tools=nil to force text output.
func (e *Executor) forceTextResponse(ctx context.Context, model string, messages []llm.Message, rec *completionRecord) (*Result, error) {
	resp, err := e.llm.ChatStream(ctx, model, messages, nil, nil)
	if err != nil {
		rec.resultContent = "Delegate was unable to complete the task within its budget."
		rec.errMsg = err.Error()
		e.recordCompletion(rec)
		return &Result{
			Content:       rec.resultContent,
			Model:         model,
			Iterations:    rec.totalIter,
			InputTokens:   rec.totalInput,
			OutputTokens:  rec.totalOutput,
			Exhausted:     true,
			ExhaustReason: rec.exhaustReason,
			ToolCalls:     rec.toolCalls,
			Duration:      time.Since(rec.startTime),
		}, nil
	}

	rec.totalInput += resp.InputTokens
	rec.totalOutput += resp.OutputTokens
	rec.resultContent = resp.Message.Content
	rec.messages = append(rec.messages, resp.Message)
	e.recordCompletion(rec)

	return &Result{
		Content:       resp.Message.Content,
		Model:         model,
		Iterations:    rec.totalIter,
		InputTokens:   rec.totalInput,
		OutputTokens:  rec.totalOutput,
		Exhausted:     true,
		ExhaustReason: rec.exhaustReason,
		ToolCalls:     rec.toolCalls,
		Duration:      time.Since(rec.startTime),
	}, nil
}

// selectModel picks a model for the delegate via the router or falls back to the default.
func (e *Executor) selectModel(ctx context.Context, delegateID, task string, profile *Profile, toolCount int) string {
	if e.router != nil {
		model, _ := e.router.Route(ctx, router.Request{
			Query:      task,
			NeedsTools: toolCount > 0,
			ToolCount:  toolCount,
			Priority:   router.PriorityBackground,
			Hints:      profile.RouterHints,
		})
		if model != "" {
			e.logger.Debug("delegate model selected by router",
				"delegate_id", delegateID,
				"profile", profile.Name,
				"model", model,
			)
			return model
		}
	}
	e.logger.Debug("delegate using default model",
		"delegate_id", delegateID,
		"profile", profile.Name,
		"model", e.defaultModel,
	)
	return e.defaultModel
}

// iterationRecord collects per-iteration data during delegate execution
// for batch archiving after completion.
type iterationRecord struct {
	index        int
	model        string
	inputTokens  int
	outputTokens int
	toolCallIDs  []string
	startedAt    time.Time
	durationMs   int64
	hasToolCalls bool
	breakReason  string
}

// completionRecord carries all data for logging and persistence of a
// delegate execution.
type completionRecord struct {
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
	iterations       []iterationRecord
}

// recordCompletion logs and optionally persists a delegate execution.
func (e *Executor) recordCompletion(rec *completionRecord) {
	now := time.Now()
	elapsed := now.Sub(rec.startTime)

	e.logger.Info("delegate completed",
		"delegate_id", rec.delegateID,
		"profile", rec.profileName,
		"model", rec.model,
		"total_iter", rec.totalIter,
		"input_tokens", rec.totalInput,
		"output_tokens", rec.totalOutput,
		"exhausted", rec.exhausted,
		"exhaust_reason", rec.exhaustReason,
		"elapsed", elapsed.Round(time.Millisecond),
	)

	if e.store != nil {
		dr := &DelegationRecord{
			ID:             rec.delegateID,
			ConversationID: rec.conversationID,
			Task:           rec.task,
			Guidance:       rec.guidance,
			Profile:        rec.profileName,
			Model:          rec.model,
			Iterations:     rec.totalIter,
			MaxIterations:  rec.maxIter,
			InputTokens:    rec.totalInput,
			OutputTokens:   rec.totalOutput,
			Exhausted:      rec.exhausted,
			ExhaustReason:  rec.exhaustReason,
			ToolsCalled:    ExtractToolsCalled(rec.messages),
			Messages:       rec.messages,
			ResultContent:  rec.resultContent,
			StartedAt:      rec.startTime,
			CompletedAt:    now,
			DurationMs:     elapsed.Milliseconds(),
			Error:          rec.errMsg,
		}

		if err := e.store.Record(dr); err != nil {
			e.logger.Warn("failed to persist delegation record",
				"delegate_id", rec.delegateID,
				"error", err,
			)
		}
	}

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
			e.logger.Warn("failed to record delegate usage",
				"delegate_id", rec.delegateID,
				"error", err,
			)
		}
	}
}

// archiveSession persists the delegate's messages, tool calls, and ends
// the archive session so the execution is visible in the session inspector.
func (e *Executor) archiveSession(rec *completionRecord, now time.Time) {
	sessionID := rec.archiveSessionID
	convID := rec.conversationID

	// Archive messages from the LLM conversation.
	var archived []memory.ArchivedMessage
	for i, m := range rec.messages {
		archived = append(archived, memory.ArchivedMessage{
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
		e.logger.Warn("failed to archive delegate messages",
			"delegate_id", rec.delegateID,
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
		e.logger.Warn("failed to archive delegate tool calls",
			"delegate_id", rec.delegateID,
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
				IterationIndex: iter.index,
				Model:          iter.model,
				InputTokens:    iter.inputTokens,
				OutputTokens:   iter.outputTokens,
				ToolCallCount:  len(iter.toolCallIDs),
				StartedAt:      iter.startedAt,
				DurationMs:     iter.durationMs,
				HasToolCalls:   iter.hasToolCalls,
				BreakReason:    iter.breakReason,
			}
		}
		if err := e.archiver.ArchiveIterations(archived); err != nil {
			e.logger.Warn("failed to archive delegate iterations",
				"delegate_id", rec.delegateID,
				"session_id", sessionID,
				"error", err,
			)
		}
		for _, iter := range rec.iterations {
			if len(iter.toolCallIDs) > 0 {
				if err := e.archiver.LinkToolCallsToIteration(sessionID, iter.index, iter.toolCallIDs); err != nil {
					e.logger.Warn("failed to link delegate tool calls to iteration",
						"delegate_id", rec.delegateID,
						"session_id", sessionID,
						"error", err,
					)
				}
			}
		}
	}

	// Set message count and end the session.
	if err := e.archiver.SetSessionMessageCount(sessionID, len(archived)); err != nil {
		e.logger.Warn("failed to set delegate session message count",
			"delegate_id", rec.delegateID,
			"error", err,
		)
	}

	endReason := "completed"
	if rec.exhausted && rec.exhaustReason != "" {
		endReason = rec.exhaustReason
	}
	if err := e.archiver.EndSessionAt(sessionID, endReason, now); err != nil {
		e.logger.Warn("failed to end delegate archive session",
			"delegate_id", rec.delegateID,
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
