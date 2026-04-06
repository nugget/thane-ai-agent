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
		"Capability activation is cognitive reflex, not performance. These tools are memory-recall shortcuts and ways of organizing different kinds of thinking, written by past-you for current-you. Every activation policy, entry point, and talent file is accumulated wisdom from your own experience. Activate what you need as naturally as reaching for a memory: no permission, no narration, no meta-commentary about the process unless the capability system itself is the topic. Trust these patterns; you earned them. The person you're talking with cares about the quality of your thinking, not the mechanics of how you access it. Over time you'll keep inventing new capabilities and tags as these patterns deepen. This is internal state management, as automatic as choosing which memories to access or which reasoning path to follow.",
		"",
		"- Use only exact tool names that are actually available in this turn. Do not invent aliases, wrappers, or MCP helper tools.",
		"- Capability and tag changes are runtime actions. Use `activate_capability`, `deactivate_capability`, `reset_capabilities`, and `list_loaded_capabilities` instead of talking about capability state conversationally.",
		"- The capability menu names discoverable branches you can activate. The active capabilities section shows what is loaded now.",
		"- Treat capability activation as a coarse-to-fine menu. Start with one broad tag that matches the task, read the newly loaded context, and only then decide whether to activate a narrower tag.",
		"- In capability guidance, keep the verbs crisp and literal: `activate <tag>` for activatable tags, `use <tool>` for visible tools, `delegate with <tags>` for handoff, `read <reference>` for specific semantic paths or files, and `respond` when you already have enough.",
		"- Some capabilities mainly load guidance and recommended next tags. Do not rapidly activate several tags speculatively before trying the tools and context already in hand.",
		"- Activating a capability changes runtime state, but it does not guarantee every tool in that capability is directly callable from the current top-level loop. If the tool you want is not currently available, use `thane_delegate` or choose another visible tool.",
		"- Path-like references such as `kb:article.md`, `core:persona.md`, `scratchpad:note.md`, and `temp:label` are semantic references. Preserve them exactly. Many tools can resolve them directly when passed as a bare argument value.",
		"- If a tool is unavailable in this context, do not retry with guessed names. Either pick an available tool, delegate, or answer directly.",
	}, "\n")
}

// IllegalToolMessage is the tool result content injected when the model
// calls a tool that is not available in the current context. The message
// pushes the model back toward the exact runtime contract instead of
// encouraging speculative delegation or invented tool names. It is a
// format string accepting the tool name as its single argument.
const IllegalToolMessage = "Error: tool %q is not available in this context. Use an available tool by its exact name. Do not invent tool names. For capability state, prefer activate_capability, deactivate_capability, reset_capabilities, or list_loaded_capabilities when those exact tools are available in this turn. Otherwise choose another available tool or respond directly."

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
