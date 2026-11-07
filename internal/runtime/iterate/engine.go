package iterate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
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
		iterations         []IterationRecord
		toolsUsed          = make(map[string]int)
		toolCallCounts     = make(map[string]int)
		ledger             = newToolLedger()
		totalInput         int
		totalOutput        int
		totalCacheCreate   int
		totalCacheCreate5m int
		totalCacheCreate1h int
		totalCacheRead     int
		illegalStrikes     int
		emptyRetried       bool
		deferredText       string
		heldReply          string
		// deferredTokens and heldTokens are the output tokens the calls
		// that wrote deferredText and heldReply spent. A stand-in costs
		// what its original call did, not the empty call it fills in for.
		deferredTokens int
		heldTokens     int
		breakReason    string
	)

	for i := 0; i < cfg.MaxIterations; i++ {
		iterLog := log.With("iter", i)
		iterCtx := logging.WithLogger(ctx, iterLog)

		// --- Mid-turn input merge (#1221) ---
		// Poll for newly-arrived conversation content at the iteration
		// boundary. This runs after any prior iteration appended its
		// tool_use/tool_result pair (that append completes before the
		// `continue` that returns here), and before this iteration's LLM
		// call, so the pair is never split. The callback delta-gates and
		// budget-caps its own output; the engine appends it verbatim as
		// user-role messages so the model sees the new input this call.
		if cfg.PullInput != nil {
			if pulled := cfg.PullInput(iterCtx); len(pulled) > 0 {
				messages = append(messages, pulled...)
				iterLog.Info("merged mid-turn input into live turn",
					"pulled_messages", len(pulled))
			}
		}

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
		// Spend the budget forward: what is left is this call's ceiling.
		// Without it the run can only notice an overrun after paying for
		// it in full, which on a runner generating single-digit tokens
		// per second is minutes of work discarded on arrival.
		// The call site rides only this call's context, not iterCtx, so
		// tools and callbacks never inherit a stamp that names a model
		// call they are not.
		callCtx := llm.WithCallSite(iterCtx, llm.CallSite{Iteration: i, Kind: llm.CallKindIteration})
		if cfg.MaxOutputTokens > 0 {
			callCtx = llm.WithMaxOutputTokens(callCtx, llm.ClampMaxOutputTokens(iterCtx, cfg.MaxOutputTokens-totalOutput))
		}
		llmResp, err := cfg.LLM.ChatStream(callCtx, model, messages, toolDefs, cfg.Stream)
		if err != nil && !errors.Is(err, llm.ErrOutputBudgetExhausted) && cfg.OnLLMError != nil {
			var newModel string
			llmResp, newModel, err = cfg.OnLLMError(callCtx, err, model, messages, toolDefs, cfg.Stream)
			if err == nil && newModel != "" {
				model = newModel
			}
		}
		if err != nil {
			partial := &Result{
				Model:                      model,
				UpstreamRequestID:          latestUpstreamRequestID(iterations),
				InputTokens:                totalInput,
				PeakInputTokens:            peakInputTokens(iterations),
				OutputTokens:               totalOutput,
				CacheCreationInputTokens:   totalCacheCreate,
				CacheCreation5mInputTokens: totalCacheCreate5m,
				CacheCreation1hInputTokens: totalCacheCreate1h,
				CacheReadInputTokens:       totalCacheRead,
				ToolsUsed:                  toolsUsed,
				ToolOutcomes:               ledger.snapshot(),
				Exhausted:                  true,
				Iterations:                 iterations,
				Messages:                   messages,
				IterationCount:             len(iterations),
			}
			if errors.Is(err, llm.ErrOutputBudgetExhausted) {
				iterLog.Warn("output budget exhausted during model call", "error", err)
				partial.ExhaustReason = ExhaustTokenBudget
				partial.Content = cfg.FallbackContent
				if partial.Content == "" {
					partial.Content = prompts.EmptyResponseFallback
				}
				return partial, nil
			}
			return partial, err
		}

		// Accumulate token usage.
		totalInput += llmResp.InputTokens
		totalOutput += llmResp.OutputTokens
		totalCacheCreate += llmResp.CacheCreationInputTokens
		totalCacheCreate5m += llmResp.CacheCreation5mInputTokens
		totalCacheCreate1h += llmResp.CacheCreation1hInputTokens
		totalCacheRead += llmResp.CacheReadInputTokens

		// --- Callback: LLM response ---
		if cfg.OnLLMResponse != nil {
			cfg.OnLLMResponse(iterCtx, llmResp, i)
		}

		// --- Budget check ---
		if cfg.outputBudgetExhausted(totalOutput) {
			iterLog.Warn("budget exhausted", "total_output", totalOutput)
			budgetRec := IterationRecord{
				Index:                      i,
				Model:                      llmResp.Model,
				UpstreamRequestID:          llmResp.UpstreamRequestID,
				StopReason:                 llmResp.StopReason,
				ToolCallSource:             llmResp.ToolCallSource,
				TextToolAction:             llmResp.TextToolAction,
				InputTokens:                llmResp.InputTokens,
				OutputTokens:               llmResp.OutputTokens,
				CacheCreationInputTokens:   llmResp.CacheCreationInputTokens,
				CacheCreation5mInputTokens: llmResp.CacheCreation5mInputTokens,
				CacheCreation1hInputTokens: llmResp.CacheCreation1hInputTokens,
				CacheReadInputTokens:       llmResp.CacheReadInputTokens,
				ToolsOffered:               toolDefsNames(toolDefs),
				StartedAt:                  iterStart,
				DurationMs:                 time.Since(iterStart).Milliseconds(),
				HasToolCalls:               len(llmResp.Message.ToolCalls) > 0,
				BreakReason:                ExhaustTokenBudget,
			}
			iterations = append(iterations, budgetRec)
			partial := &Result{
				Model:                      model,
				UpstreamRequestID:          latestUpstreamRequestID(iterations),
				InputTokens:                totalInput,
				PeakInputTokens:            peakInputTokens(iterations),
				OutputTokens:               totalOutput,
				CacheCreationInputTokens:   totalCacheCreate,
				CacheCreation5mInputTokens: totalCacheCreate5m,
				CacheCreation1hInputTokens: totalCacheCreate1h,
				CacheReadInputTokens:       totalCacheRead,
				ToolsUsed:                  toolsUsed,
				ToolOutcomes:               ledger.snapshot(),
				Exhausted:                  true,
				ExhaustReason:              ExhaustTokenBudget,
				Iterations:                 iterations,
				IterationCount:             i + 1,
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
			Index:                      i,
			Model:                      llmResp.Model,
			UpstreamRequestID:          llmResp.UpstreamRequestID,
			StopReason:                 llmResp.StopReason,
			ToolCallSource:             llmResp.ToolCallSource,
			TextToolAction:             llmResp.TextToolAction,
			InputTokens:                llmResp.InputTokens,
			OutputTokens:               llmResp.OutputTokens,
			CacheCreationInputTokens:   llmResp.CacheCreationInputTokens,
			CacheCreation5mInputTokens: llmResp.CacheCreation5mInputTokens,
			CacheCreation1hInputTokens: llmResp.CacheCreation1hInputTokens,
			CacheReadInputTokens:       llmResp.CacheReadInputTokens,
			ToolsOffered:               toolDefsNames(toolDefs),
			StartedAt:                  iterStart,
			HasToolCalls:               len(llmResp.Message.ToolCalls) > 0,
		}

		// =============================================
		// TOOL CALL PATH
		// =============================================
		if len(llmResp.Message.ToolCalls) > 0 {
			// When the model returns text alongside tool calls, capture it as
			// deferredText so an empty post-tool response falls back to it
			// instead of nudging the model into restating (issue #347).
			//
			// The text is deliberately NOT stripped from the message appended
			// to history. Erasing the model's pre-tool-call reasoning made it
			// lose its own plan across iterations: on a hard edit it would
			// narrate a long plan alongside a loop_definition_get read, the
			// stripped (empty) message went into history, and the next
			// iteration — now blind to that plan — stalled without ever
			// emitting the follow-up write. Keeping the text lets the reasoning
			// survive; the empty-response fallback below still consumes
			// deferredText, so #347's user-visible duplication stays fixed.
			if cfg.DeferMixedText && llmResp.Message.Content != "" {
				deferredText = llmResp.Message.Content
				deferredTokens = llmResp.OutputTokens
			}

			if cfg.NormalizeToolCall != nil {
				normalizedToolCalls := make([]llm.ToolCall, 0, len(llmResp.Message.ToolCalls))
				for _, rawTC := range llmResp.Message.ToolCalls {
					normalizedToolCalls = append(normalizedToolCalls, cfg.NormalizeToolCall(iterCtx, i, rawTC))
				}
				llmResp.Message.ToolCalls = normalizedToolCalls
			}

			// Add assistant message with tool calls.
			messages = append(messages, llmResp.Message)

			var illegalCall bool
			var batchHasNonMetaTool bool
			var toolLoopDetected bool

			for _, tc := range llmResp.Message.ToolCalls {
				toolName := tc.Function.Name
				// Named before the repeat guard, so a refused call is
				// charged to the document it would have written.
				target := cfg.targetOf(toolName, tc.Function.Arguments)

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
						Content:    cfg.repeatedCallMessage(toolName, toolCallCounts[callKey]),
						ToolCallID: tc.ID,
					})
					ledger.blocked(toolName, target)
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
					if toolName != "tag_activate" &&
						toolName != "tag_deactivate" &&
						toolName != "tag_reset" &&
						toolName != "tag_inspect" &&
						toolName != "lens_activate" &&
						toolName != "lens_deactivate" &&
						toolName != "lens_list" {
						batchHasNonMetaTool = true
					}
				}
				result = ledger.observe(toolName, target, tc.Function.Arguments, toolErr, result)

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

		// replyTokens is what the text this iteration ends on cost to
		// write. A stand-in below takes its original call's cost, so the
		// closure guard weighs the reply it would hold, not the empty
		// response that reply fills in for.
		replyTokens := llmResp.OutputTokens

		// Handle empty responses. After tool call iterations this is a
		// common failure mode, but first-turn empty completions are not
		// useful either, so treat both paths consistently when nudging
		// is enabled. What stands in for the missing text, newest first:
		// text the model wrote beside an earlier tool call (fresh text
		// clears it, so any left was written after the latest hold), a
		// nudge while an iteration remains to answer it, the reply a
		// closure hold folded into history, and last the fallback.
		if llmResp.Message.Content == "" {
			if deferredText != "" {
				iterLog.Info("using deferred text from prior iteration",
					"deferred_len", len(deferredText))
				llmResp.Message.Content = deferredText
				replyTokens = deferredTokens
			} else if cfg.NudgeOnEmpty && !emptyRetried && i+1 < cfg.MaxIterations {
				// The nudge needs a next iteration to answer it. On the
				// last one it would end the loop on a user message,
				// which force-text recovery does not answer, and the run
				// would return no content at all.
				iterLog.Warn("empty response, nudging model")
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
			} else if heldReply != "" {
				// A closure hold kept the turn open for input that arrived
				// after this reply was finished, and nothing since has
				// answered it. The held reply still answers everything
				// before that input, so it ships instead of the fallback.
				iterLog.Warn("empty response after a closure hold, returning the held reply",
					"held_len", len(heldReply), "nudged", emptyRetried)
				llmResp.Message.Content = heldReply
				replyTokens = heldTokens
			} else if cfg.NudgeOnEmpty && !emptyRetried {
				// The nudge was not spent, but no iteration remains to
				// answer it, so the run has run out. It ends the way any
				// run that runs out of iterations does: exhausted, with
				// force-text recovery trying once when the last message
				// is a tool result. A canned fallback here would read to
				// callers such as delegates as a run that finished.
				iterLog.Warn("empty response on the last iteration, ending the run as exhausted")
				break
			} else if cfg.NudgeOnEmpty {
				// The nudge was already spent and the model still wrote
				// nothing — apply fallback.
				iterLog.Error("empty response after nudge, returning fallback")
				fallback := cfg.FallbackContent
				if fallback == "" {
					fallback = prompts.EmptyResponseFallback
				}
				llmResp.Message.Content = fallback
			}
			// When NudgeOnEmpty is false and nothing earlier stands in,
			// empty content is preserved — the caller is responsible for
			// handling it (e.g., delegate treats it as ExhaustNoOutput).
		}

		// --- Closure-time input merge (#1221) ---
		// The model produced a complete reply and is about to end the turn.
		// If new conversation input arrived while it was composing, fold that
		// reply into history, append the new input, and keep the turn alive
		// for one more iteration instead of shipping an answer the next
		// message may already supersede. This is the second PullInput poll
		// site: it reaches the zero-tool-call conversational case the
		// top-of-iteration poll cannot, since a text-only reply has no later
		// boundary. OnTextResponse is deliberately NOT fired here — this is
		// not the turn's final reply, so memory/fact-extraction waits for the
		// real close. The callback's shared per-turn drain budget bounds how
		// many times a turn can be held open.
		//
		// The held reply is remembered as heldReply, so that when nothing
		// later produces text the turn returns it rather than the fallback
		// (the empty-response handling above). A newer text reply always
		// supersedes it: that reply either closes the turn here or is held
		// in its place.
		//
		// A hold must be able to answer what it pulls, and when it cannot
		// (see closureHoldBlocker) the engine does not poll here at all.
		// On the last allowed iteration, holding ended the loop on the
		// pulled user message, which force-text recovery does not answer:
		// the run returned no content, the reply was dropped, and the input
		// was drained into a turn that never answered it. With less output
		// allowance left than this reply spent, the held iteration's
		// ceiling is that remainder, so its answer is likely cut short, and
		// the cut-short text (or the budget fallback) would replace the
		// finished reply. Polling and then declining the hold would not
		// help, since whatever the callback returns counts as delivered (the
		// loop acks it with the turn). Unpolled, the input stays with the
		// caller; the loop's mailbox acks only the wake batch and what was
		// pulled, then re-wakes on whatever is still pending (the post-turn
		// ack and wakeIfMailboxNonEmpty in internal/runtime/loop's
		// Loop.run), which is also where input past the drain budget goes.
		// That re-wake is a new run with its own iterations and budget.
		if cfg.PullInput != nil && llmResp.Message.Content != "" {
			if blocker := cfg.closureHoldBlocker(i, totalOutput, replyTokens); blocker != "" {
				iterLog.Debug("closure poll skipped, reply closes the turn",
					"blocker", blocker)
			} else if pulled := cfg.PullInput(iterCtx); len(pulled) > 0 {
				messages = append(messages, llmResp.Message)
				messages = append(messages, pulled...)
				heldReply, heldTokens = llmResp.Message.Content, replyTokens
				iterLog.Info("held turn open for input arriving at closure",
					"pulled_messages", len(pulled))
				continue
			}
		}

		// Append the final assistant message before firing OnTextResponse
		// so the callback receives a complete message history.
		messages = append(messages, llmResp.Message)

		// --- Callback: text response ---
		if cfg.OnTextResponse != nil {
			cfg.OnTextResponse(iterCtx, llmResp.Message.Content, messages)
		}

		return &Result{
			Content:                    llmResp.Message.Content,
			Model:                      model,
			UpstreamRequestID:          latestUpstreamRequestID(iterations),
			InputTokens:                totalInput,
			PeakInputTokens:            peakInputTokens(iterations),
			OutputTokens:               totalOutput,
			CacheCreationInputTokens:   totalCacheCreate,
			CacheCreation5mInputTokens: totalCacheCreate5m,
			CacheCreation1hInputTokens: totalCacheCreate1h,
			CacheReadInputTokens:       totalCacheRead,
			ToolsUsed:                  toolsUsed,
			ToolOutcomes:               ledger.snapshot(),
			Exhausted:                  false,
			Iterations:                 iterations,
			Messages:                   messages,
			IterationCount:             i + 1,
		}, nil
	}

	// Loop exhausted or broken — force a text response.
	if breakReason == "" {
		breakReason = ExhaustMaxIterations
	}
	log.Warn("iteration loop ended", "reason", breakReason)

	return e.forceText(ctx, cfg, model, messages, &Result{
		Model:                      model,
		UpstreamRequestID:          latestUpstreamRequestID(iterations),
		InputTokens:                totalInput,
		PeakInputTokens:            peakInputTokens(iterations),
		OutputTokens:               totalOutput,
		CacheCreationInputTokens:   totalCacheCreate,
		CacheCreation5mInputTokens: totalCacheCreate5m,
		CacheCreation1hInputTokens: totalCacheCreate1h,
		CacheReadInputTokens:       totalCacheRead,
		ToolsUsed:                  toolsUsed,
		ToolOutcomes:               ledger.snapshot(),
		Exhausted:                  true,
		ExhaustReason:              breakReason,
		Iterations:                 iterations,
		IterationCount:             len(iterations),
	})
}

