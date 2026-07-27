package talents

// TagAlways and TagPersona are applicability tags: they answer "on which turns
// does this guidance apply", which is a different question from the capability
// tags that answer "what can the agent do right now".
//
// They are not required, and most talents carry neither. A talent tagged `web`
// is selected by a capability being active, which already says when it
// applies. These two exist for guidance that is not tied to any capability —
// the doctrine that shapes how the agent behaves rather than what it can
// reach — and they compose freely with capability tags on the same file.
//
// They replace an older encoding where a talent with no tags at all meant
// permanent guidance. Absence is a poor way to say "always": it cannot be
// distinguished from an oversight, it forced the loader to guess which files
// were talents at all by inspecting capitalization, and it left the rule
// invisible to anyone reading a file that carried no statement about itself.
const (
	// TagAlways marks guidance that applies to every turn, whatever shape
	// that turn takes. It is injected unconditionally, so a talent carrying
	// it reaches a worker loop fetching a webpage as readily as an
	// interactive reply.
	TagAlways = "always"

	// TagPersona marks guidance that only means something when the persona
	// is present — where the agent is being itself rather than executing a
	// procedure.
	//
	// It is not a synonym for "conversational". Drafting an email is not a
	// conversation but wants the voice; a burn-ban poller updating a
	// document is neither. What unites the turns this covers is that the
	// persona is in play, which is why the tag follows the persona rather
	// than trying to enumerate the situations that want one.
	TagPersona = "persona"
)
