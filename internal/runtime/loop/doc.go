// Package loop implements persistent goroutine-based delegate loops —
// lightweight autonomous observers that run continuously alongside the
// main agent, with direct completion delivery or ordinary tool use for
// any durable artifacts they need to maintain.
//
// Loops are the universal primitive replacing previously-separate
// systems (metacognitive loop, observers) with a single
// [Registry.SpawnLoop] abstraction. Each loop is a background goroutine that
// iterates on a randomized bounded sleep schedule, running LLM iterations
// via the agent runner and optionally delivering results back through a
// completion path.
//
// A [Registry] tracks all active loops and provides visibility into what
// is running, their health, and resource usage. It enforces concurrency
// limits and coordinates graceful shutdown.
//
// See issue #509 for the full design.
//
// # Turn construction lifecycle
//
// Model-facing loop work is built in two phases so wake-specific code
// can prepare useful context without owning model execution. First,
// a wake is converted into an [AgentTurn]. Then the loop applies its
// common request environment and runs the agent.
//
//	WaitFunc / timer wake
//	     │
//	     ▼
//	TurnInput                 event payload, supervisor flag, notify envelopes
//	     │
//	     ├─ Config.TurnBuilder    custom request-producing wake logic
//	     │
//	     └─ Config.Task /
//	        Config.TaskBuilder    prompt-only convenience path
//	     ▼
//	AgentTurn                prepared Request plus compact snapshot summary
//	     │
//	     │  prepareAgentTurnRequest
//	     ▼
//	Request                  loop defaults, launch overrides, progress,
//	                         tools, tags, fallback content
//	     │
//	     │  runAgentTurn
//	     ▼
//	Runner.Run              model execution, response capture, telemetry
//
// [Config.Handler] is intentionally outside this chain. Use it for
// infrastructure work that does not need a model turn; use
// [Config.TurnBuilder] when the wake should result in agent execution.
//
// # Capability tag lifecycle
//
// Capability tags scope the tool surface, KB articles, talents, and
// other context a loop iteration sees. The same conceptual value
// flows through several fields at different stages of a loop's life.
// They are not redundant — each represents a distinct point on the
// lifecycle — but in isolation any one of them looks like it does
// the same job as the others. This map exists so per-field Godoc can
// stay short and reference here rather than re-explaining the chain.
//
//	Spec.Tags                    declarative, persisted with the spec
//	     │
//	     │  Spec.profileRequest()
//	     ▼
//	requestBase.InitialTags      base iteration tags
//	     │
//	     │  + Launch.InitialTags    per-invocation runtime override
//	     │  + activatedTags         carried from prior iterations'
//	     │                          Response.ActiveTags
//	     ▼
//	loop.Request.InitialTags     merged set per iteration
//	     │
//	     │  loopAdapter translates to agent.Request
//	     ▼
//	agent.Request.InitialTags    seed for the capability scope
//	     │
//	     │  scope.Request(tag) per tag
//	     ▼
//	scope.Snapshot()             active tags during the run
//	     │
//	     │  end-of-run capture
//	     ▼
//	agent.Response.ActiveTags    fed back into activatedTags
//
// Each layer in plain English:
//
//   - [Spec.Tags] is the spec-level declaration. Operators editing a
//     loop definition set this field; it persists with the spec.
//     Activated at iteration 0 of any run from this spec, and
//     remains active through the loop's lifetime unless the model
//     deactivates a tag mid-run.
//
//   - [Launch.InitialTags] is a per-invocation runtime override.
//     Launchers that scope a particular run differently from the
//     spec — the delegate executor, MQTT wake dispatch, programmatic
//     spawners — set it here. Does not persist with the spec.
//
//   - [Request.InitialTags] (this package's [Request]) is the merged
//     set the loop hands to its runner each iteration. The merge
//     happens in the loop's request-build path and combines
//     requestBase.InitialTags, requestOverride.InitialTags, and the
//     loop's running activatedTags state.
//
//   - agent.Request.InitialTags receives the merged set across the
//     loop/agent boundary and becomes the capability scope's seed.
//     Lives in the [agent] package, not here.
//
//   - agent.Request.RuntimeTags is distinct from InitialTags despite
//     the similar shape. RuntimeTags are trusted runtime-asserted
//     tags the model cannot deactivate within the run; they're
//     pinned via scope.PinChannelTags rather than scope.Request.
//     Set by trusted callers (the Signal bridge pins
//     "message_channel"). Use when a tag must remain active
//     regardless of model behavior.
//
//   - agent.Response.ActiveTags is the end-of-run snapshot. The loop
//     captures it into activatedTags for the next iteration's merge,
//     which is how a tag activated mid-run by the model survives
//     into iteration N+1.
//
//   - [Request.SkipTagFilter] (and the matching agent.Request field)
//     bypass the scope filter entirely. Used by self-scoping
//     contexts like the metacognitive loop. Empty Spec.Tags + a
//     SkipTagFilter request together signal "this loop does not
//     participate in tag filtering."
//
// When adding or modifying tag-related fields, anchor your work
// against this map rather than re-explaining the chain in each
// field's Godoc. If you find yourself writing "but if FieldB is also
// set, this is ignored" or "see FieldC for the related case" in a
// field comment, that's a sign the field belongs in this lifecycle
// document instead.
package loop
