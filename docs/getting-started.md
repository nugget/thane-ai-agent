# Getting Started with Thane

## Prerequisites

- **[Go](https://go.dev/) 1.24+** (for building from source)
- **[just](https://just.systems/)** (command runner — replaces Makefiles)
- **[Ollama](https://ollama.ai/)** running with at least one model pulled
- **Home Assistant** (optional but recommended)

## Build

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build
```

This builds a platform-specific binary into `dist/`. Cross-compile with `just build linux arm64`.

## Initialize

Set up the `~/Thane` directory with config, talents, and persona:

```bash
just init
```

This creates `~/Thane/config.yaml`, copies talent files, and sets up the persona. Edit `~/Thane/config.yaml` for your setup.

At minimum, set:
- `models.ollama_url` — where your Ollama instance lives
- `models.default` — which model to use
- `data_dir` — where to store SQLite databases

If you have Home Assistant:
- `homeassistant.url` — your HA instance URL
- `homeassistant.token` — a long-lived access token ([how to create one](https://www.home-assistant.io/docs/authentication/#your-account-profile))

Optional cloud model support:
- `anthropic.api_key` — enables Claude models for complex reasoning

See `examples/config.example.yaml` for all available options with documentation.

## Run

```bash
# Start the server
just serve

# Or run the binary directly
./dist/thane-*/thane -config config.yaml serve

# Quick test from CLI
./dist/thane-*/thane -config config.yaml ask "Hello!"
```

The server starts two API endpoints:
- `http://localhost:8080` — Native API (OpenAI-compatible)
- `http://localhost:11434` — Ollama-compatible API (for HA integration)

## Web Chat

Thane includes a built-in web chat interface at `http://localhost:8080/chat`.

## Development

All workflows go through `just`. Run `just` with no arguments to see available recipes:

```bash
just ci                       # Full CI gate: fmt + lint + test
just build                    # Build for current platform
just test                     # Run tests (always with -race)
just logs                     # Tail live logs
```

## Next Steps

- [Home Assistant Integration](homeassistant.md) — Connect Thane as your HA conversation agent
- [ARCHITECTURE.md](../ARCHITECTURE.md) — Understand how Thane works under the hood
- [config.example.yaml](../examples/config.example.yaml) — Full configuration reference
