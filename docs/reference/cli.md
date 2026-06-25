# CLI Reference

Thane ships as a single binary with eight commands.

```
$ thane --help
Thane - Autonomous Home Assistant Agent

Usage: thane [flags] <command> [args]

Commands:
  serve        Start the API server
  init [dir]   Initialize working directory with defaults (default: .)
  validate     Parse and validate the config without starting services
  ask          Ask a single question (for testing)
  ingest       Import markdown docs into fact store
  caps         Show resolved capability tags from a running daemon
  health [url] Probe a running daemon's /health endpoint (exit 0 if healthy)
  version      Show version information

Flags:
  -config <path>    Path to config file (default: auto-discover)
  -o, --output fmt  Output format: text (default) or json
```

## Commands

### `thane serve`

Start the API server. This is the primary runtime mode — Thane runs as a
long-lived process serving APIs, processing events, and managing scheduled
tasks.

Starts these listeners:
- **Port 8080** — Native Thane /v1 API + web dashboard
- **Port 8081** — OpenAI-compatible API (optional)
- **Port 11434** — Ollama-compatible API (for Home Assistant)
- **Port 8843** — CardDAV server (for contact sync)

### `thane init [dir]`

Initialize a Thane working directory with bundled defaults. Creates the
directory structure (`db/`, `talents/`, `archive/`), writes a default
`config.yaml` (0600 permissions, contains placeholders for secrets) and a
default `persona.md`, deploys the embedded talent corpus, bootstraps the
core identity (signing key, channel CA) and the archive skeleton
(orientation READMEs + the `interactions/` schema stub). Existing files
are never overwritten — re-runs report `(exists, skipping)` per file, so
it's safe to run against an established workspace to fill in anything
missing.

`dir` defaults to the current directory.

```bash
thane init ~/Thane
```

### `thane validate`

Parse and validate the config file without starting any services or
opening any sockets. Useful as a pre-deploy gate (`thane validate &&
thane serve`) or in CI.

```bash
thane validate                            # auto-discovered config
thane -config /etc/thane/config.yaml validate
thane -o json validate | jq .             # structured report for scripting
```

Text mode prints a one-line confirmation plus a short structural summary
(default model, resource/model/root counts, MCP server count, which
optional integrations are configured). JSON mode emits `{path, valid,
error, summary}` and exits non-zero on failure.

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

### `thane caps`

Show resolved capability tags from a running daemon — useful for
inspecting which tags resolved on the running config and what tools each
tag carries. Reads from the live `serve` process via its API; requires
the daemon to be running.

```bash
thane caps
thane caps -x          # include tags the operator overlay excluded
thane -o json caps     # structured output
```

### `thane health`

Probe a running daemon's `/health` endpoint and exit 0 if it reports
healthy, non-zero otherwise. Useful as a liveness check in scripts,
supervisors, or container health probes. Takes an optional URL; defaults
to the local daemon if omitted.

```bash
thane health
thane health http://127.0.0.1:8080/health
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
