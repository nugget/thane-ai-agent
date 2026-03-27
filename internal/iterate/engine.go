package iterate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Engine runs the model iteration loop. It is stateless; all
// configuration is passed via [Config] and all per-run state lives
// on the stack inside [Engine.Run].
type Engine struct{}

// Run executes the iteration loop: call the LLM, execute any tool
// calls, feed results back, and repeat until the model produces a
// text-only response or a budget is exhausted.
//
// The caller provides the initial message history (including system
// prompt). Run appends assistant and tool messages during execution
// and returns the final state in [Result].
func (e *Engine) Run(ctx context.Context, cfg Config, messages []llm.Message) (*Result, error) {
	cfg.applyDefaults()
	log := logging.Logger(ctx)
	model := cfg.Model

	// Per-run tracking.
	var (
		iterations     []IterationRecord
		toolsUsed      = make(map[string]int)
		toolCallCounts = make(map[string]int)
		totalInput     int
		totalOutput    int
		illegalStrikes int
		emptyRetried   bool
		deferredText   string
		breakReason    string
	)

	for i := 0; i < cfg.MaxIterations; i++ {
		iterLog := log.With("iter", i)
		iterCtx := logging.WithLogger(ctx, iterLog)

		// Get tool definitions for this iteration.
		var toolDefs []map[string]any
		if cfg.ToolDefs != nil {
			toolDefs = cfg.ToolDefs(i)
		}

		// --- Callbacks: iteration start ---
		// toolDefs is resolved first so the callback has the full picture
		// (message history + tools offered) for accurate token estimation.
		if cfg.OnIterationStart != nil {
			cfg.OnIterationStart(iterCtx, i, model, messages, toolDefs)
		}

		iterStart := time.Now()

		// --- LLM call ---
		llmResp, err := cfg.LLM.ChatStream(iterCtx, model, messages, toolDefs, cfg.Stream)
		if err != nil {
			if cfg.OnLLMError != nil {
				var newModel string
				llmResp, newModel, err = cfg.OnLLMError(iterCtx, err, model, messages, toolDefs, cfg.Stream)
				if err != nil {
					return &Result{
						Model:          model,
						InputTokens:    totalInput,
						OutputTokens:   totalOutput,
						ToolsUsed:      toolsUsed,
						Exhausted:      true,
						Iterations:     iterations,
						Messages:       messages,
						IterationCount: len(iterations),
					}, err
				}
				if newModel != "" {
					model = newModel
				}
			} else {
				return &Result{
					Model:          model,
					InputTokens:    totalInput,
					OutputTokens:   totalOutput,
					ToolsUsed:      toolsUsed,
					Exhausted:      true,
					Iterations:     iterations,
					Messages:       messages,
					IterationCount: len(iterations),
				}, err
			}
		}

		// Accumulate token usage.
		totalInput += llmResp.InputTokens
		totalOutput += llmResp.OutputTokens

		// --- Callback: LLM response ---
		if cfg.OnLLMResponse != nil {
			cfg.OnLLMResponse(iterCtx, llmResp, i)
		}

		// --- Budget check ---
		if cfg.CheckBudget != nil && cfg.CheckBudget(totalOutput) {
			iterLog.Warn("budget exhausted", "total_output", totalOutput)
			budgetRec := IterationRecord{
				Index:        i,
				Model:        llmResp.Model,
				InputTokens:  llmResp.InputTokens,
				OutputTokens: llmResp.OutputTokens,
				ToolsOffered: toolDefsNames(toolDefs),
				StartedAt:    iterStart,
				DurationMs:   time.Since(iterStart).Milliseconds(),
				HasToolCalls: len(llmResp.Message.ToolCalls) > 0,
				BreakReason:  ExhaustTokenBudget,
			}
			iterations = append(iterations, budgetRec)
			partial := &Result{
				Model:          model,
				InputTokens:    totalInput,
				OutputTokens:   totalOutput,
				ToolsUsed:      toolsUsed,
				Exhausted:      true,
				ExhaustReason:  ExhaustTokenBudget,
				Iterations:     iterations,
				IterationCount: i + 1,
			}
			// Text-only response: use content directly.
			if llmResp.Message.Content != "" && len(llmResp.Message.ToolCalls) == 0 {
				messages = append(messages, llmResp.Message)
				partial.Content = llmResp.Message.Content
				partial.Messages = messages
				return partial, nil
			}
			// Tool-call or empty response: try forceText using the existing
			// message history. Do NOT append the unexecuted tool-call
			// response — it would create unmatched tool calls. forceText
			// fires only when the last message is a tool result; if not,
			// ensure content is non-empty via fallback.
			forceResult, forceErr := e.forceText(ctx, cfg, model, messages, partial)
			if forceErr != nil {
				return nil, forceErr
			}
			if forceResult.Content == "" {
				forceResult.Content = cfg.FallbackContent
				if forceResult.Content == "" {
					forceResult.Content = prompts.EmptyResponseFallback
				}
			}
			return forceResult, nil
		}

		// Build iteration record.
		iterRec := IterationRecord{
			Index:        i,
			Model:        llmResp.Model,
			InputTokens:  llmResp.InputTokens,
			OutputTokens: llmResp.OutputTokens,
			ToolsOffered: toolDefsNames(toolDefs),
			StartedAt:    iterStart,
			HasToolCalls: len(llmResp.Message.ToolCalls) > 0,
		}

		// =============================================
		// TOOL CALL PATH
		// =============================================
		if len(llmResp.Message.ToolCalls) > 0 {
			// When the model returns text alongside tool calls, defer
			// the text for later use and strip it from the message
			// context. This prevents the model from restating already-
			// streamed content after tool execution (issue #347).
			if cfg.DeferMixedText && llmResp.Message.Content != "" {
				deferredText = llmResp.Message.Content
				llmResp.Message.Content = ""
			}

			// Add assistant message with tool calls.
			messages = append(messages, llmResp.Message)

			var illegalCall bool
			var batchHasNonMetaTool bool
			var toolLoopDetected bool

			for _, tc := range llmResp.Message.ToolCalls {
				toolName := tc.Function.Name

				// Marshal arguments to JSON.
				argsJSON := ""
				if tc.Function.Arguments != nil {
					argsBytes, _ := json.Marshal(tc.Function.Arguments)
					argsJSON = string(argsBytes)
				}

				// Detect tool call loops. Append an error result but
				// continue processing remaining calls in the batch so
				// every tool call has a matching result — the API
				// requires a 1:1 correspondence.
				callKey := toolName + ":" + argsJSON
				toolCallCounts[callKey]++
				if toolCallCounts[callKey] > cfg.MaxToolRepeat {
					iterLog.Warn("tool call loop detected",
						"tool", toolName,
						"repeat_count", toolCallCounts[callKey],
					)
					messages = append(messages, llm.Message{
						Role:       "tool",
						Content:    fmt.Sprintf("Error: tool '%s' has been called %d times with the same arguments. Stop calling tools and provide your response to the user.", toolName, toolCallCounts[callKey]),
						ToolCallID: tc.ID,
					})
					toolLoopDetected = true
					continue // skip execution; move to next tool in batch
				}

				iterLog.Info("tool exec", "tool", toolName)
				if iterLog.Enabled(iterCtx, slog.LevelDebug) {
					argPreview := argsJSON
					if len(argPreview) > 200 {
						argPreview = argPreview[:200] + "..."
					}
					iterLog.Debug("tool exec args", "tool", toolName, "args", argPreview)
				}

				// --- Callback: tool call start ---
				if cfg.OnToolCallStart != nil {
					cfg.OnToolCallStart(iterCtx, tc)
				}

				// Enrich context before execution.
				toolCtx := iterCtx
				if cfg.OnBeforeToolExec != nil {
					toolCtx = cfg.OnBeforeToolExec(iterCtx, i, tc)
				}

				// Record tool call ID. Prefer the internal ID injected
				// into toolCtx by OnBeforeToolExec (e.g. a UUID stored
				// by the agent for DB linking), falling back to the
				// LLM-assigned tc.ID.
				toolCallRecordID := tc.ID
				if id := tools.ToolCallIDFromContext(toolCtx); id != "" {
					toolCallRecordID = id
				}

				// Check tool availability.
				var result string
				var toolErr error
				if cfg.CheckToolAvail != nil && !cfg.CheckToolAvail(toolName) {
					toolErr = &tools.ErrToolUnavailable{ToolName: toolName}
					iterLog.Warn("blocked call to unavailable tool", "tool", toolName)
				} else {
					result, toolErr = cfg.Executor.Execute(toolCtx, toolName, argsJSON)
				}
				toolsUsed[toolName]++
				iterRec.ToolCallIDs = append(iterRec.ToolCallIDs, toolCallRecordID)

				errMsg := ""
				if toolErr != nil {
					errMsg = toolErr.Error()
					var unavail *tools.ErrToolUnavailable
					if errors.As(toolErr, &unavail) {
						illegalCall = true
						result = fmt.Sprintf(prompts.IllegalToolMessage, toolName)
						iterLog.Warn("illegal tool call", "tool", toolName)
					} else {
						result = "Error: " + errMsg
						iterLog.Error("tool exec failed", "tool", toolName, "error", toolErr)
					}
				} else {
					iterLog.Debug("tool exec done", "tool", toolName, "result_len", len(result))
					if toolName != "request_capability" && toolName != "drop_capability" {
						batchHasNonMetaTool = true
					}
				}

				// --- Callback: tool call done ---
				// Pass toolCtx so the callback can access values injected
				// by OnBeforeToolExec (tool_call_id, per-tool deadline, etc.).
				if cfg.OnToolCallDone != nil {
					cfg.OnToolCallDone(toolCtx, toolName, result, errMsg)
				}

				// Add tool result message.
				messages = append(messages, llm.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
				})
			}

			if toolLoopDetected {
				iterRec.BreakReason = "tool_loop"
			}

			// Illegal tool strike counting.
			if illegalCall {
				illegalStrikes++
				if illegalStrikes >= cfg.MaxIllegalStrikes {
					iterLog.Warn("repeated illegal tool calls, breaking loop",
						"strikes", illegalStrikes)
					breakReason = ExhaustIllegalTool
					iterRec.BreakReason = ExhaustIllegalTool
					iterRec.DurationMs = time.Since(iterStart).Milliseconds()
					iterations = append(iterations, iterRec)
					break
				}
				iterLog.Warn("illegal tool call, allowing recovery iteration",
					"strikes", illegalStrikes)
			} else if batchHasNonMetaTool {
				illegalStrikes = 0
			}

			iterRec.DurationMs = time.Since(iterStart).Milliseconds()
			iterations = append(iterations, iterRec)
			continue
		}

		// =============================================
		// TEXT RESPONSE PATH
		// =============================================
		iterRec.DurationMs = time.Since(iterStart).Milliseconds()
		iterations = append(iterations, iterRec)

		// If the model produced fresh text after tool execution, discard
		// any deferred text — the model is providing a new response.
		if llmResp.Message.Content != "" && deferredText != "" {
			deferredText = ""
		}

		// Handle empty responses after tool call iterations.
		if llmResp.Message.Content == "" && i > 0 {
			if deferredText != "" {
				iterLog.Info("using deferred text from prior iteration",
					"deferred_len", len(deferredText))
				llmResp.Message.Content = deferredText
			} else if cfg.NudgeOnEmpty && !emptyRetried {
				iterLog.Warn("empty response after tool calls, nudging model")
				prompt := cfg.NudgePrompt
				if prompt == "" {
					prompt = prompts.EmptyResponseNudge
				}
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: prompt,
				})
				emptyRetried = true
				continue
			} else if cfg.NudgeOnEmpty {
				// Nudge was already attempted and failed — apply fallback.
				iterLog.Error("empty response after nudge, returning fallback")
				fallback := cfg.FallbackContent
				if fallback == "" {
					fallback = prompts.EmptyResponseFallback
				}
				llmResp.Message.Content = fallback
			}
			// When NudgeOnEmpty is false, empty content is preserved —
			// the caller is responsible for handling it (e.g., delegate
			// treats it as ExhaustNoOutput).
		}

		// Append the final assistant message before firing OnTextResponse
		// so the callback receives a complete message history.
		messages = append(messages, llmResp.Message)

		// --- Callback: text response ---
		if cfg.OnTextResponse != nil {
			cfg.OnTextResponse(iterCtx, llmResp.Message.Content, messages)
		}

		return &Result{
			Content:        llmResp.Message.Content,
			Model:          model,
			InputTokens:    totalInput,
			OutputTokens:   totalOutput,
			ToolsUsed:      toolsUsed,
			Exhausted:      false,
			Iterations:     iterations,
			Messages:       messages,
			IterationCount: i + 1,
		}, nil
	}

	// Loop exhausted or broken — force a text response.
	if breakReason == "" {
		breakReason = ExhaustMaxIterations
	}
	log.Warn("iteration loop ended", "reason", breakReason)

	return e.forceText(ctx, cfg, model, messages, &Result{
		Model:          model,
		InputTokens:    totalInput,
		OutputTokens:   totalOutput,
		ToolsUsed:      toolsUsed,
		Exhausted:      true,
		ExhaustReason:  breakReason,
		Iterations:     iterations,
		IterationCount: len(iterations),
	})
}

