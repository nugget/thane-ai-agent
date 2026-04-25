# Getting Started

## Prerequisites

- **[Go](https://go.dev/) 1.24+** — for building from source
- **[just](https://just.systems/)** — command runner (replaces Makefiles)
- **At least one model provider** — [Ollama](https://ollama.ai/),
  [LM Studio](https://lmstudio.ai/), or an
  [Anthropic](https://www.anthropic.com/) API key
- **[Home Assistant](https://www.home-assistant.io/)** — with a long-lived access token
- **MQTT broker** — typically the Mosquitto add-on in Home Assistant

Thane requires Home Assistant and an MQTT broker. HA is the sensory layer —
how Thane perceives and acts in the physical world. MQTT is the telemetry
bus — how Thane publishes its own state as HA-discoverable entities. See
[Philosophy: Why Home Assistant](../understanding/philosophy.md#why-home-assistant)
for the reasoning.

> **Future:** Self-managed Home Assistant in a Docker container is on the
> roadmap for operators who don't already run HA.

## Build

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build
```

This builds a platform-specific binary into `dist/`. Cross-compile with
`just build linux arm64`.

## Initialize

Set up the `~/Thane` directory with config, talents, persona, and a local
cryptographic identity:

```bash
just init
```

This creates `~/Thane/config.yaml`, copies talent files, sets up the
persona, and bootstraps `~/Thane/core` as the instance trust root. The core
root contains a generated Ed25519 signing key, an internal channel CA, and a
signed git birth commit containing the public identity material and
`core/config.yaml`. Edit `~/Thane/config.yaml` for your setup.

**Required settings:**

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: your_long_lived_access_token

mqtt:
  broker: tcp://homeassistant.local:1883
```

**Model providers** (configure at least one):

```yaml
# Local models via Ollama (recommended for delegation)
models:
  ollama_url: http://localhost:11434

# Local models via LM Studio
models:
  lmstudio_url: http://localhost:1234

# Cloud models via Anthropic
anthropic:
  api_key: sk-ant-...
```

Most operators run a local provider for delegation (cheap, fast tool
execution) and optionally add Anthropic for complex reasoning. See
[Your First Thane](guide.md) for hardware and model sizing guidance.

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
- `http://localhost:8080` — Native API (OpenAI-compatible) + operational dashboard
- `http://localhost:11434` — Ollama-compatible API (for Home Assistant)
- `http://localhost:8843` — CardDAV server (for contact sync)

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

- [Your First Thane](guide.md) — full onboarding guide with models and relationship building
- [Hardware Requirements](hardware.md) — platform support and minimum specs
- [Home Assistant](homeassistant.md) — detailed HA integration and protocols
- [Configuration](configuration.md) — config organized by concern
- [Deployment](deployment.md) — service installation for macOS and Linux
