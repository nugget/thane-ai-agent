# Memory Architecture

## Philosophy

Memory in Thane isn't an afterthought — it's foundational. Unlike chat-only systems that bolt on RAG, Thane treats memory as a **first-class data store** that the agent actively queries and updates.

### Core Principles

1. **Memory is queryable, not just appendable** — The agent can ask "what do I know about the garage?" and get structured answers through semantic search.
2. **Multiple memory types serve different purposes** — Each with its own lifecycle and access patterns.
3. **SQLite is the truth** — All memory is persisted to SQLite. Survives restarts, migrations, updates. Queryable outside the agent if needed.
4. **Capture first, structure later** — Store raw data; let patterns emerge from use.

## Memory Types

### Semantic Facts

Persistent knowledge stored with embeddings for natural language recall:

- **Categories:** user, home, device, routine, preference, general
- **Tools:** `remember_fact`, `recall_fact`, `forget_fact`
- **Search:** Embedding-based similarity via Ollama (`nomic-embed-text` or similar)
- **Auto-extraction:** After each interaction, a classifier evaluates whether new facts should be stored. Same-value observations reinforce confidence; changed values trigger updates.

Facts are long-term memory. Tell Thane "the reading lamp is in the office" and it remembers across sessions, restarts, and model changes.

### Conversation Memory

Short-term context for the current interaction. Messages are stored with role, content, timestamp, and token count.

**Compaction:** When approaching context limits, older messages are summarized by the LLM into compressed form. Compaction preserves semantic content (decisions, facts, preferences) while reducing token count.

### Session Working Memory

A read/write scratchpad for the active session — emotional texture, conversational arc, things the agent wants to remember within a conversation but not permanently.

Working memory bridges the gap between ephemeral conversation context and permanent facts. It survives compaction within a session but doesn't persist across sessions.

### Session Archive

Complete, immutable transcripts of all conversations with full-text search:

- **Storage:** SQLite FTS5 for fast text search
- **Tool:** `archive_search` — search across all historical conversations
- **Import:** OpenClaw session import tool (migrates external conversation history)
- **Use:** "What did we discuss about MQTT last week?" → searches across all sessions

The archive is never modified after writing — it's a permanent record.

### Checkpoints

Full state snapshots for crash recovery, triggered by:
- Message count threshold (configurable, default 50)
- Graceful shutdown
- Manual request

Checkpoints capture conversations, facts, and metadata so the agent can resume exactly where it left off.

## Anticipations

Event-driven triggers stored in SQLite:

- **Created by:** `create_anticipation` tool — the agent decides what to watch for
- **Trigger conditions:** Entity patterns, state transitions, time windows
- **Resolution:** When a matching event arrives (via HA WebSocket or MQTT), the anticipation fires and triggers an agent loop run with context about what happened

Example: Thane notices you left home → creates an anticipation for your return → when person entity changes to `home`, fires a welcome-back response.

## Storage Layout

All memory lives in SQLite databases under `data_dir`:

```
~/Thane/data/
├── thane.db          # Conversations, messages, checkpoints
├── facts.db          # Semantic facts with embeddings
├── anticipations.db  # Event-driven triggers
└── archive.db        # Immutable session transcripts (FTS5)
```

## Integration with Agent Loop

1. **Before LLM call:** Load conversation history, query relevant facts, inject working memory, build enriched context
2. **After LLM response:** Store new messages, auto-extract facts if enabled, update working memory
3. **On events:** Match incoming events against anticipations, fire triggered agent runs
4. **Periodic:** Compact old conversations, checkpoint state

## Future Directions

- Confidence decay on stale facts (facts that haven't been reinforced lose confidence over time)
- Temporal queries ("what happened last night?") via HA history API
- Privacy controls per-conversation
- Cross-instance memory sharing
