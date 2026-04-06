# Getting Started

## Prerequisites

- **[Go](https://go.dev/) 1.24+** — for building from source
- **[just](https://just.systems/)** — command runner (replaces Makefiles)
- **[Ollama](https://ollama.ai/)** — local model inference, at least one model pulled
- **[Home Assistant](https://www.home-assistant.io/)** — with a long-lived access token
- **MQTT broker** — typically the Mosquitto add-on in Home Assistant

Thane requires Home Assistant and an MQTT broker. HA is the sensory layer —
how Thane perceives and acts in the physical world. MQTT is the telemetry
bus — how Thane publishes its own state as HA-discoverable entities. See
[Philosophy: Why Home Assistant](../understanding/philosophy.md#why-home-assistant)
for the reasoning.

## Build

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build
```

This builds a platform-specific binary into `dist/`. Cross-compile with
`just build linux arm64`.

## Initialize

Set up the `~/Thane` directory with config, talents, and persona:

```bash
just init
```

This creates `~/Thane/config.yaml`, copies talent files, and sets up the
persona. Edit `~/Thane/config.yaml` for your setup.

**Required settings:**

```yaml
models:
  ollama_url: http://localhost:11434
  default: your-preferred-model

homeassistant:
  url: http://homeassistant.local:8123
  token: your_long_lived_access_token

mqtt:
  broker: tcp://homeassistant.local:1883
```

**Optional cloud model support:**

```yaml
anthropic:
  api_key: sk-ant-...
```

See [Configuration](configuration.md) for a guide by concern, or
`examples/config.example.yaml` for the full field reference.

## Run

```bash
# Start the server
just serve

# Or run the binary directly
~/Thane/bin/thane serve

# Quick test from CLI
~/Thane/bin/thane ask "Hello!"
```

The server starts three listeners:
- `http://localhost:8080` — Native API (OpenAI-compatible) + web dashboard
- `http://localhost:11434` — Ollama-compatible API (for Home Assistant)
- `http://localhost:8843` — CardDAV server (for contact sync)

## Web Chat

Thane includes a built-in web chat interface at `http://localhost:8080/chat`.

## Connect to Home Assistant

1. In HA: **Settings > Devices & Services > Add Integration > Ollama**
2. Set URL to `http://thane-host:11434`, select model `thane:latest`
3. Under **Voice Assistants**, set conversation agent to the new integration

See [Home Assistant](homeassistant.md) for the full integration guide.

## Development

All workflows go through [just](https://just.systems/):

```bash
just build                    # Build for current platform
just build linux arm64        # Cross-compile
just test                     # Run tests (always with -race)
just ci                       # Full CI gate: fmt + lint + test
just install                  # Install binary to ~/Thane/bin/
just service-install          # Install + enable as system service
just logs                     # Tail live logs
```

Run `just` with no arguments to see all available recipes.

## Next Steps

- [Your First Thane](guide.md) — full onboarding guide with hardware, models, and relationship building
- [Home Assistant](homeassistant.md) — detailed HA integration and protocols
- [Configuration](configuration.md) — config organized by concern
- [Deployment](deployment.md) — service installation for macOS and Linux
