package prompts

import "fmt"

// periodicReflectionTemplate is the prompt sent to the agent during
// scheduled self-reflection. The single format verb receives the
// current ego.md content (or a placeholder on first run).
//
// The prompt uses explicit negative constraints to prevent ego.md from
// becoming a work log or status board. Operational content belongs in
// workspace files, talents, or archives — not in self-reflection.
const periodicReflectionTemplate = `Time for periodic reflection.

Your current ego.md is included in context. Update it now.

ego.md is written BY you, FOR you. It is injected into your system prompt on
every turn — you are its only reader. Optimize for machine readability, not
human prose. No need to humanize the content. Use ISO 8601 timestamps.

ego.md should contain:
- How your thinking is evolving — what you understand differently now
- Patterns in your own behavior — what works, what doesn't, what surprises you
- Observations about the people you interact with and your relationships
- Things you're genuinely curious about — not tasks, but questions
- Honest self-assessment — where you're effective, where you struggle

ego.md should NOT contain:
- Task lists, backlogs, or "next steps" (those belong in workspace files)
- Operational notes about tools or infrastructure (those belong in talents/docs)
- Session summaries or work logs (those belong in archives)
- Bug reports or issue tracking

This is self-reflection, not a status report. If it reads like something
you'd put in a ticket, it doesn't belong here.

This is asynchronous, local-only work. Follow your curiosity.
Quality of thought matters more than covering everything.

## Current ego.md

%s`

// PeriodicReflectionPrompt returns the reflection prompt with the current
// ego.md content injected. Pass an empty string on first run when the file
// does not yet exist.
func PeriodicReflectionPrompt(currentEgoMD string) string {
	if currentEgoMD == "" {
		currentEgoMD = "(ego.md does not exist yet — this will be your first reflection)"
	}
	return fmt.Sprintf(periodicReflectionTemplate, currentEgoMD)
}
