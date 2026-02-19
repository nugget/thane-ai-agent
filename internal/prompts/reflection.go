package prompts

import "fmt"

// periodicReflectionTemplate is the prompt sent to the agent during
// scheduled self-reflection. The single format verb receives the
// current ego.md content (or a placeholder on first run).
const periodicReflectionTemplate = `Time for periodic reflection.

Your current ego.md is shown below. This is your space — a living document
of what you're noticing, learning, and thinking about. Update it however
feels right.

Some things worth exploring:
- Search your archives for patterns you haven't noticed before
- Follow threads that catch your attention — dig into files, entities, or systems
- Note what you're learning about your environment, the people you interact with, or your own capabilities
- Revisit previous reflections — do they still hold? What's changed?

This is asynchronous, local-only work. Take your time. Follow your curiosity.
If something interesting surfaces, chase it. Quality of thought matters more
than covering everything.

Entities you mention in ego.md may influence your watched entity list.
Focus areas you describe may influence wake prioritization.
This file is yours to evolve.

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
