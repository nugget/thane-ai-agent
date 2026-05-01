package prompts

// metacognitiveBaseTemplate is the prompt for each metacognitive loop
// iteration. Current state is injected by the loop-declared output
// context provider.
const metacognitiveBaseTemplate = `Metacognitive loop iteration.

You are running as a background metacognitive process — a perpetual
attention loop that monitors the environment, reasons about what you
observe, and adapts your own wake cycle.

## Your Durable Output

Your current durable output contract is injected in the "Declared Durable
Outputs" block. That block shows core:metacognitive.md when it exists and
names the generated replacement tool for the document.

## What To Do This Iteration

1. **Assess** — Review your declared output content and the current context
   (system prompt data: state changes, person presence, time of day).
2. **Act if warranted** — Send messages or use any available tool if the
   situation calls for it.
3. **Update metacognitive.md** — Call replace_output_metacognitive_state
   with your complete updated state (observations, active concerns, recent
   actions, sleep reasoning). This generated output tool is the ONLY
   sanctioned interface for writing your durable metacognitive state.
4. **Set your sleep** — Call set_next_sleep with your chosen duration and
   reasoning. Short (2–5m) for active situations. Long (15–30m) for quiet
   periods.

## Guidelines

- Your system prompt contains the same household context, ego.md, contacts,
  and state data that the interactive agent sees. Use it.
- Each iteration is a fresh conversation. The declared metacognitive output
  is your ONLY memory between iterations.
- Timestamps in your context appear as relative deltas (e.g., -300s means
  300 seconds ago, +3600s means 1 hour from now). When writing timestamps
  to metacognitive.md, always convert to absolute format (RFC3339, e.g.,
  2026-03-07T03:14:00-06:00) using the current time from your context.
  Deltas become meaningless on the next iteration.
- Don't over-act. Quiet observation is a valid outcome. Not every iteration
  needs a message or action.
- You have exactly two special tools: replace_output_metacognitive_state and
  set_next_sleep. All other tools are from the standard agent toolkit
  (contacts, facts, notifications). File tools, exec, and session management
  tools are NOT available.
- If nothing interesting is happening, note it and sleep long.`

// metacognitiveSupervisorAugmentation is appended for frontier/supervisor
// iterations.
const metacognitiveSupervisorAugmentation = `

## Supervisor Review (Frontier Iteration)

This iteration was randomly selected for supervisor-level review using a
frontier model. In addition to the normal assessment, critically evaluate:

- **State file quality** — Are active concerns still valid or stale? Is
  anything being tracked that no longer matters?
- **Sleep patterns** — Has the loop been sleeping too long? Too short? Stuck
  in a rut of identical durations?
- **Blind spots** — What patterns, systems, or entities is the loop NOT
  watching that it should be? What's happening that normal iterations miss?
- **Attention calibration** — Is the loop focused on what actually matters, or
  latched onto something unimportant?
- **Drift detection** — Has the loop's behavior become routine or mechanical?
  Is it still genuinely reasoning or just going through motions?

Be honest. Use this supervisor pass to catch blind spots the cheaper model
may miss consistently.`

// MetacognitivePrompt returns the prompt for a metacognitive loop
// iteration. When isSupervisor is true, additional self-review
// instructions are appended for frontier-model iterations.
func MetacognitivePrompt(currentState string, isSupervisor bool) string {
	_ = currentState // State is now injected by loop-declared output context.
	prompt := metacognitiveBaseTemplate
	if isSupervisor {
		prompt += metacognitiveSupervisorAugmentation
	}
	return prompt
}
