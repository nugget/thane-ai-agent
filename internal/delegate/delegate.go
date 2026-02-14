package delegate

import (
	"context"
	"encoding/json"
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

// Result is the outcome of a delegated task execution.
type Result struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	Iterations   int    `json:"iterations"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Exhausted    bool   `json:"exhausted"`
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
	did := shortID(delegateID.String())

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

	maxIter := profile.MaxIter
	if maxIter <= 0 {
		maxIter = defaultMaxIter
	}
	maxTokens := profile.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	for i := range maxIter {
		// Check context cancellation at iteration boundary.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("delegate cancelled: %w", err)
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
			return nil, fmt.Errorf("delegate llm call failed (iter %d): %w", i, err)
		}

		totalInput += resp.InputTokens
		totalOutput += resp.OutputTokens

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
			e.logCompletion(did, profile.Name, model, i+1, totalInput, totalOutput, false, startTime)
			return &Result{
				Content:      resp.Message.Content,
				Model:        model,
				Iterations:   i + 1,
				InputTokens:  totalInput,
				OutputTokens: totalOutput,
				Exhausted:    false,
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
			return e.forceTextResponse(ctx, did, model, messages, profile.Name, i+1, totalInput, totalOutput, startTime)
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
			result, err := reg.Execute(toolCtx, tc.Function.Name, argsJSON)
			if err != nil {
				result = "Error: " + err.Error()
				e.logger.Error("delegate tool exec failed",
					"delegate_id", did,
					"profile", profile.Name,
					"tool", tc.Function.Name,
					"error", err,
				)
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
	}

	// Max iterations exhausted — force a text response.
	e.logger.Warn("delegate max iterations reached",
		"delegate_id", did,
		"profile", profile.Name,
		"max_iter", maxIter,
	)
	return e.forceTextResponse(ctx, did, model, messages, profile.Name, maxIter, totalInput, totalOutput, startTime)
}

// forceTextResponse makes a final LLM call with tools=nil to force text output.
func (e *Executor) forceTextResponse(ctx context.Context, delegateID, model string, messages []llm.Message, profileName string, totalIter, totalInput, totalOutput int, startTime time.Time) (*Result, error) {
	resp, err := e.llm.ChatStream(ctx, model, messages, nil, nil)
	if err != nil {
		e.logCompletion(delegateID, profileName, model, totalIter, totalInput, totalOutput, true, startTime)
		return &Result{
			Content:      "Delegate was unable to complete the task within its budget.",
			Model:        model,
			Iterations:   totalIter,
			InputTokens:  totalInput,
			OutputTokens: totalOutput,
			Exhausted:    true,
		}, nil
	}

	totalInput += resp.InputTokens
	totalOutput += resp.OutputTokens

	e.logCompletion(delegateID, profileName, model, totalIter, totalInput, totalOutput, true, startTime)
	return &Result{
		Content:      resp.Message.Content,
		Model:        model,
		Iterations:   totalIter,
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		Exhausted:    true,
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

// logCompletion logs structured information about a completed delegate execution.
func (e *Executor) logCompletion(delegateID, profileName, model string, totalIter, totalInput, totalOutput int, exhausted bool, startTime time.Time) {
	e.logger.Info("delegate completed",
		"delegate_id", delegateID,
		"profile", profileName,
		"model", model,
		"total_iter", totalIter,
		"input_tokens", totalInput,
		"output_tokens", totalOutput,
		"exhausted", exhausted,
		"elapsed", time.Since(startTime).Round(time.Millisecond),
	)
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

// shortID returns a truncated prefix of a UUID for compact log output.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
