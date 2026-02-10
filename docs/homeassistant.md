# Home Assistant Integration

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
./thane -config config.yaml serve
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
3. **Executes actions** — `control_device` handles natural language commands with fuzzy entity matching. Say "turn on the kitchen light" and Thane finds the right entity.
4. **Remembers context** — Facts and preferences persist across conversations.

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
- "Remind me to check the mail when I get home"

## Compared to Built-in Assist

| Capability | HA Assist | Thane |
|-----------|-----------|-------|
| Entity access | Pre-selected only | Full API access |
| Device discovery | Manual exposure | Automatic |
| Memory | None | Persistent semantic memory |
| Context awareness | Limited | Full state correlation |
| Model choice | Single | Smart routing |
