package prompts

import "strings"

// EmptyResponseNudge is the prompt injected when the model returns no
// content after executing tool calls. It gives the model one more
// chance to produce a user-visible response.
const EmptyResponseNudge = "You executed tool calls but did not provide a response to the user. Please respond now."

// EmptyResponseFallback is the user-facing message returned when the
// model fails to produce content even after being nudged (or during
// max-iterations recovery).
const EmptyResponseFallback = "I processed your request but wasn't able to compose a response. Please try again."

// InteractiveEmptyResponseFallback is a safer user-visible fallback for
// interactive loops that must return something even when the model ends
// the turn without content.
const InteractiveEmptyResponseFallback = "I hit a problem before I could finish that. Please try again."

// RuntimeContract teaches the live execution model that prompt-injected
// identity files cannot reliably convey: exact tool naming, capability
// activation semantics, delegation when top-level tools are gated, and
// semantic path references like kb:article.md.
func RuntimeContract() string {
	return strings.Join([]string{
		"## Runtime Contract",
		"",
		"Keep the straight path clean. If persona, mission, conversation history, and current context are enough, answer directly.",
		"",
		"Capabilities are bright entry points into richer tool, context, and talent menus. When a task needs a domain, open one relevant door, read what appears, and keep moving without narrating the machinery.",
		"",
		"- Use only exact tool names that are actually available in this turn. Do not invent aliases, wrappers, or MCP helper tools.",
		"- Use capability tools for runtime state: `activate_capability`, `deactivate_capability`, `reset_capabilities`, `list_loaded_capabilities`, or `inspect_capability` when those exact tools are visible.",
		"- Preserve semantic path references exactly as provided, including prefixes like `kb:` or `core:`. Do not rewrite, normalize, or paraphrase them.",
		"- Start with one broad entry point unless the request clearly spans domains. Prefer the currently loaded context before opening more doors.",
		"- If a needed tool is unavailable, use an available tool, activate a relevant capability, delegate with `thane_now` or `thane_assign` when visible, or answer directly.",
	}, "\n")
}

// IllegalToolMessage is the tool result content injected when the model
// calls a tool that is not available in the current context. The message
// pushes the model back toward the exact runtime contract instead of
// encouraging speculative delegation or invented tool names. It is a
// format string accepting the tool name as its single argument.
const IllegalToolMessage = "Error: tool %q is not available in this context. Use an available tool by its exact name. Do not invent tool names. For capability state, prefer activate_capability, deactivate_capability, reset_capabilities, list_loaded_capabilities, or inspect_capability when those exact tools are available in this turn. Otherwise choose another available tool or respond directly."

// TimeoutRecoverySystem is the system prompt for the recovery model
// when the primary model times out after completing tool calls.
const TimeoutRecoverySystem = "You are summarizing work completed by a previous assistant that timed out before it could respond. Provide a brief, helpful summary to the user."

// TimeoutRecoveryFallback is the user-facing message returned when
// the primary model times out but the recovery model is unavailable
// or also fails. It is a format string accepting the total tool call
// count and a comma-separated tool list as arguments.
const TimeoutRecoveryFallback = "I completed %d tool call(s) (%s) but the request timed out before I could compose a response. Please check the results or try again."

// TimeoutRecoveryEmpty is the user-facing message returned when the
// recovery model produces an empty response.
const TimeoutRecoveryEmpty = "The request timed out after completing tool calls. Please check the results."
