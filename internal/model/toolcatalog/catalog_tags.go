package toolcatalog

// builtinTagSpecs is the compiled-in tag catalog. PR-G formalized the
// hierarchy: every leaf declares its Parents (the menu trailheads it
// appears under), and coarse trailheads are explicitly marked
// Kind: TagKindMenu. Some leaves legitimately serve more than one
// menu (files spans development and knowledge; web spans development,
// knowledge, and media) and declare multi-valued Parents.
//
// Adding a new tag: pick a Kind, write a Description that fits the
// model-facing menu, and assign Parents from the existing menus when
// the tag clearly belongs under one. A new top-level menu is a bigger
// move — make sure the leaves grouped under it warrant a coarse
// trailhead before adding one.
var builtinTagSpecs = map[string]BuiltinTagSpec{
	// --- Menus (coarse trailheads) ---

	"development": {
		Description: "Coarse trailhead for software, repository, and code-change work. Usually leads to forge, files, web, or shell.",
		Kind:        TagKindMenu,
	},
	"home": {
		Description: "Coarse trailhead for home, device, room, and automation work. Usually leads to ha, awareness, or notifications.",
		Kind:        TagKindMenu,
	},
	"interactive": {
		Description: "Coarse trailhead for interactive loop behavior and channel guidance. Usually leads to signal, owu, or owner context.",
		Kind:        TagKindMenu,
	},
	"knowledge": {
		Description: "Coarse trailhead for kb articles, memory, dossiers, and document understanding. Usually leads to documents, memory, files, archive, or web.",
		Kind:        TagKindMenu,
	},
	"media": {
		Description: "Coarse trailhead for transcripts, feeds, and media analysis. Usually leads to feeds, attachments, or web.",
		Kind:        TagKindMenu,
	},
	"operations": {
		Description: "Coarse trailhead for routing, scheduling, logs, runtime state, and operational debugging. Usually leads to diagnostics, models, scheduler, loops, session, or companion.",
		Kind:        TagKindMenu,
	},
	"people": {
		Description: "Coarse trailhead for identity, relationship context, and communication channels. Usually leads to contacts, signal, email, or owner context.",
		Kind:        TagKindMenu,
	},

	// --- Leaves ---
	// Each leaf declares the menu(s) it appears under via Parents. Some
	// leaves are reachable from multiple menus — those carry multiple
	// entries in Parents.

	"archive": {
		Description: "What you've already heard. Full-text search and transcript retrieval across past conversations — the place to look when a phrase sounds like an inside joke, a name, or shorthand you should already understand.",
		Parents:     []string{"knowledge"},
	},
	"attachments": {
		Description: "Attachment listing, search, and vision description tools.",
		Parents:     []string{"media"},
	},
	"awareness": {
		Description: "Subscribe entities and live providers to a loop or conversation's auto-injected context. Reflexive for service loops, optional for single-shots.",
		Parents:     []string{"home"},
	},
	"companion": {
		Description: "Native macOS companion app integration tools (calendar, contacts, AppleScript bridges).",
		Parents:     []string{"operations"},
	},
	"contacts": {
		Description: "Structured contact-directory records and vCard administration tools.",
		Parents:     []string{"people"},
	},
	"diagnostics": {
		Description: "Logs, usage, version, and operational debugging tools.",
		Parents:     []string{"operations"},
	},
	"documents": {
		Description: "Curated documents you've authored or imported — KB articles, runbooks, persona/ego notes, indexed reference material under managed roots. NOT past conversation history; for things the user said or you've heard before, use `archive` or `memory` instead.",
		Parents:     []string{"knowledge"},
	},
	"email": {
		Description: "Email inbox reading, search, and sending tools.",
		Parents:     []string{"people"},
	},
	"feeds": {
		Description: "Media feed following and feed management tools.",
		Parents:     []string{"media"},
	},
	"files": {
		Description: "Workspace file read, write, edit, and search tools.",
		Parents:     []string{"development", "knowledge"},
	},
	"forge": {
		Description: "Forge and code-collaboration tools for issues, pull requests, checks, and reviews.",
		Parents:     []string{"development"},
	},
	"ha": {
		Description: "Home Assistant state, control, registry, and automation tools.",
		Parents:     []string{"home"},
	},
	"loops": {
		Description: "Live loop status, sleep control, notifications, ad hoc spawn, and durable loop-definition authoring tools.",
		Parents:     []string{"operations"},
	},
	"memory": {
		Description: "Things you've chosen to remember — durable facts you've stored about people, places, routines, and the user's vocabulary. The store you write to with `remember_fact` and read with `recall_fact`. For past *conversations* (what was said, when, by whom) use `archive` instead; these are sibling doors, not the same room.",
		Parents:     []string{"knowledge"},
	},
	"models": {
		Description: "Model registry inspection, routing, and policy tools.",
		Parents:     []string{"operations"},
	},
	"notifications": {
		Description: "Notification delivery, escalation, and actionable response tools.",
		Parents:     []string{"home"},
	},
	"owu": {
		Description: "Open WebUI interactive chat loop context and guidance.",
		Parents:     []string{"interactive"},
	},
	"scheduler": {
		Description: "Scheduling and task management tools.",
		Parents:     []string{"operations"},
	},
	"session": {
		Description: "Conversation/session lifecycle and checkpoint tools.",
		Parents:     []string{"operations"},
	},
	"shell": {
		Description: "Shell execution tools for local command work.",
		Parents:     []string{"development"},
	},
	"signal": {
		Description: "Signal messaging tools.",
		Parents:     []string{"interactive", "people"},
	},
	"web": {
		Description: "Web page retrieval and wider-web discovery tools.",
		Parents:     []string{"development", "knowledge", "media"},
	},

	// --- Protected leaves ---
	// Runtime-asserted, can't be model-toggled. Surface in the menu for
	// situational awareness, but they're not navigation trailheads.

	"message_channel": {
		Description: "Current message-app conversation affordances, such as reactions, normalized across Signal, Matrix, iMessage, and similar providers.",
		Protected:   true,
	},
	"owner": {
		Description: "Trusted owner/operator context set by runtime identity. When present, treat it as true; it can unlock owner-specific guidance and tools.",
		Protected:   true,
		Parents:     []string{"interactive", "people"},
	},
}
