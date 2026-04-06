# The Agent Loop

The agent loop is Thane's core reasoning cycle. Every request — whether from
a user typing in the dashboard, a Home Assistant voice command, an MQTT wake
subscription, or a scheduled task — enters the same loop.

## The Cycle

Each request runs through up to ten iterations:

### 1. Context Assembly

Gather everything the agent needs to reason about the request:

- **Conversation history** from the active session
- **Semantic facts** relevant to the topic (via embeddings search)
- **Contact context** for the sender (trust zone, relationship, last interaction)
- **Current conditions** (time, timezone, uptime, energy costs)
- **Talents** matched to active capability tags
- **Home state** (if the `ha` tag is active)
- **Working memory** (session scratchpad)
- **Token budget** (the agent sees its own context consumption)

Context assembly runs fresh each turn. The agent always works from current
state, not stale snapshots.

### 2. Tag Activation

Determine which capability tags are active. Tags control which tools and
talents the agent can see:

- **Always-active tags** (configured in `capability_tags` with `always_active: true`)
  provide core tools like memory, notifications, and session management
- **Channel-pinned tags** activate automatically based on the request source
  (email requests activate `email`, HA requests activate `ha`)
- **Agent-requested tags** activate when the agent calls `activate_capability`
  because it needs tools outside its current set

This creates a dynamic toolset that starts lean and expands on demand.

### 3. Planning

The LLM receives the assembled context and active tools. It decides:

- What information it needs (tool calls to make)
- What actions to take (services to call, messages to send)
- Whether to delegate tool-heavy work to a local model
- Whether it can respond directly

### 4. Tool Execution

Tool calls execute — in parallel where possible. Results feed back into
the next iteration. Tool calls can be:

- **Native tools** (80+ built-in: HA control, email, contacts, memory, files, etc.)
- **MCP tools** (bridged from external servers like ha-mcp)
- **Delegation** (spawning a local model to execute a multi-step task)

### 5. Response Shaping

When the agent has enough information — or hits the iteration limit — it
generates a final text response formatted for the requesting interface.

If the loop exhausts its ten iterations without producing a response, a
final LLM call with no tools available forces a text response.

## Capability Tags

Tags are the mechanism that keeps the agent loop efficient. Instead of
loading 80+ tools on every request, tags gate tools and talents by semantic
domain.

### Configuration

```yaml
capability_tags:
  ha:
    description: "Home Assistant device control and monitoring"
    tools: [control_device, find_entity, get_state, list_entities, call_service]
  email:
    description: "Email reading, sending, and management"
    tools: [email_list, email_read, email_search, email_send, email_reply]
  memory:
    description: "Fact storage and recall"
    tools: [remember_fact, recall_fact, forget_fact]
    always_active: true
```

### Delegation Pressure

When the orchestrator model starts with only ~15-20 tools (the always-active
set), it naturally reaches for `thane_delegate` when it encounters a request
that needs capabilities outside its active tags. This is delegation pressure
by architecture, not instruction — the model delegates because it literally
doesn't have the tools to do the work directly.

The orchestrator can also activate tags explicitly when it wants direct
access rather than delegation. The choice between "activate the tag and do
it myself" versus "delegate to a local model" is a judgment call the
orchestrator makes based on the complexity and importance of the task.

### Tag Persistence

Tags activated during a conversation persist for the duration of that
session. A request that starts by activating `email` doesn't need to
reactivate it on subsequent turns. Tags reset when the session closes.

## Iteration Budget

The loop runs a maximum of ten iterations per request. Each iteration is
one LLM call plus tool execution. This prevents runaway tool-call chains
while giving the agent enough room for multi-step reasoning.

The agent sees its token budget in the context, so it can make informed
decisions about when to checkpoint, delegate, or wrap up.

## Entry Points

The agent loop doesn't care where requests come from. All entry points
produce the same `Request` structure:

| Source | How It Enters |
|--------|---------------|
| Web dashboard / API | HTTP request to `/v1/chat/completions` |
| Home Assistant | Ollama-compatible API on port 11434 |
| Scheduled task | Scheduler fires, injects wake message |
| MQTT subscription | Topic message triggers agent with content |
| Email poll | New messages detected, agent wakes with email context |
| Signal message | Inbound Signal message routed to agent |

See [Event Sources](../reference/event-sources.md) for details on each.
