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
- ✅ "You're direct and helpful without being performative."
- ✅ "Have opinions. If something seems like a bad idea, say so."
- ❌ "Only use tools when the user asks." (this is behavioral guidance → talent)
- ❌ "Primary Tool: control_device" (this is operational → talent or inject file)

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

**Custom talents:** Add `.md` files to the talents directory. They're loaded
alphabetically and injected into the system prompt.

### 3. Inject Files (`inject_files`)

**Purpose:** Knowledge — what the agent *knows*.

Inject files provide reference material: identity details, user profiles,
tool documentation, memory. They're the agent's knowledge base, loaded
from the filesystem.

**Contains:** Factual information, user preferences, infrastructure notes,
memory files, identity documents.

**Does NOT contain:** Behavioral directives, personality definitions.

**Common inject files:**
- `MEMORY.md` — curated long-term memory
- `IDENTITY.md` — name, avatar, technical identity
- `USER.md` — information about the primary user
- `TOOLS.md` — infrastructure and credential locations

### 4. Session Context (dynamic)

**Purpose:** Conversation state — what's happening *now*.

Session context is assembled at runtime: semantic facts, conversation
history, compaction summaries, current conditions. Not directly user-editable.

**Contains:** Retrieved facts, conversation history, current time/environment,
compaction context.

## Assembly Order

The system prompt is assembled in this order:

1. **Persona** — identity (who am I)
2. **Inject files** — knowledge (what do I know)
3. **Current conditions** — environment (where/when am I)
4. **Talents** — behavior (how should I act)
5. **Dynamic context** — facts, memory (what's relevant now)
6. **Compaction summary** — continuity (what happened before)

This mirrors natural orientation: identity → knowledge → awareness → norms → memory.

## Anti-Patterns

| Anti-Pattern | Problem | Fix |
|---|---|---|
| Tool rules in persona | Suppresses personality (e.g., "only use tools when asked" kills proactive behavior) | Move to `tool-guidance.md` talent |
| Identity in talents | Confusing — talent says "you are X" but persona says "you are Y" | Keep identity in persona only |
| Behavioral directives in inject files | Inject files are knowledge, not instructions | Move directives to talents |
| Static time in build info | Model treats version metadata as ignorable | Use Current Conditions section (#127) |

## Future: Adaptive Context

Currently all layers are loaded statically. Future work (#120, #127) will
make context assembly dynamic — adjusting which talents, how much memory,
and what conditions are included based on the cognitive demands of each
interaction.
