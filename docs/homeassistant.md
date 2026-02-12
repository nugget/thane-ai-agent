# Home Assistant Integration

> Your home already has an agent. We make it *autonomous*.

Thane integrates with Home Assistant as a native conversation agent through the Ollama-compatible API.

## Setup

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

### 2. Start Thane

```bash
just serve
```

Verify the Ollama-compatible API is running:
```bash
curl http://localhost:11434/api/tags
```

### 3. Add Ollama Integration in HA

1. Go to **Settings → Devices & Services → Add Integration**
2. Search for **Ollama**
3. Set the URL to `http://thane-host:11434`
4. Select model `thane:latest`

### 4. Set as Conversation Agent

1. Go to **Settings → Voice Assistants**
2. Edit your assistant (or create a new one)
3. Set **Conversation Agent** to the Ollama integration you just added

## How It Works

When you talk to your voice assistant or type in the HA chat, Home Assistant sends the conversation to Thane's Ollama-compatible API. Thane:

1. **Strips HA's injected tools** — HA sends its own limited tool definitions (HassTurnOn, GetLiveContext, etc.). Thane removes these and uses its own, smarter toolset.
2. **Queries HA directly** — Instead of relying on pre-selected entities, Thane discovers and queries entities through the REST and WebSocket APIs.
3. **Executes actions** — `control_device` handles natural language commands with fuzzy entity matching.
4. **Remembers context** — Facts and preferences persist across conversations.

## Available Tools

| Tool | Description |
|------|-------------|
| `control_device` | Natural language device control with fuzzy entity matching |
| `find_entity` | Smart entity discovery across all HA domains |
| `get_state` | Current state of any entity |
| `list_entities` | Browse entities by domain or pattern |
| `call_service` | Direct HA service invocation |

## What You Can Do

**Device control:**
- "Turn on the living room lights"
- "Set the thermostat to 72"
- "Lock the front door"

**Status queries:**
- "Is anyone home?"
- "What's the temperature in the garage?"
- "Are any doors open?"

**Complex reasoning:**
- "Why is the house so warm?"
- "What happened while I was away?"

## Compared to Built-in Assist

| Capability | HA Assist | Thane |
|-----------|-----------|-------|
| Entity access | Pre-selected only | Full API access |
| Device discovery | Manual exposure | Automatic |
| Memory | None | Persistent semantic memory |
| Context awareness | Limited | Full state correlation |
| Model choice | Single | Smart routing (local + cloud) |

## macOS Note

If running Thane as a launchd service on macOS, you must grant **Local Network** permission in System Settings → Privacy & Security → Local Network. Without this, macOS silently blocks unsigned binaries from accessing LAN hosts like Home Assistant. See [issue #53](https://github.com/nugget/thane-ai-agent/issues/53).
