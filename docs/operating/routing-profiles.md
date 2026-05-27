# Virtual Models

Thane uses **virtual model names** to adapt its behavior based on the
nature of each interaction. Names like `thane:latest`, `thane:premium`,
or `thane:assist` are selected by setting the model field in any
Ollama-compatible client (Open WebUI, HA Assist, API calls). The
canonical Go shape is `router.VirtualModel`; older docs may still call
these "routing profiles."

## Available Virtual Models

### `thane:latest` — Daily Conversation *(default)*

The default for human conversation. Uses the best local model via
`local_first` routing, with full persona, memory, and delegation-first tool
access.

- **Model:** Best local model (router default)
- **Delegation:** Enabled (all-iteration gating)
- **Context:** Full (persona, memory, talents, episodic history)
- **Use when:** Chatting, asking questions, general interaction
- **Aliases:** `thane`, `thane:balanced`

### `thane:premium` — Complex Reasoning

Selects the highest-quality model available, regardless of cost. Use for
tasks requiring deep analysis, judgment, or creative work.

- **Model:** Highest quality score in configured models
- **Delegation:** Enabled
- **Context:** Full
- **Use when:** Complex analysis, nuanced questions, creative writing
- **Note:** Vendor-neutral — routes to whatever model has the highest quality
  score in your config (Opus, Gemini, GPT, etc.)
- **Aliases:** `thane:best`, `thane:thinking`

### `thane:ops` — Operations / Direct Tool Access

Premium model with **delegation gating disabled** — all tools are available
on every iteration. The model sees and acts on tool results firsthand rather
than delegating to a smaller model.

- **Model:** Highest quality score (same as premium)
- **Delegation:** **Disabled** — full tool access on every iteration
- **Context:** Full
- **Use when:** Multi-step operations, debugging, code review — anything
  where the primary model needs direct tool control
- **Cost:** Highest. Every tool-call iteration uses the premium model.

### `thane:assist` — Quick Task Execution

Optimized for brief, action-oriented requests. Fast local-first model
selection with `device_control` mission hints. The canonical name for
HA voice commands and quick device control.

- **Model:** Cost-optimized, local-first (`local_only=true`, `quality_floor=4`)
- **Delegation:** Enabled
- **Context:** Minimal
- **Use when:** "Turn off the lights," "what's the temperature," quick
  lookups
- **Good for:** HA voice commands, quick device control
- **Aliases:** `thane:command`, `thane:fast`, `thane:homeassistant`

### `thane:local` — Local Models Only

Forces local/free model selection. No paid API calls will be made.

- **Model:** Best local model (`local_only=true`)
- **Delegation:** Enabled
- **Context:** Full
- **Use when:** Cost-sensitive work, privacy-sensitive topics, testing local
  models

### `thane:event` — Automation / Fire-and-Forget *(advanced)*

For machine-initiated requests with no person on the other end. Uses the
cheapest local model. **Not exposed in `/api/tags`** — clients that
auto-discover models won't see it, but the name resolves when used
explicitly.

- **Model:** Cheapest local model (`local_only=true`, `quality_floor=1`)
- **Delegation:** Enabled
- **Context:** Minimal — no persona or memory overhead
- **Use when:** HA automations, webhooks, scheduled tasks, cron triggers
- **Note:** Responses are terse and structured, not conversational
- **Aliases:** `thane:trigger`

### `thane:peer` — Agent-to-Agent *(advanced)*

For communication between Thane instances or other AI agents. No persona
performance — structured, efficient interaction between machines that both
understand they're talking to software. **Not exposed in `/api/tags`**
— clients that auto-discover models won't see it, but the name resolves
when used explicitly.

- **Model:** Default routing (`mission=conversation`)
- **Delegation:** Enabled
- **Context:** Structured, no persona
- **Use when:** Another Thane instance, MCP client, or external agent is
  calling

## Deprecated Aliases

These names still resolve but log a deprecation warning. Prefer the
canonical name on the right.

| Alias | Canonical | Notes |
|----------|-------------|-------|
| `thane`, `thane:balanced` | `thane:latest` | Aliases of the default |
| `thane:best`, `thane:thinking` | `thane:premium` | Vendor-neutral name |
| `thane:command`, `thane:fast`, `thane:homeassistant` | `thane:assist` | Renamed for clarity |
| `thane:trigger` | `thane:event` | Renamed for clarity |

Every alias logs `virtual model alias used` at warn level on
resolution; the canonical name does not. The router behaviour is
identical either way — switching to the canonical name only quiets
the log line.

## How Virtual Models Work

When a client sends a request with `"model": "thane:premium"`, Thane:

1. Maps the virtual-model name to **routing hints** (quality floor,
   mission, local-only flag, delegation gating)
2. The **router** scores all configured models against those hints and
   selects the best match
3. The **agent loop** checks hints for delegation gating (enabled/disabled)
4. **Context injection** adapts based on the interaction type

The virtual model does *not* hardcode a specific deployment — it
describes the *intent*, and the router finds the best model for that
intent from your configured model list.

## Choosing a Virtual Model

```
Cost:    event < assist < latest < premium = ops
Quality: event < assist < latest < premium = ops
Control: event = assist = latest = premium < ops  (ops = direct tool access)
```

For most users: `thane:latest` for conversation, `thane:premium` when you
need the best, `thane:ops` for hands-on operations work, `thane:assist`
for quick device control.

## Configuration

Exposed virtual models (`thane:latest`, `thane:premium`, `thane:ops`,
`thane:assist`, `thane:local`) appear as models in any
Ollama-compatible client. In Open WebUI, they show up in the model
selector. In HA Assist, set the model name in the conversation agent
configuration.

The advanced virtual models (`thane:event`, `thane:peer`) are not
listed in `/api/tags` for auto-discovery, but resolve correctly when
used as an explicit model name — useful for machine-initiated calls
(automations, agent-to-agent traffic) where the caller knows what it
wants.

No configuration is needed — virtual models work out of the box. The
router uses your `models.available` list to determine what "premium"
and "local" mean for your setup.
