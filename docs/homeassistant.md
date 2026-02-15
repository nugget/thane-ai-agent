# Home Assistant Integration

> Your home already has an agent. We make it *autonomous*.

Thane integrates with Home Assistant through multiple protocols, each used for what it does best.

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

1. Go to **Settings → Devices & Services → Add Integration**
2. Search for **Ollama**
3. Set the URL to `http://thane-host:11434`
4. Select model `thane:latest`

### 3. Set as Conversation Agent

1. Go to **Settings → Voice Assistants**
2. Edit your assistant (or create a new one)
3. Set **Conversation Agent** to the Ollama integration

That's it. Talk to your voice assistant or type in the HA chat — Thane handles the rest.

## How It Works

When HA sends a conversation to Thane's Ollama-compatible API, Thane:

1. **Strips HA's injected tools** — HA sends limited tool definitions (HassTurnOn, GetLiveContext, etc.). Thane removes these and uses its own, smarter toolset.
2. **Delegates to local models** — The primary model orchestrates; tool-heavy work (entity search, service calls, state verification) is delegated to fast local models at zero API cost.
3. **Queries HA directly** — Instead of relying on pre-selected entities, Thane discovers and queries entities dynamically.
4. **Remembers context** — Facts and preferences persist across conversations.

## Integration Protocols

### REST API
State queries, service calls, template rendering. Used by native HA tools (`get_state`, `find_entity`, `call_service`, `control_device`).

### WebSocket API
Persistent connection for real-time `state_changed` events. Thane subscribes to entity patterns (e.g., `person.*`, `binary_sensor.*door*`) and receives instant notifications when state changes. This is the same mechanism used by the HA frontend and mobile apps — the official, first-class event bus.

### MCP (ha-mcp)
[ha-mcp](https://github.com/karimkhaleel/ha-mcp) provides 90+ HA tools via the Model Context Protocol — search, state queries, service calls, camera images, automation traces, area registry, and more. Thane hosts ha-mcp as a stdio subprocess and bridges selected tools into the agent loop.

See [Delegation & MCP](delegation.md) for details on tool gating and MCP configuration.

### MQTT
Thane publishes its own sensor telemetry as HA-discoverable entities (uptime, token usage, model info, version). Also subscribes to Frigate events for NVR-driven triggers. HA state changes use the WebSocket API, not MQTT.

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

## Routing Profiles for HA

Any [routing profile](routing-profiles.md) works with HA, but some are particularly useful:

- **`thane:latest`** — General conversation, delegates HA tasks to local models
- **`thane:command`** — Quick device control ("turn off the lights")
- **`thane:trigger`** — Cheapest option for HA automations calling Thane
- **`thane:ops`** — Direct tool access when you need the primary model to see HA state firsthand

## macOS Note

If running Thane as a launchd service on macOS, you must grant **Local Network** permission in System Settings → Privacy & Security → Local Network. Without this, macOS silently blocks unsigned binaries from accessing LAN hosts like Home Assistant. See [issue #53](https://github.com/nugget/thane-ai-agent/issues/53).
