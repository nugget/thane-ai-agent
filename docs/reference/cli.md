# CLI Reference

Thane ships as a single binary with four commands.

```
$ thane --help
Thane - Autonomous Home Assistant Agent

Usage: thane [flags] <command> [args]

Commands:
  serve    Start the API server
  ask      Ask a single question (for testing)
  ingest   Import markdown docs into fact store
  version  Show version information

Flags:
  -config <path>    Path to config file (default: auto-discover)
  -o, --output fmt  Output format: text (default) or json
```

## Commands

### `thane serve`

Start the API server. This is the primary runtime mode — Thane runs as a
long-lived process serving APIs, processing events, and managing scheduled
tasks.

Starts three listeners:
- **Port 8080** — Native API (OpenAI-compatible) + web dashboard
- **Port 11434** — Ollama-compatible API (for Home Assistant)
- **Port 8843** — CardDAV server (for contact sync)

### `thane ask`

One-shot question for testing. Runs a single request through the agent loop
and prints the response. Uses in-memory stores — no persistent state.

```bash
thane ask "What time is it?"
```

### `thane ingest`

Import markdown documents into the semantic fact store. Parses structured
content into categorized facts with optional embeddings.

```bash
thane ingest ~/notes/home-layout.md
```

### `thane version`

Print version, commit hash, build time, and branch information. Version is
injected at build time via ldflags.

## Config Auto-Discovery

If no `-config` flag is provided, Thane searches these paths in order:

1. `./config.yaml`
2. `~/Thane/config.yaml`
3. `~/.config/thane/config.yaml`
4. `/config/config.yaml`
5. `/usr/local/etc/thane/config.yaml`
6. `/etc/thane/config.yaml`

The first file found is used. See
[Configuration](../operating/configuration.md) for what goes in the config
file.
