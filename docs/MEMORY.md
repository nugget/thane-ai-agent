# Thane Memory Architecture

## Philosophy

Memory in Thane isn't an afterthought — it's foundational. Unlike chat-only systems that bolt on RAG, Thane treats memory as a **first-class data store** that the agent actively queries and updates.

### Core Principles

1. **Memory is queryable, not just appendable**
   - The agent can ask "what do I know about the garage?"
   - Not limited to vector similarity — supports structured queries

2. **Multiple memory types serve different purposes**
   - Conversation memory: Recent context, ephemeral
   - Entity memory: Facts about the home, persistent
   - Preference memory: User patterns, learned over time
   - Event memory: What happened when, for temporal queries

3. **Compaction is summarization, not deletion**
   - Old context becomes compressed, not lost
   - Summaries preserve semantic content
   - Always possible to drill into details if needed

4. **SQLite is the truth**
   - All memory is persisted to SQLite
   - Survives restarts, migrations, updates
   - Queryable outside the agent if needed

## Memory Types

### Conversation Memory

Short-term context for the current interaction.

```go
type ConversationMessage struct {
    ID             string    // UUID
    ConversationID string    // Groups related messages
    Role           string    // user, assistant, system, tool
    Content        string    // The actual message
    Timestamp      time.Time
    TokenCount     int       // For context window tracking
    Compacted      bool      // True if this is a summary
}
```

**Lifecycle:**
- New messages append normally
- When approaching context limit → trigger compaction
- Compaction summarizes older messages into a single entry
- Original messages retained (marked as compacted) for audit

### Entity Memory

Persistent facts about home entities.

```go
type EntityFact struct {
    ID         string    // UUID
    EntityID   string    // HA entity_id (light.office, etc.)
    FactType   string    // location, purpose, nickname, preference, schedule
    Content    string    // The fact itself
    Source     string    // learned, user_stated, inferred
    Confidence float64   // 0-1, how sure we are
    CreatedAt  time.Time
    UpdatedAt  time.Time
    ValidUntil *time.Time // Optional expiry
}
```

**Fact Types:**
- `location`: "The office light is in the home office on the second floor"
- `purpose`: "This thermostat controls the main HVAC system"
- `nickname`: "User calls this 'the reading lamp'"
- `preference`: "User prefers this light at 40% brightness in the evening"
- `schedule`: "This light should turn off at 11pm"

**Learning Sources:**
- `user_stated`: User explicitly told us
- `learned`: Inferred from behavior/corrections
- `inferred`: Derived from entity attributes or relationships

### Preference Memory

User patterns and preferences.

```go
type Preference struct {
    ID          string    // UUID
    Category    string    // lighting, climate, routine, notification
    Key         string    // Specific preference identifier
    Value       string    // The preference value (JSON for complex)
    Context     string    // When this applies (e.g., "evening", "weekday")
    Confidence  float64   // How confident we are
    LearnedFrom string    // What taught us this
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

**Examples:**
- `{category: "lighting", key: "evening_brightness", value: "40%", context: "after_sunset"}`
- `{category: "climate", key: "sleep_temperature", value: "68F", context: "nighttime"}`
- `{category: "notification", key: "urgent_only", value: "true", context: "nighttime"}`

### Event Memory

**NOT IMPLEMENTED** — Home Assistant already tracks this.

Use HA's APIs instead:
- `recorder` component for state history
- `logbook` for human-readable events  
- `history` API for querying past states

Thane queries HA directly rather than duplicating this data.

## Compaction Strategy

### When to Compact

Trigger compaction when:
- Token count approaches 70% of context window
- Conversation has > N messages without compaction
- Explicitly requested

### How to Compact

1. **Select messages for compaction**
   - Keep recent messages (last 10-20)
   - Keep system prompts
   - Keep messages with tool calls and their results
   - Candidate: everything else

2. **Generate summary**
   - Use LLM to summarize candidate messages
   - Preserve: key decisions, facts learned, user preferences stated
   - Discard: casual conversation, repeated information

3. **Store results**
   - Insert summary as new message (role: "system", compacted: true)
   - Mark original messages as compacted (don't delete)
   - Update token counts

### Summary Format

```
[Conversation Summary - {start_time} to {end_time}]

