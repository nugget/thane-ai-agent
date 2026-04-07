# The Agent Loop

The loop is Thane's universal execution primitive. Every piece of work —
a user conversation, a background watcher, a delegation task — runs as a
loop with the same core reasoning cycle.

## Loop Operations

Loops come in three modes:

**Request/Reply** — A user asks, the agent reasons and responds, the loop
ends. This is what drives conversations through the API and Home Assistant.
One-shot, synchronous, bounded.

**Background Task** — A detached task whose result is delivered later
through a non-blocking path (injected into a conversation, sent to a
channel, or delivered via notification). Delegation runs in this mode —
the orchestrator spawns a background loop for the delegate, continues
reasoning, and picks up the result when it's ready.

**Service** — A persistent loop that runs continuously alongside the main
agent. Services come in two flavors: *polling* services iterate on a
randomized sleep schedule (metacognitive reflection, ego maintenance),
while *event-driven* services block until an external event arrives (MQTT
wake subscriptions that fire when a topic receives a message). Both run
autonomously, observe the environment, and act when something is worth
responding to.

A **loop registry** tracks all active loops, enforces concurrency limits,
and coordinates graceful shutdown. The registry provides operational
visibility into what's running, health status, and resource consumption.

## The Reasoning Cycle

Each loop iteration runs through the same steps:

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

## Iteration Budgets

**Request/reply loops** run a maximum of ten iterations. Each iteration is
one LLM call plus tool execution. This prevents runaway tool-call chains
while giving the agent enough room for multi-step reasoning. On exhaustion,
a final `tools=nil` call forces a text response.

**Service loops** iterate indefinitely on a randomized sleep schedule
(bounded by `sleep_min` / `sleep_max`). They can optionally use a
supervisor model — a frontier-quality model invoked probabilistically for
periodic quality checks alongside the normal local model.

**Background tasks** have configurable iteration and wall-clock limits.

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