// forceText makes a final LLM call with tools=nil to force a text
// response. It fills in the partial result with the content and
// returns it.
func (e *Engine) forceText(ctx context.Context, cfg Config, model string, messages []llm.Message, partial *Result) (*Result, error) {
	log := logging.Logger(ctx)

	// Only attempt the force-text call if the last message is a tool result.
	if len(messages) > 0 && messages[len(messages)-1].Role == "tool" {
		log.Warn("forcing text response", "reason", partial.ExhaustReason, "model", model)

		// The recovery call is a real generation against the same
		// budget, and it is the one most likely to run when the budget
		// is already spent — so it takes what remains and no more. An
		// earlier version floored it at a few hundred tokens so a spent
		// run could still close with a sentence, which quietly let the
		// run finish past its stated ceiling. A budget that can be
		// exceeded to say something nice about being over budget is not
		// a budget; when nothing remains the caller gets the fallback
		// content, which is what that path already exists to provide.
		// The recovery call is recorded below as the next iteration, so
		// it is placed there too.
		recoveryCtx := llm.WithCallSite(ctx, llm.CallSite{Iteration: len(partial.Iterations), Kind: llm.CallKindForceText})
		if cfg.MaxOutputTokens > 0 {
			remaining := cfg.MaxOutputTokens - partial.OutputTokens
			if remaining <= 0 {
				log.Warn("skipping force-text recovery: output budget exhausted",
					"max_output_tokens", cfg.MaxOutputTokens,
					"output_tokens", partial.OutputTokens,
				)
				partial.Messages = messages
				return partial, nil
			}
			recoveryCtx = llm.WithMaxOutputTokens(recoveryCtx, llm.ClampMaxOutputTokens(ctx, remaining))
		}
		resp, err := cfg.LLM.ChatStream(recoveryCtx, model, messages, nil, cfg.Stream)
		if err != nil {
			if errors.Is(err, llm.ErrOutputBudgetExhausted) {
				partial.ExhaustReason = ExhaustTokenBudget
				log.Warn("output budget exhausted during force-text recovery", "model", model, "error", err)
			} else {
				log.Error("force-text LLM call failed", "model", model, "reason", partial.ExhaustReason, "error", err)
			}
			if partial.Content == "" {
				partial.Content = cfg.FallbackContent
				if partial.Content == "" {
					partial.Content = prompts.EmptyResponseFallback
				}
			}
			partial.Messages = messages
			return partial, nil
		}

		// The recovery call is reported like every other successful
		// call, under the index it is recorded at below, so a count of
		// per-call response lines includes the turn's final call.
		if cfg.OnLLMResponse != nil {
			cfg.OnLLMResponse(recoveryCtx, resp, len(partial.Iterations))
		}

		partial.InputTokens += resp.InputTokens
		partial.OutputTokens += resp.OutputTokens
		if cfg.outputBudgetExhausted(partial.OutputTokens) {
			partial.ExhaustReason = ExhaustTokenBudget
		}
		partial.CacheCreationInputTokens += resp.CacheCreationInputTokens
		partial.CacheCreation5mInputTokens += resp.CacheCreation5mInputTokens
		partial.CacheCreation1hInputTokens += resp.CacheCreation1hInputTokens
		partial.CacheReadInputTokens += resp.CacheReadInputTokens
		messages = append(messages, resp.Message)

		content := resp.Message.Content
		if content == "" {
			log.Error("empty response in force-text recovery", "model", model, "reason", partial.ExhaustReason)
			content = cfg.FallbackContent
			if content == "" {
				content = prompts.EmptyResponseFallback
			}
		}

		// Record the recovery call as its own iteration.
		partial.Iterations = append(partial.Iterations, IterationRecord{
			Index:                      len(partial.Iterations),
			Model:                      resp.Model,
			UpstreamRequestID:          resp.UpstreamRequestID,
			StopReason:                 resp.StopReason,
			ToolCallSource:             resp.ToolCallSource,
			TextToolAction:             resp.TextToolAction,
			InputTokens:                resp.InputTokens,
			OutputTokens:               resp.OutputTokens,
			CacheCreationInputTokens:   resp.CacheCreationInputTokens,
			CacheCreation5mInputTokens: resp.CacheCreation5mInputTokens,
			CacheCreation1hInputTokens: resp.CacheCreation1hInputTokens,
			CacheReadInputTokens:       resp.CacheReadInputTokens,
			StartedAt:                  time.Now(),
			HasToolCalls:               false,
			BreakReason:                partial.ExhaustReason,
		})
		partial.IterationCount = len(partial.Iterations)

		partial.Content = content
	}

	// partial was built before the recovery call, so both of these are
	// stale by exactly that one iteration. Recomputing from the records
	// covers it without a second accumulation path — and the recovery
	// call matters most here: it carries the whole accumulated history,
	// so it is frequently the largest single call of the turn.
	partial.UpstreamRequestID = latestUpstreamRequestID(partial.Iterations)
	partial.PeakInputTokens = peakInputTokens(partial.Iterations)
	partial.Messages = messages
	return partial, nil
}

// latestUpstreamRequestID returns the most recent non-empty upstream
// request ID across iterations, or "" when none captured one. Used to
// surface the final iteration's provider-side ID on Result without
// forcing every Result construction site to dig into Iterations.
func latestUpstreamRequestID(iters []IterationRecord) string {
	for i := len(iters) - 1; i >= 0; i-- {
		if iters[i].UpstreamRequestID != "" {
			return iters[i].UpstreamRequestID
		}
	}
	return ""
}

// peakInputTokens returns the largest single-call prompt among the
// recorded iterations, cached tokens included (see [PromptTokens]). It
// reads the records rather than tracking a running maximum beside
// totalInput so the two can never disagree: every Result site already
// derives from this slice, and a call that failed before producing a
// record contributes no usage to either number.
func peakInputTokens(iters []IterationRecord) int {
	peak := 0
	for _, it := range iters {
		peak = max(peak, PromptTokens(it.InputTokens, it.CacheCreationInputTokens, it.CacheReadInputTokens))
	}
	return peak
}