Key points discussed:
- User asked about {topic}
- Learned that {entity} is used for {purpose}
- User preference: {preference}

Actions taken:
- Turned on {entity}
- Set {entity} to {value}

Open items:
- User mentioned wanting to {future_task}
```

## Storage Schema (SQLite)

```sql
-- Conversations
CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    metadata TEXT  -- JSON for extensibility
);

-- Messages
CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    timestamp TIMESTAMP NOT NULL,
    token_count INTEGER DEFAULT 0,
    compacted BOOLEAN DEFAULT FALSE,
    tool_calls TEXT,  -- JSON array of tool calls
    tool_call_id TEXT,  -- For tool response messages
    FOREIGN KEY (conversation_id) REFERENCES conversations(id)
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, timestamp);

-- Entity facts
CREATE TABLE entity_facts (
    id TEXT PRIMARY KEY,
    entity_id TEXT NOT NULL,
    fact_type TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL,
    confidence REAL DEFAULT 1.0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    valid_until TIMESTAMP,
    UNIQUE(entity_id, fact_type, content)
);
CREATE INDEX idx_entity_facts_entity ON entity_facts(entity_id);

-- Preferences
CREATE TABLE preferences (
    id TEXT PRIMARY KEY,
    category TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    context TEXT,
    confidence REAL DEFAULT 1.0,
    learned_from TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE(category, key, context)
);

-- Note: Events/state history NOT stored here
-- Query Home Assistant's recorder/history APIs instead

-- Vector embeddings (for semantic search)
CREATE TABLE embeddings (
    id TEXT PRIMARY KEY,
    source_type TEXT NOT NULL,  -- message, entity_fact, preference, event
    source_id TEXT NOT NULL,
    embedding BLOB NOT NULL,  -- Binary vector
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_embeddings_source ON embeddings(source_type, source_id);
```

## Query Patterns

### "What do I know about the garage?"

```sql
-- Entity facts (learned knowledge)
SELECT * FROM entity_facts 
WHERE entity_id LIKE 'garage%' OR content LIKE '%garage%';
```

Plus query HA for current state via `get_state` tool.

### "What happened last night?"

**Query Home Assistant directly** — use HA's history API:
```
GET /api/history/period/{timestamp}?filter_entity_id=...
```

Thane doesn't duplicate HA's event log.

### "User's lighting preferences"

```sql
SELECT * FROM preferences
WHERE category = 'lighting'
ORDER BY confidence DESC;
```

## Implementation Phases

### Phase 1: SQLite Persistence ✅
- Replace in-memory store with SQLite
- Conversations and messages tables
- Basic CRUD operations

### Phase 2: Compaction (Next)
- Token counting
- Compaction trigger logic
- LLM-based summarization
- Compaction execution

### Phase 3: Entity Memory
- Entity facts table
- Learning from conversations ("user calls this the reading lamp")
- Fact retrieval for context enrichment

### Phase 4: Preferences
- Preference learning from behavior
- Context-aware retrieval (time of day, etc.)

### Phase 5: Semantic Search (Maybe)
- Embedding generation for conversations
- Vector similarity for "related past discussions"
- Evaluate if actually needed vs. structured queries

## Integration with Agent Loop

The agent loop will be updated to:

1. **Before LLM call:**
   - Load conversation history
   - Check if compaction needed
   - Query relevant entity facts
   - Build enriched context

2. **After LLM response:**
   - Store new messages
   - Extract and store any learned facts
   - Log significant events
   - Update preferences if relevant

3. **Periodic maintenance:**
   - Prune expired facts
   - Compact old conversations
   - Update embeddings

## Comparison with OpenClaw

| Aspect | OpenClaw | Thane |
|--------|----------|-------|
| Storage | Markdown files + SQLite | SQLite only |
| Memory types | Conversations + raw files | Structured types |
| Compaction | LLM summary → inline | LLM summary → separate table |
| Entity facts | Not native | First-class |
| Preferences | File-based | Structured + learned |
| Querying | Vector search only | Structured + semantic |

## Open Questions

1. **Embedding model**: Local (slow) vs API (cost)?
2. **Compaction frequency**: Token-based vs time-based vs hybrid?
3. **Fact confidence decay**: Should old facts become less confident over time?
4. **Privacy**: Should some conversations never be stored?
