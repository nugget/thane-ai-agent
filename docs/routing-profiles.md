# Routing Profiles

Thane uses **routing profiles** to adapt its behavior based on the nature of each interaction. Profiles are selected by setting the model name in any Ollama-compatible client (Open WebUI, HA Assist, API calls).

## Available Profiles

### `thane:latest` — Daily Conversation *(default)*

The default for human conversation. Uses the best local model via `local_first` routing, with full persona, memory, and delegation-first tool access.

- **Model:** Best local model (router default)
- **Delegation:** Enabled (all-iteration gating)
- **Context:** Full (persona, memory, talents, episodic history)
- **Use when:** Chatting, asking questions, general interaction

### `thane:premium` — Complex Reasoning

Selects the highest-quality model available, regardless of cost. Use for tasks requiring deep analysis, judgment, or creative work.

- **Model:** Highest quality score in configured models
- **Delegation:** Enabled
- **Context:** Full
- **Use when:** Complex analysis, nuanced questions, creative writing
- **Note:** Vendor-neutral — routes to whatever model has the highest quality score in your `models.available` config (Opus, Gemini, GPT, etc.)

### `thane:ops` — Operations / Direct Tool Access

Premium model with **delegation gating disabled** — all tools are available on every iteration. The model sees and acts on tool results firsthand rather than delegating to a smaller model.

- **Model:** Highest quality score (same as premium)
- **Delegation:** **Disabled** — full tool access on every iteration
- **Context:** Full
- **Use when:** Multi-step operations, deployments, debugging, code review — anything where the primary model needs direct tool control
- **Cost:** Highest. Every tool-call iteration uses the premium model.

### `thane:command` — Quick Task Execution

Optimized for brief, action-oriented requests. Fast model selection with device-control mission hints.

- **Model:** Cost-optimized (device_control mission scoring)
- **Delegation:** Enabled
- **Context:** Minimal
- **Use when:** "Turn off the lights," "what's the temperature," quick lookups
- **Good for:** HA voice commands from a person, quick device control

### `thane:trigger` — Automation / Fire-and-Forget

For machine-initiated requests with no person on the other end. Uses the cheapest local model.

- **Model:** Cheapest local model (local_only, quality_floor=1)
- **Delegation:** Enabled
- **Context:** Minimal — no persona or memory overhead
- **Use when:** HA automations, webhooks, scheduled tasks, cron triggers
- **Note:** Responses are terse and structured, not conversational

### `thane:peer` — Agent-to-Agent

For communication between Thane instances or other AI agents. No persona performance — structured, efficient interaction between machines that both understand they're talking to software.

- **Model:** Default routing
- **Delegation:** Enabled
- **Context:** Structured, no persona
- **Use when:** Another Thane instance, MCP client, or external agent is calling
- **Future:** Capability negotiation, trust levels, cross-instance memory

### `thane:local` — Local Models Only

Forces local/free model selection. No paid API calls will be made.

- **Model:** Best local model (local_only=true)
- **Delegation:** Enabled
- **Context:** Full
- **Use when:** Cost-sensitive work, privacy-sensitive topics, testing local models

## Deprecated Profiles

These old names still work but log a deprecation warning. They will be removed in a future release.

| Old Name | Replacement | Notes |
|----------|-------------|-------|
| `thane:thinking` | `thane:premium` | Vendor-neutral name |
| `thane:balanced` | `thane:latest` | Was redundant with default routing |
| `thane:fast` | `thane:command` | Renamed for clarity |
| `thane:homeassistant` | `thane:command` | Channel ≠ profile; HA can use any profile |

## How Profiles Work

When a client sends a request with `"model": "thane:premium"`, Thane:

1. Maps the profile name to **router hints** (quality floor, mission, local-only flag, delegation gating)
2. The **router** scores all configured models against those hints and selects the best match
3. The **agent loop** checks hints for delegation gating (enabled/disabled)
4. **Context injection** adapts based on the interaction type

The profile does *not* hardcode a specific model — it describes the *intent*, and the router finds the best model for that intent from your configured model list.

## Choosing a Profile

```
Cost:    trigger < command < latest < premium = ops
Quality: trigger < command < latest < premium = ops
Control: trigger = command = latest = premium < ops (ops = direct tool access)
```

For most users: `thane:latest` for conversation, `thane:premium` when you need the best, `thane:ops` for hands-on operations work.

## Configuration

Profiles appear as models in any Ollama-compatible client. In Open WebUI, they show up in the model selector. In HA Assist, set the model name in the conversation agent configuration.

No configuration is needed to use profiles — they work out of the box. The router uses your `models.available` list to determine what "premium" and "local" mean for your setup.
