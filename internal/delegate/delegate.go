package delegate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

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
	model := e.selectModel(ctx, task, profile, len(toolDefs))

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
			"profile", profile.Name,
			"iter", i+1,
			"model", model,
			"msgs", len(messages),
		)

		resp, err := e.llm.ChatStream(ctx, model, messages, toolDefs, nil)
		if err != nil {
			return nil, fmt.Errorf("delegate llm call failed (iter %d): %w", i+1, err)
		}

		totalInput += resp.InputTokens
		totalOutput += resp.OutputTokens

		e.logger.Info("delegate llm response",
			"profile", profile.Name,
			"iter", i+1,
			"model", model,
			"input_tokens", resp.InputTokens,
			"output_tokens", resp.OutputTokens,
			"tool_calls", len(resp.Message.ToolCalls),
			"elapsed", time.Since(iterStart).Round(time.Millisecond),
		)

		// No tool calls — we have the final response.
		if len(resp.Message.ToolCalls) == 0 {
			e.logCompletion(profile.Name, model, i+1, totalInput, totalOutput, false, startTime)
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
				"profile", profile.Name,
				"cumul_output", totalOutput,
				"max_tokens", maxTokens,
			)
			return e.forceTextResponse(ctx, model, messages, profile.Name, i+1, totalInput, totalOutput, startTime)
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
				"profile", profile.Name,
				"iter", i+1,
				"tool", tc.Function.Name,
			)

			toolCtx := tools.WithConversationID(ctx, "delegate")
			result, err := reg.Execute(toolCtx, tc.Function.Name, argsJSON)
			if err != nil {
				result = "Error: " + err.Error()
				e.logger.Error("delegate tool exec failed",
					"profile", profile.Name,
					"tool", tc.Function.Name,
					"error", err,
				)
			} else {
				e.logger.Debug("delegate tool exec done",
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
		"profile", profile.Name,
		"max_iter", maxIter,
	)
	return e.forceTextResponse(ctx, model, messages, profile.Name, maxIter, totalInput, totalOutput, startTime)
}

// forceTextResponse makes a final LLM call with tools=nil to force text output.
func (e *Executor) forceTextResponse(ctx context.Context, model string, messages []llm.Message, profileName string, iterations, totalInput, totalOutput int, startTime time.Time) (*Result, error) {
	resp, err := e.llm.ChatStream(ctx, model, messages, nil, nil)
	if err != nil {
		e.logCompletion(profileName, model, iterations, totalInput, totalOutput, true, startTime)
		return &Result{
			Content:      "Delegate was unable to complete the task within its budget.",
			Model:        model,
			Iterations:   iterations,
			InputTokens:  totalInput,
			OutputTokens: totalOutput,
			Exhausted:    true,
		}, nil
	}

	totalInput += resp.InputTokens
	totalOutput += resp.OutputTokens

	e.logCompletion(profileName, model, iterations, totalInput, totalOutput, true, startTime)
	return &Result{
		Content:      resp.Message.Content,
		Model:        model,
		Iterations:   iterations,
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		Exhausted:    true,
	}, nil
}

// selectModel picks a model for the delegate via the router or default.
func (e *Executor) selectModel(ctx context.Context, task string, profile *Profile, toolCount int) string {
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
				"profile", profile.Name,
				"model", model,
			)
			return model
		}
	}
	e.logger.Debug("delegate using default model",
		"profile", profile.Name,
		"model", e.defaultModel,
	)
	return e.defaultModel
}

func (e *Executor) logCompletion(profileName, model string, iterations, totalInput, totalOutput int, exhausted bool, startTime time.Time) {
	e.logger.Info("delegate completed",
		"profile", profileName,
		"model", model,
		"iterations", iterations,
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