// forceText makes a final LLM call with tools=nil to force a text
// response. It fills in the partial result with the content and
// returns it.
func (e *Engine) forceText(ctx context.Context, cfg Config, model string, messages []llm.Message, partial *Result) (*Result, error) {
	log := logging.Logger(ctx)

	// Only attempt the force-text call if the last message is a tool result.
	if len(messages) > 0 && messages[len(messages)-1].Role == "tool" {
		log.Warn("forcing text response", "reason", partial.ExhaustReason)

		resp, err := cfg.LLM.ChatStream(ctx, model, messages, nil, cfg.Stream)
		if err != nil {
			log.Error("force-text LLM call failed", "error", err)
			if partial.Content == "" {
				partial.Content = cfg.FallbackContent
				if partial.Content == "" {
					partial.Content = prompts.EmptyResponseFallback
				}
			}
			partial.Messages = messages
			return partial, nil
		}

		partial.InputTokens += resp.InputTokens
		partial.OutputTokens += resp.OutputTokens
		messages = append(messages, resp.Message)

		content := resp.Message.Content
		if content == "" {
			log.Error("empty response in force-text recovery")
			content = cfg.FallbackContent
			if content == "" {
				content = prompts.EmptyResponseFallback
			}
		}

		// Record the recovery call as its own iteration.
		partial.Iterations = append(partial.Iterations, IterationRecord{
			Index:        len(partial.Iterations),
			Model:        resp.Model,
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			StartedAt:    time.Now(),
			HasToolCalls: false,
			BreakReason:  partial.ExhaustReason,
		})
		partial.IterationCount = len(partial.Iterations)

		partial.Content = content
	}

	partial.Messages = messages
	return partial, nil
}

// toolDefsNames extracts tool names from OpenAI-format tool definitions.
func toolDefsNames(defs []map[string]any) []string {
	if len(defs) == 0 {
		return nil
	}
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		fn, _ := def["function"].(map[string]any)
		if fn != nil {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}
