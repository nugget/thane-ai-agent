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

A turn that uses stored context must successfully read its active conversation
history before recording incoming messages or calling the model. Failed queries,
unreadable rows, and canceled reads return errors so incomplete history cannot
silently become the next prompt. A successful read of a new conversation may be empty.

**Compaction:** When approaching context limits, older messages are
summarized by the LLM into compressed form. Compaction preserves semantic
content (decisions, facts, preferences) while reducing token count.
Its token counts, message selection, and prior summaries must all be readable
before summarization proceeds. Failed or canceled compaction leaves the source
rows intact. Automatic compaction runs after the reply with a five-minute
deadline; failures are logged for the operator.

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
- **Tools:** `search` — discover conversation evidence alongside indexed documents;
  `archive_search` — archive-specific discovery with time/conversation filters
  and surrounding context
- **Use:** "What did we discuss about MQTT last week?" searches across all sessions

Session transitions update lifecycle status and session ownership in one
transaction. Message content, IDs, timestamps, provenance, and tool-call
links stay intact; resetting a conversation clears its active context
while preserving its searchable history and channel binding.

Explicit discovery and automatic prompt context are separate. The general
`search` tool coordinates source-owned archive and document indexes; adding a
searchable document root does not inject its contents into a loop's prompt.
Document bodies participate only when the root opts into `context.search_body`.
Root visibility (`default`, `on_request`, `never`), audience restrictions, and
the calling run's available source tools still apply. Results preserve original
messages versus synthesized memory, document references, and search coverage.
Read promising source artifacts before treating a summary as evidence.

### Episodic Summaries

Post-session analysis that extracts key facts from conversations into the
semantic knowledge base. Enables long-term learning and pattern recognition
across sessions.

### Checkpoints

The `session_checkpoint` tool records a labeled bookmark of the session's
message IDs and active context in `session_checkpoints`. It leaves every
message in its current lifecycle state, so checkpointing does not empty
the next turn's context. Bookmarks survive later session transitions
because those transitions preserve the referenced messages.

Session bookmarks are separate from the broader state snapshots managed
by the checkpoint subsystem. A bookmark records a point in conversation
history; it does not restore agent state or undo external actions.

The broader snapshots are diagnostic projections created by manual API
requests, before model failover, and during graceful shutdown. They capture
conversation working windows, facts, and selected task fields. They omit
archived history, message provenance, full task payloads, and configuration;
shutdown snapshots follow session closure and can have empty working windows.
There is no automatic periodic snapshot hook. Snapshots can be inspected or
deleted, but `POST /v1/checkpoints/{id}/restore` returns **501 Not Implemented**.
Persistence across restarts comes from the underlying state stores.

## Session Management

Sessions are bounded conversations with explicit lifecycle controls:

- **session_close** — End the session with a carry-forward note for the
  next session. The carry-forward is the primary continuity mechanism
  across session boundaries.
- **session_checkpoint** — Record a labeled point without closing or
  changing the active conversation context.
- **session_split** — Close the early part of a session and retain the
  selected message and later messages in a successor session. Selectors
  apply only to the current session, and the retained portion must be
  after compacted history. Retained messages keep their original identity
  and provenance. Historical tool executions and model iterations remain
  attributed to their originating session, with their message links intact.

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

The archive borrows the working store's database connection. Session
transitions update the existing rows together; external imports insert into
those same tables. Import cleanup also runs in one transaction, so a failed
purge retains its messages, tool calls, sessions, and import tracking.

## Integration with the Agent Loop

1. **Before LLM call:** Load conversation history, query relevant facts,
   inject working memory, build enriched context
2. **After LLM response:** Store new messages, auto-extract facts if
   enabled, update working memory
3. **As context fills:** Compact old conversations

## Future Directions

- Confidence decay on stale facts
- Temporal queries via HA history API
- Privacy controls per-conversation
- Cross-instance memory sharing
