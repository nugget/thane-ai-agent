# Context Layer Architecture

Thane assembles its system prompt from multiple context sources, each with a
distinct semantic role. Understanding these roles prevents common mistakes like
putting tool rules in persona files or identity content in talents.

## The Four Layers

### 1. Persona (`persona.md`)

**Purpose:** Identity, voice, values. Who the agent *is*.

**Contains:**
- Name, personality, communication style
- Core values and boundaries
- Relationship to the user
- Emotional tone and vibe

**Does NOT contain:**
- Tool usage instructions
- Operational rules
- API details or infrastructure notes

**Who edits:** The agent (with human approval). This is the agent's self-concept.

**Loaded:** Always, first in the system prompt.

### 2. Talents (`talents/*.md`)

**Purpose:** Behavioral guidance. How the agent *acts*. Modular and swappable.

**Contains:**
- When and how to use tools (`tool-guidance.md`)
- Conversational patterns (`conversational.md`, `engagement-mirroring.md`)
- Domain knowledge and strategies (`spatial-reasoning.md`, `time-awareness.md`)
- Memory and continuity practices (`shared-memory.md`)

**Does NOT contain:**
- Identity or personality (removing a talent shouldn't change who the agent is)
- User-specific data
- Credentials or infrastructure details

**Who edits:** Developer or user. Talents are designed to be shared, composed, and
swapped without affecting identity.

**Loaded:** Always, after persona. All `.md` files in `talents_dir` are concatenated
in alphabetical order.

### 3. Inject Files (`context.inject_files`)

**Purpose:** Knowledge and memory. What the agent *knows*. External reference
material, often shared across runtimes.

**Typical files:**
- `MEMORY.md` — Long-term curated memory
- `USER.md` — Information about the human
- `IDENTITY.md` — Technical identity details
- `TOOLS.md` — Infrastructure notes, API details, credentials locations

**Does NOT contain:**
- Behavioral directives (use talents for that)
- System prompt framing (use persona for that)

**Who edits:** Various. MEMORY.md is agent-curated. USER.md is user-maintained.
TOOLS.md accumulates from both.

**Loaded:** Always, after talents. Each file is injected as-is.

### 4. Session Context

**Purpose:** What's happening *now*. The active conversation.

**Contains:**
- Conversation history (user messages, agent responses, tool calls)
- Working memory (when implemented — see #120)
- Compaction summaries (when context is truncated)

**Managed by:** The runtime. Compaction is automatic; working memory will be
agent-authored.

## Key Principles

### Each Layer Has One Job

Mixing concerns creates conflicts. When `persona.md` contains "only use tools
when asked," it contradicts a `proactive-curiosity.md` talent. The persona says
"don't," the talent says "do," and the model is caught between them.

**Rule:** If you're unsure where something belongs, ask: "Is this about who I am,
how I behave, what I know, or what's happening now?"

### Persona Is Not Operational

The most common anti-pattern is stuffing tool rules into the persona file. This
happens because persona.md is the most visible file and feels like "the config."
Resist this. Tool rules are behavioral guidance — they belong in talents.

### Talents Are Composable

A talent should work independently. Adding `proactive-curiosity.md` shouldn't
require also adding `tool-guidance.md` (though they complement each other). Removing
a talent should change behavior, not break identity.

### Inject Files Are Reference Material

Think of inject files as documents on the agent's desk. They inform decisions but
don't direct behavior. The agent reads MEMORY.md to know what happened yesterday,
not to be told how to act today.

## Future: Adaptive Context (Design Phase)

The current system loads all layers for every interaction. Future work will make
context assembly adaptive — selecting the right depth and breadth for each task:

- **Expectations hints** signal what kind of thinking a task requires
- **Context constructor** assembles the prompt based on those hints
- **Pre-compiled summaries** at multiple granularity levels (full, recent, minimal)
  provide right-sized context without runtime summarization

See issues #119, #120, #102, #22 for related design work.
