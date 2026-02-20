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
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// delegateToolName is the tool name excluded from delegate registries to prevent recursion.
const delegateToolName = "thane_delegate"

// Exhaustion reason constants.
const (
	ExhaustMaxIterations = "max_iterations"
	ExhaustTokenBudget   = "token_budget"
	ExhaustWallClock     = "wall_clock"
	ExhaustNoOutput      = "no_output"
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

// Executor runs delegated tasks using a lightweight iteration loop.
type Executor struct {
	logger       *slog.Logger
	llm          llm.Client
	router       *router.Router
	parentReg    *tools.Registry
	profiles     map[string]*Profile
	timezone     string
	defaultModel string
	store        *DelegationStore
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

// ProfileNames returns the names of all registered profiles.
func (e *Executor) ProfileNames() []string {
	names := make([]string, 0, len(e.profiles))
	for name := range e.profiles {
		names = append(names, name)
	}
	return names
}

// Execute runs a delegated task with the given profile and guidance.
func (e *Executor) Execute(ctx context.Context, task, profileName, guidance string) (*Result, error) {
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	// Generate a unique delegate ID for log correlation.
	delegateID, _ := uuid.NewV7()
	did := delegateID.String()

	profile := e.profiles[profileName]
	if profile == nil {
		profile = e.profiles["general"]
	}

	// Build filtered tool registry.
	var reg *tools.Registry
	if len(profile.AllowedTools) > 0 {
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
		"tools_available", len(toolDefs),
	)

	// Build system prompt.
	var sb strings.Builder
	sb.WriteString(profile.SystemPrompt)
	sb.WriteString("\n\n")
	sb.WriteString(conditions.CurrentConditions(e.timezone))

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
					delegateID:     did,
					conversationID: tools.ConversationIDFromContext(ctx),
					task:           task,
					guidance:       guidance,
					profileName:    profile.Name,
					model:          model,
					totalIter:      i,
					maxIter:        maxIter,
					totalInput:     totalInput,
					totalOutput:    totalOutput,
					exhausted:      true,
					exhaustReason:  ExhaustWallClock,
					startTime:      startTime,
					messages:       messages,
					resultContent:  "Delegate was unable to complete the task within its time limit.",
					errMsg:         err.Error(),
					toolCalls:      toolCalls,
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
				delegateID:     did,
				conversationID: tools.ConversationIDFromContext(ctx),
				task:           task,
				guidance:       guidance,
				profileName:    profile.Name,
				model:          model,
				totalIter:      i,
				maxIter:        maxIter,
				totalInput:     totalInput,
				totalOutput:    totalOutput,
				exhausted:      true,
				exhaustReason:  ExhaustWallClock,
				startTime:      startTime,
				messages:       messages,
				toolCalls:      toolCalls,
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
					delegateID:     did,
					conversationID: tools.ConversationIDFromContext(ctx),
					task:           task,
					guidance:       guidance,
					profileName:    profile.Name,
					model:          model,
					totalIter:      i + 1,
					maxIter:        maxIter,
					totalInput:     totalInput,
					totalOutput:    totalOutput,
					exhausted:      true,
					exhaustReason:  ExhaustWallClock,
					startTime:      startTime,
					messages:       messages,
					resultContent:  "Delegate was unable to complete the task within its time limit.",
					errMsg:         err.Error(),
					toolCalls:      toolCalls,
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
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:     did,
				conversationID: tools.ConversationIDFromContext(ctx),
				task:           task,
				guidance:       guidance,
				profileName:    profile.Name,
				model:          model,
				totalIter:      i + 1,
				maxIter:        maxIter,
				totalInput:     totalInput,
				totalOutput:    totalOutput,
				exhausted:      true,
				exhaustReason:  ExhaustWallClock,
				startTime:      startTime,
				messages:       messages,
				toolCalls:      toolCalls,
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

		// No tool calls — we have the final response.
		if len(resp.Message.ToolCalls) == 0 {
			content := resp.Message.Content
			messages = append(messages, resp.Message)

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
					delegateID:     did,
					conversationID: tools.ConversationIDFromContext(ctx),
					task:           task,
					guidance:       guidance,
					profileName:    profile.Name,
					model:          model,
					totalIter:      i + 1,
					maxIter:        maxIter,
					totalInput:     totalInput,
					totalOutput:    totalOutput,
					exhausted:      true,
					exhaustReason:  ExhaustNoOutput,
					startTime:      startTime,
					messages:       messages,
					resultContent:  "",
					toolCalls:      toolCalls,
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
				delegateID:     did,
				conversationID: tools.ConversationIDFromContext(ctx),
				task:           task,
				guidance:       guidance,
				profileName:    profile.Name,
				model:          model,
				totalIter:      i + 1,
				maxIter:        maxIter,
				totalInput:     totalInput,
				totalOutput:    totalOutput,
				exhausted:      false,
				startTime:      startTime,
				messages:       messages,
				resultContent:  content,
				toolCalls:      toolCalls,
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
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:     did,
				conversationID: tools.ConversationIDFromContext(ctx),
				task:           task,
				guidance:       guidance,
				profileName:    profile.Name,
				model:          model,
				totalIter:      i + 1,
				maxIter:        maxIter,
				totalInput:     totalInput,
				totalOutput:    totalOutput,
				exhausted:      true,
				exhaustReason:  ExhaustTokenBudget,
				startTime:      startTime,
				messages:       messages,
				toolCalls:      toolCalls,
			})
		}

		// Execute tool calls.
		messages = append(messages, resp.Message)
		for _, tc := range resp.Message.ToolCalls {
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

			toolCtx := tools.WithConversationID(ctx, "delegate")
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
				// Per-tool timeout fired but parent context is still alive —
				// report as a tool error so the LLM can adapt.
				if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
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
						completed = true
						e.recordCompletion(&completionRecord{
							delegateID:     did,
							conversationID: tools.ConversationIDFromContext(ctx),
							task:           task,
							guidance:       guidance,
							profileName:    profile.Name,
							model:          model,
							totalIter:      i + 1,
							maxIter:        maxIter,
							totalInput:     totalInput,
							totalOutput:    totalOutput,
							exhausted:      true,
							exhaustReason:  ExhaustWallClock,
							startTime:      startTime,
							messages:       messages,
							resultContent:  "Delegate was unable to complete the task within its time limit.",
							errMsg:         err.Error(),
							toolCalls:      toolCalls,
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

		// Re-check wall clock after tool execution.
		if time.Since(startTime) > maxDuration {
			e.logger.Warn("delegate wall clock exceeded after tool exec",
				"delegate_id", did,
				"profile", profile.Name,
				"elapsed", time.Since(startTime).Round(time.Millisecond),
				"max_duration", maxDuration,
			)
			completed = true
			return e.forceTextResponse(ctx, model, messages, &completionRecord{
				delegateID:     did,
				conversationID: tools.ConversationIDFromContext(ctx),
				task:           task,
				guidance:       guidance,
				profileName:    profile.Name,
				model:          model,
				totalIter:      i + 1,
				maxIter:        maxIter,
				totalInput:     totalInput,
				totalOutput:    totalOutput,
				exhausted:      true,
				exhaustReason:  ExhaustWallClock,
				startTime:      startTime,
				messages:       messages,
				toolCalls:      toolCalls,
			})
		}
	}

	// Max iterations exhausted — force a text response.
	e.logger.Warn("delegate max iterations reached",
		"delegate_id", did,
		"profile", profile.Name,
		"max_iter", maxIter,
	)
	completed = true
	return e.forceTextResponse(ctx, model, messages, &completionRecord{
		delegateID:     did,
		conversationID: tools.ConversationIDFromContext(ctx),
		task:           task,
		guidance:       guidance,
		profileName:    profile.Name,
		model:          model,
		totalIter:      maxIter,
		maxIter:        maxIter,
		totalInput:     totalInput,
		totalOutput:    totalOutput,
		exhausted:      true,
		exhaustReason:  ExhaustMaxIterations,
		startTime:      startTime,
		messages:       messages,
		toolCalls:      toolCalls,
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

// completionRecord carries all data for logging and persistence of a
// delegate execution.
type completionRecord struct {
	delegateID     string
	conversationID string
	task           string
	guidance       string
	profileName    string
	model          string
	totalIter      int
	maxIter        int
	totalInput     int
	totalOutput    int
	exhausted      bool
	exhaustReason  string
	startTime      time.Time
	messages       []llm.Message
	resultContent  string
	errMsg         string
	toolCalls      []ToolCallOutcome
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

	if e.store == nil {
		return
	}

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
