# Memory

Memory in Thane isn't an afterthought — it's foundational. Unlike chat-only
systems that bolt on RAG, Thane treats memory as a **first-class data store**
that the agent actively queries and updates.

## Principles

1. **Memory is queryable, not just appendable.** The agent can ask "what do
   I know about the garage?" and get structured answers through semantic
   search.
2. **Multiple memory types serve different purposes.** Each with its own
   lifecycle and access patterns.
3. **SQLite is the truth.** All memory is persisted to SQLite. Survives
   restarts, migrations, updates. Queryable outside the agent if needed.
4. **Capture first, structure later.** Store raw data; let patterns emerge
   from use.

## Memory Types

### Semantic Facts

Persistent knowledge stored with embeddings for natural language recall.

- **Categories:** user, home, device, routine, preference, general
- **Tools:** `remember_fact`, `recall_fact`, `forget_fact`
- **Search:** Embedding-based similarity via Ollama (`nomic-embed-text` or similar)
- **Auto-extraction:** After each interaction, a classifier evaluates
  whether new facts should be stored. Same-value observations reinforce
  confidence; changed values trigger updates.

Facts are long-term memory. Tell Thane "the reading lamp is in the office"
and it remembers across sessions, restarts, and model changes.

### Conversation Memory

Short-term context for the current interaction. Messages are stored with
role, content, timestamp, and token count.

**Compaction:** When approaching context limits, older messages are
summarized by the LLM into compressed form. Compaction preserves semantic
content (decisions, facts, preferences) while reducing token count.

### Session Working Memory

A read/write scratchpad for the active session — emotional texture,
conversational arc, things the agent wants to remember within a conversation
but not permanently.

Working memory bridges the gap between ephemeral conversation context and
permanent facts. It survives compaction within a session but doesn't persist
across sessions.

### Session Archive

Complete, immutable transcripts of all conversations with full-text search.

- **Storage:** Unified `messages` table in thane.db with lifecycle `status`
  column (`active` -> `compacted` -> `archived`). FTS5 index for fast text
  search.
- **Tool:** `archive_search` — search across all historical conversations
- **Use:** "What did we discuss about MQTT last week?" searches across all sessions

Archived messages are never modified after writing — they're a permanent record.

### Episodic Summaries

Post-session analysis that extracts key facts from conversations into the
semantic knowledge base. Enables long-term learning and pattern recognition
across sessions.

### Checkpoints

Full state snapshots for crash recovery, triggered by:
- Message count threshold (configurable, default 50)
- Graceful shutdown
- Manual request (`session_checkpoint` tool)

Checkpoints capture conversations, facts, and metadata so the agent can
resume exactly where it left off.

## Session Management

Sessions are bounded conversations with explicit lifecycle controls:

- **session_close** — End the session with a carry-forward note for the
  next session. The carry-forward is the primary continuity mechanism
  across session boundaries.
- **session_checkpoint** — Save state without closing. Enables crash
  recovery without losing conversational flow.
- **session_split** — Fork the session. Post-split messages stay in
  memory for the new branch.

Context usage is injected into the system prompt so the agent can monitor
its own token consumption and make informed decisions about when to
checkpoint, close, or split.

## Storage Layout

All memory lives in SQLite databases under `data_dir`:

```
~/Thane/data/
  thane.db          # Conversations, messages, sessions, tool calls, checkpoints
  facts.db          # Semantic facts with embeddings
```

Active and archived messages share the `messages` table, differentiated by
a lifecycle `status` column. FTS5 triggers keep the full-text index in sync
automatically.

## Integration with the Agent Loop

1. **Before LLM call:** Load conversation history, query relevant facts,
   inject working memory, build enriched context
2. **After LLM response:** Store new messages, auto-extract facts if
   enabled, update working memory
3. **Periodic:** Compact old conversations, checkpoint state

## Future Directions

- Confidence decay on stale facts
- Temporal queries via HA history API
- Privacy controls per-conversation
- Cross-instance memory sharing
