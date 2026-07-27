package agent

import "github.com/nugget/thane-ai-agent/internal/model/talents"

// talentTags is the tag set a talent's applicability is judged against: the
// capabilities active on this turn, plus the two tags that describe the turn
// itself.
//
// [talents.TagAlways] is added unconditionally — that is what it means — and
// [talents.TagPersona] only when the persona is actually being rendered. The
// second is derived rather than declared so the two cannot disagree: a turn
// carrying persona-dependent guidance while the persona itself was swapped out
// would give the model prose written for a voice that is not there.
//
// Deriving it also keeps this out of the way of the larger question. Which
// identity a turn wears is currently decided by prompt mode, in a branch that
// has outgrown its origin (#1281). Making the tag authoritative over that
// branch would promote it from a patch to structure, so for now the tag
// follows the decision rather than making it.
//
// The returned map is a copy: the caller's snapshot describes what the agent
// can do, and these two describe what kind of turn it is. Mixing them into the
// shared set would report them as activatable capabilities to everything else
// that reads it.
func talentTags(active map[string]bool, taskPrompt bool) map[string]bool {
	out := make(map[string]bool, len(active)+2)
	for tag, on := range active {
		out[tag] = on
	}
	out[talents.TagAlways] = true
	if !taskPrompt {
		out[talents.TagPersona] = true
	}
	return out
}
