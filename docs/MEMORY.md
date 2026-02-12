# Thane Memory Architecture

## Philosophy

Memory in Thane isn't an afterthought — it's foundational. Unlike chat-only systems that bolt on RAG, Thane treats memory as a **first-class data store** that the agent actively queries and updates.

### Core Principles

1. **Memory is queryable, not just appendable** — The agent can ask "what do I know about the garage?" and get structured answers through semantic search.

2. **Multiple memory types serve different purposes** — Conversation context (ephemeral), semantic facts (persistent), checkpoints (crash recovery).

3. **Compaction is summarization, not deletion** — Old context becomes compressed, not lost. Summaries preserve semantic content.

4. **SQLite is the truth** — All memory is persisted to SQLite. Survives restarts, migrations, updates. Queryable outside the agent if needed.

## Memory Types

### Conversation Memory

Short-term context for the current interaction. Messages are stored with role, content, timestamp, and token count. When approaching context limits, compaction summarizes older messages into a compressed form.

**Lifecycle:**
- New messages append normally
- When approaching context limit → trigger compaction
- Compaction uses the LLM to summarize older messages
- Checkpoint/restore preserves full state across restarts

### Semantic Facts

Persistent knowledge stored with embeddings for natural language recall:

- **Categories:** user, home, device, routine, preference, general
- **Operations:** `remember_fact`, `recall_fact`, `forget_fact` tools
- **Search:** Embedding-based similarity search via Ollama (`nomic-embed-text` or similar)

Facts are the agent's long-term memory. Tell it "the reading lamp is in the office" and it remembers across sessions.

### Checkpoints

Full state snapshots triggered by:
- Message count threshold (configurable, default 50)
- Graceful shutdown
- Manual request

Checkpoints capture conversations, facts, and metadata for crash recovery.

## Compaction Strategy

### When to Compact

- Token count approaches context window limit
- Triggered automatically by the checkpointer
- Configurable thresholds

### How to Compact

1. Keep recent messages and system prompts
2. Use LLM to summarize older messages
3. Preserve: key decisions, facts learned, user preferences, tool results
4. Store summary as compressed context

## Storage

All memory lives in SQLite databases under `data_dir`:

- `thane.db` — Conversations, messages, checkpoints
- `facts.db` — Semantic facts with embeddings
- `anticipations.db` — Scheduled triggers and event-based actions

## Integration with Agent Loop

1. **Before LLM call:** Load conversation history, query relevant facts, build enriched context
2. **After LLM response:** Store new messages, extract and store any learned facts
3. **Periodic maintenance:** Compact old conversations, checkpoint state

## Future Directions

- Preference learning from behavioral patterns
- Temporal queries ("what happened last night?") via HA history API
- Confidence decay on stale facts
- Privacy controls per-conversation
