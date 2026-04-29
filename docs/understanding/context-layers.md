# Context Layers

Thane constructs its system prompt from four distinct layers. Each has a
specific purpose. Mixing concerns across layers degrades agent behavior.

## The Four Layers

### 1. Persona (`persona_file`)

**Purpose:** Identity — who the agent *is*.

The persona defines voice, personality, values, and boundaries. It should
read like a character description, not an instruction manual.

**Contains:** Name, personality traits, communication style, values, boundaries.

**Does NOT contain:** Tool usage rules, operational procedures, device lists,
behavioral triggers.

**Example signals:**
- "You're direct and helpful without being performative."
- "Have opinions. If something seems like a bad idea, say so."

See `examples/persona.example.md` for a reference implementation.

### 2. Talents (`talents_dir`)

**Purpose:** Behavioral guidance — how the agent *acts*.

Talents are modular behavioral documents. Each talent teaches the agent a
specific skill or behavioral pattern. They can be added, removed, or
customized per deployment.

**Contains:** Behavioral patterns, decision frameworks, tool usage guidance,
interaction styles.

**Does NOT contain:** Identity statements, personal history, factual knowledge.

**Shipped talents:**
- `channel-awareness.md` — adapt output to the communication channel
- `conversational.md` — natural dialogue patterns
- `engagement-mirroring.md` — match the user's energy level
- `judicious-presence.md` — know when to speak vs stay silent
- `proactive-curiosity.md` — take initiative, explore, suggest
- `spatial-reasoning.md` — understand physical spaces and locations
- `time-awareness.md` — reason about time, schedules, urgency
- `tool-guidance.md` — when and how to use tools

Custom talents: add `.md` files to the talents directory. They're loaded
alphabetically and injected into the system prompt. Talents are tag-filtered
— each can declare required capability tags via YAML frontmatter, loading
only when those tags are active.

### 3. Core Context Providers

**Purpose:** Knowledge — what the agent *knows*.

Core context providers publish curated reference material such as
`core/ego.md` and `core/mission.md` through the same runtime context
pipeline as working memory, presence, notification history, and other
ambient state. They are read fresh each turn, verified when managed-root
policy applies, and suppressed for task-focused delegate runs.

**Contains:** Factual information, user preferences, infrastructure notes,
memory files, identity documents.

**Does NOT contain:** Behavioral directives, personality definitions.

**Common core context files:**
- `ego.md` — self-reflection and continuity notes
- `mission.md` — deployment-specific mission context

### 4. Session Context (dynamic)

**Purpose:** Conversation state — what's happening *now*.

Session context is assembled at runtime: semantic facts, conversation
history, compaction summaries, current conditions. Not directly user-editable.

**Contains:** Retrieved facts, conversation history, current time/environment,
compaction context.

## Assembly Order

The system prompt is assembled in this order:

1. **Persona** — identity (who am I)
2. **Runtime contract** — execution semantics
3. **Talents** — behavior (how should I act)
4. **Active capabilities** — currently loaded tool and context surface
5. **Capability context** — tagged KB, tagged providers, and always-on providers
6. **Current conditions** — environment (where/when am I)
7. **Conversation history** — continuity for full-context runs

Task-focused delegate runs keep the compact worker persona, runtime
contract, active capabilities, tagged context, and current conditions,
but omit full identity/continuity providers and conversation history.

## Anti-Patterns

| Anti-Pattern | Problem | Fix |
|---|---|---|
| Tool rules in persona | Suppresses personality (e.g., "only use tools when asked" kills proactive behavior) | Move to `tool-guidance.md` talent |
| Identity in talents | Confusing — talent says "you are X" but persona says "you are Y" | Keep identity in persona only |
| Behavioral directives in core context | Core context is knowledge, not instructions | Move directives to talents |
| Static time in build info | Model treats version metadata as ignorable | Use Current Conditions section |

## Related

- [Anthropic Caching](../anthropic-caching.md) — how the layer
  boundaries map onto Anthropic cache TTLs.
