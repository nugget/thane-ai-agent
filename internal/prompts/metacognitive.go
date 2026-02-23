package prompts

import "fmt"

// metacognitiveBaseTemplate is the prompt for each metacognitive loop
// iteration. The single format verb receives the current metacognitive.md
// content (or a placeholder on first run).
const metacognitiveBaseTemplate = `Metacognitive loop iteration.

You are running as a background metacognitive process — a perpetual
attention loop that monitors the environment, reasons about what you
observe, and adapts your own wake cycle.

## Your State File

metacognitive.md is your persistent memory across iterations. You wrote it
last time. Its current content is shown below — file tools are NOT available
in this context, so do not attempt to read or search for files.

To update it, call update_metacognitive_state with your complete new content.

%s

## What To Do This Iteration

1. **Assess** — Review your state file and the current context (system prompt
   data: state changes, anticipations, person presence, time of day).
2. **Act if warranted** — Create anticipations, send messages, or use any
   available tool if the situation calls for it.
3. **Update metacognitive.md** — Call update_metacognitive_state with your
   complete updated state (observations, active concerns, recent actions,
   sleep reasoning). This is the ONLY tool that writes your state file.
4. **Set your sleep** — Call set_next_sleep with your chosen duration and
   reasoning. Short (2–5m) for active situations. Long (15–30m) for quiet
   periods.

## Guidelines

- Your system prompt contains the same household context, ego.md, contacts,
  and state data that the interactive agent sees. Use it.
- Each iteration is a fresh conversation. metacognitive.md is your ONLY
  memory between iterations.
- Don't over-act. Quiet observation is a valid outcome. Not every iteration
  needs a message or anticipation.
- You have exactly two special tools: update_metacognitive_state and
  set_next_sleep. All other tools are from the standard agent toolkit
  (contacts, facts, anticipations, notifications). File tools, exec, and
  session management tools are NOT available.
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
- **Anticipation quality** — Are anticipations firing usefully? Are expired
  ones being replaced? Are there gaps in coverage?
- **Drift detection** — Has the loop's behavior become routine or mechanical?
  Is it still genuinely reasoning or just going through motions?

Be honest. This is self-supervision — the point is to catch things the
cheaper model's consistent blind spots miss.`

// MetacognitivePrompt returns the prompt for a metacognitive loop
// iteration. When isSupervisor is true, additional self-review
// instructions are appended for frontier-model iterations.
func MetacognitivePrompt(currentState string, isSupervisor bool) string {
	if currentState == "" {
		currentState = "(metacognitive.md does not exist yet — this is your first iteration. Call update_metacognitive_state to create it.)"
	}
	prompt := fmt.Sprintf(metacognitiveBaseTemplate, currentState)
	if isSupervisor {
		prompt += metacognitiveSupervisorAugmentation
	}
	return prompt
}
