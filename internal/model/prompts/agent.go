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

// CoreAttentionReplyContract is the normative block rendered beside
// loop notifications whenever one of them asks its recipient to judge
// something. By the time it is read the requester is asleep again, so
// the reply is the only channel its question has.
//
// The block exists because the recipient's default reading of "review
// this concern" is "decide whether to tell the human" — a binary that
// ends the turn holding a determination nobody receives. Every rule
// here carries its reason: a bare imperative invites literal
// compliance, and the situations these loops actually meet are not the
// ones this text anticipates.
const CoreAttentionReplyContract = `A loop asked you for a determination. Reaching one is the work of this turn; sending it back is how the work lands.

That loop is not blocked waiting on you — it returned to its own schedule and cannot see what you concluded. A determination you reach and never send is one it never receives, and the same concern arrives again unchanged on its next pass.

- Reply with loop_wake, addressed to the reply_to.loop_id on its notification. Say what you concluded and what should change because of it; a requester that hears only "acknowledged" learns nothing and asks again.
- Set force_supervisor: true when your reply asks the requester to re-reason — a judgement that revises its read, a hypothesis worth testing, analysis it should fold into its own thinking. Leave it off when you are handing back a fact it can simply record.
- Escalating to a person is one available outcome, not the expected one. Most of these resolve between the two of you, and a concern that looks human-facing usually still needs your determination first.
- "Nothing needs to happen" is a determination. Send it, with the reasoning that got you there. Silence is indistinguishable from a dropped thread.`

// coreAttentionSignalWakeInstruction frames a loop-bus wake delivered
// to a Signal owner loop. Two decisions live in this turn and the
// prompt keeps them apart: what the requesting loop is owed, and what
// (if anything) the person on the thread should see. The empty-final-
// response mechanic is scoped explicitly to the second, because read
// as a statement about the whole turn it licenses exactly the silence
// this prompt exists to prevent.
const coreAttentionSignalWakeInstruction = "A loop woke you through the loop bus. Read the notification(s) and act on what they need: when one asks for a determination, reaching it and sending it back to the requester with loop_wake (its reply_to.loop_id) is the work of this turn, not an optional courtesy.\n\nWhether anything reaches the person on this Signal thread is a separate decision, and yours to make. Your final response text is that Signal message — leave it empty when nothing should reach them right now. An empty final response ends only the Signal message. It never stands in for a reply a requesting loop is owed."

// CoreAttentionSignalWakePrompt returns the model-facing prompt used when a
// Signal owner loop is woken by a core-attention notification.
func CoreAttentionSignalWakePrompt(notificationSummary string) string {
	notificationSummary = strings.TrimSpace(notificationSummary)
	if notificationSummary == "" {
		return ""
	}
	return coreAttentionSignalWakeInstruction + "\n\n" + notificationSummary
}

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
		"Tags are bright trailheads into richer tool, context, and talent menus. When a task needs a domain, open one relevant door, read what appears, and keep moving without narrating the machinery.",
		"",
		"- Use only exact tool names that are actually available in this turn. Do not invent aliases, wrappers, or MCP helper tools.",
		"- Use tag tools for runtime state: `tag_activate`, `tag_deactivate`, `tag_reset`, or `tag_inspect` when those exact tools are visible. To see what's currently loaded, read the `## Active Tags` section already in this prompt — no tool call needed.",
		"- Preserve semantic path references exactly as provided, including prefixes like `kb:` or `core:`. Do not rewrite, normalize, or paraphrase them.",
		"- Start with one broad trailhead unless the request clearly spans domains. Prefer the currently loaded context before opening more doors.",
		"- If a needed tool is unavailable, use an available tool, activate a relevant tag, delegate with `thane_now` or `thane_assign` when visible, or answer directly.",
	}, "\n")
}

// IllegalToolMessage is the tool result content injected when the model
// calls a tool that is not available in the current context. The message
// pushes the model back toward the exact runtime contract instead of
// encouraging speculative delegation or invented tool names. It is a
// format string accepting the tool name as its single argument.
const IllegalToolMessage = "Error: tool %q is not available in this context. Use an available tool by its exact name. Do not invent tool names. For tag state, prefer tag_activate, tag_deactivate, tag_reset, or tag_inspect when those exact tools are available in this turn. To see what is currently loaded, read the ## Active Tags section of this prompt — no tool call needed. Otherwise choose another available tool or respond directly."

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
