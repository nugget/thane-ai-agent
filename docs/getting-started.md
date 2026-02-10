# Getting Started with Thane

## Prerequisites

- **Go 1.21+** (for building from source)
- **Ollama** running with at least one model pulled
- **Home Assistant** (optional but recommended)

## Build

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
go build -o thane ./cmd/thane
```

## Configure

Copy the example config and edit for your setup:

```bash
cp config.example.yaml config.yaml
```

At minimum, set:
- `models.ollama_url` — where your Ollama instance lives
- `models.default` — which model to use
- `data_dir` — where to store SQLite databases

If you have Home Assistant:
- `homeassistant.url` — your HA instance URL
- `homeassistant.token` — a long-lived access token ([how to create one](https://www.home-assistant.io/docs/authentication/#your-account-profile))

See `config.example.yaml` for all available options with documentation.

## Run

```bash
# Start the server
./thane -config config.yaml serve

# Quick test from CLI
./thane -config config.yaml ask "Hello!"
```

The server starts two API endpoints:
- `http://localhost:8080` — Native API (OpenAI-compatible)
- `http://localhost:11434` — Ollama-compatible API (for HA integration)

## Web Chat

Thane includes a built-in web chat interface at `http://localhost:8080/chat`.

## Next Steps

- [Home Assistant Integration](homeassistant.md) — Connect Thane as your HA conversation agent
- [ARCHITECTURE.md](../ARCHITECTURE.md) — Understand how Thane works under the hood
- [config.example.yaml](../config.example.yaml) — Full configuration reference
