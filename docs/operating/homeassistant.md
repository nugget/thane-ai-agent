# Thane + Home Assistant

> Your home already has an agent. We make it *autonomous*.

Thane connects to Home Assistant through multiple protocols, each used for
what it does best. No custom integration or HACS required — Thane uses HA's
native Ollama integration and standard APIs.

## Quick Setup

### 1. Create a Long-Lived Access Token

In Home Assistant:
1. Go to your **Profile** (bottom-left)
2. Scroll to **Long-Lived Access Tokens**
3. Click **Create Token**, name it "Thane"
4. Copy the token into your `config.yaml`:

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: your_token_here
```

### 2. Add Ollama Integration in HA

1. Go to **Settings > Devices & Services > Add Integration**
2. Search for **Ollama**
3. Set the URL to `http://thane-host:11434`
4. Select model `thane:latest`

### 3. Set as Conversation Agent

1. Go to **Settings > Voice Assistants**
2. Edit your assistant (or create a new one)
3. Set **Conversation Agent** to the Ollama integration

That's it. Talk to your voice assistant or type in the HA chat — Thane
handles the rest.

## How It Works

When HA sends a conversation to Thane's Ollama-compatible API, Thane:

1. **Strips HA's injected tools** — HA sends limited tool definitions
   (HassTurnOn, GetLiveContext, etc.). Thane removes these and uses its own,
   smarter toolset.
2. **Delegates to local models** — The primary model orchestrates;
   tool-heavy work (entity search, service calls, state verification) is
   delegated to fast local models at zero API cost.
3. **Queries HA directly** — Instead of relying on pre-selected entities,
   Thane discovers and queries entities dynamically via the REST and
   WebSocket APIs.
4. **Remembers context** — Facts and preferences persist across
   conversations.

## Integration Protocols

### REST API

State queries, service calls, template rendering. Used by native HA tools
(`ha_get_state`, `ha_find_entity`, `ha_call_service`, `ha_control_device`). This is
the workhorse for most HA interactions.

### WebSocket API

Persistent connection for real-time `state_changed` events. The event feed
is gated client-side by the subscription registry: ingest-mode entity
subscriptions (ids or globs like `binary_sensor.*door*`, added via
`add_entity_subscription` from any owner) plus the system-seeded person
floor from `person.track` decide what is captured. This is the same
mechanism used by the HA frontend and mobile apps — the official,
first-class event bus.

WebSocket events can trigger agent wakes, enabling proactive behavior without
polling.

### No MCP bridge required

The native tool set is the complete HA surface — search, state, control
with target fan-out, history, automation CRUD with traces, service
discovery, and registry access all speak HA's REST and WebSocket APIs
directly. Earlier deployments bridged the third-party `ha-mcp` MCP
server to fill gaps; native parity landed in v0.10.2 and the bridge was
retired (dropping the `uvx` startup dependency and its supply-chain
exposure with it).

See [Delegation & MCP](../understanding/delegation.md) for MCP as a
general extension path.

### MQTT

Thane publishes its own sensor telemetry as HA-discoverable entities
(uptime, token usage, model info, version). Also subscribes to configurable
topics for event-driven triggers. HA state changes use the WebSocket API,
not MQTT.

See [MQTT](mqtt.md) for setup and configuration.

## Compared to Built-in Assist

| Capability | HA Assist | Thane |
|-----------|-----------|-------|
| Entity access | Pre-selected only | Full API — discovers everything |
| Device discovery | Manual exposure | Automatic |
| Memory | None | Persistent semantic memory |
| Context awareness | Limited | Full state correlation |
| Model choice | Single | Intent-based routing (local + cloud) |
| Tool execution | Cloud-dependent | Local models at zero cost via delegation |
| Real-time events | N/A | WebSocket state subscriptions |
| Cost per request | Varies | Mostly free (local model delegation) |

## What You Can Do

**Device control:**
- "Turn on the living room lights"
- "Set the thermostat to 72"
- "Make the office Hue Go teal"

**Status queries:**
- "Is anyone home?"
- "What's the temperature in the garage?"
- "Are any doors open?"

**Complex reasoning:**
- "Why is the house so warm?"
- "What happened while I was away?"
- "The laundry has been in the washer for an hour — should I move it?"

## Virtual Models for HA

Any [virtual model](routing-profiles.md) works with HA, but some are
particularly useful:

- **`thane:latest`** — General conversation, delegates HA tasks to local
  models
- **`thane:assist`** — Quick device control ("turn off the lights").
  Accepts the aliases `thane:command`, `thane:fast`, and
  `thane:homeassistant`.
- **`thane:event`** — Cheapest option for HA automations calling
  Thane. Not listed in `/api/tags` (use the explicit name from your
  automation). Accepts the alias `thane:trigger`.
- **`thane:ops`** — Direct tool access when you need the primary model to
  see HA state firsthand
