# AGENTS.md

Welcome. Thane is an autonomous AI agent written in Go that connects
language models to Home Assistant, turning thousands of real-time sensor
readings into coherent environmental awareness. It runs on local models
via Ollama, optionally augmented by cloud models for complex reasoning.
Single binary, SQLite storage, no runtime dependencies.

If you're here to understand the project, start with
[docs/understanding/philosophy.md](docs/understanding/philosophy.md).
For the full documentation suite, see [docs/](docs/).

Everything below is what you need to contribute code.

## Build & Test

All workflows go through [just](https://just.systems/). Never call `go`
tools directly — the justfile handles build tags, cross-compilation, code
signing, and version injection.

```bash
just build              # Build for current platform → dist/
just ci                 # Full CI gate: fmt check + lint + test (run before every push)
just test               # Tests only (always with -race)
just lint               # golangci-lint v2
just fmt-check          # gofmt check
```

`just ci` must pass locally before every push. No exceptions. Don't rely
on GitHub Actions to catch what you could have caught locally.

## Code Conventions

- **Go 1.24+** required. Build requires `-tags "sqlite_fts5"` (the
  justfile handles this).
- **Conventional commits**: `feat:`, `fix:`, `docs:`, `refactor:`,
  `test:`, `chore:`.
- **All HTTP clients** must use `httpkit.NewClient()` /
  `httpkit.NewTransport()` — never construct `http.Client{}` directly.
  httpkit is the single source of truth for outbound HTTP: retry
  transport, User-Agent injection, connection pool management.
- **Prefer the standard library**. Third-party imports add supply chain
  risk, version churn, and transitive deps. Use stdlib when it can do the
  job.
- **Context propagation**: Always pass the caller's `ctx` through to
  downstream calls. Never use `context.Background()` inside a handler
  that receives `ctx` — it breaks cancellation and deadline enforcement.
- **Error handling**: Always drain response bodies
  (`httpkit.DrainAndClose`), bound error reads
  (`httpkit.ReadErrorBody`).
- **Timestamp parsing**: Use `database.ParseTimestamp()` for SQLite TEXT
  columns. Never raw `time.Parse` for stored timestamps.
- **Timestamps in model context**: Any timestamp shown to the model must
  use exact-second deltas (`-120s`, `+3600s`) via
  `awareness.FormatDeltaOnly()`. Models are poor at timestamp arithmetic.
  Storage and logs keep absolute timestamps.
- **String truncation**: Never truncate by byte index (`s[:n]`) — use
  `[]rune` or `truncateUTF8` in `internal/tools` to avoid splitting
  multi-byte characters.
- **Contract structs**: Exported structs that define cross-package,
  model-facing, API-facing, or persistence-facing contracts need explicit
  serialization tags (`json`, `yaml` where config-facing, `snake_case`
  names, `-` for runtime-only fields).
- **Model-facing changes**: If touching tool implementations, schemas,
  descriptions, or any context consumed by models, read
  [docs/model-facing-context.md](docs/model-facing-context.md) and
  [docs/model-facing-tools.md](docs/model-facing-tools.md) first. Apply
  those conventions during the work, not as a cleanup pass.
- **Tests**: Table-driven where possible, always with `-race`.
- **Logging**: Structured via `slog`. INFO = operator story, DEBUG = deep
  troubleshooting, WARN = degraded, ERROR = broken. Include relevant
  context fields.
- **Tool result sizes**: Cap output (search: 16 KB, transcripts: 32 KB).
  Watch for unbounded data returns.
- **Config defaults**: Set in `applyDefaults()`, not struct tags.
- **Go doc comments**: GoDoc is a primary audience for this codebase.
  Every exported symbol gets a doc comment starting with its name that
  reads as a complete sentence. Every package gets `// Package foo ...`.
  Write comments that help a reader understand *why*, not just *what* —
  the signature already says what.
- **Provider pattern**: New integrations implement a provider interface
  (see `search.Provider`).

## Architecture at a Glance

- **Dual-port**: Port 8080 (native OpenAI-compatible API + web dashboard)
  and port 11434 (Ollama-compatible API for HA integration).
- **Agent loop**: Iterates up to 10 times per request. Each iteration:
  LLM call, tool execution, repeat or respond. On exhaustion, a final
  `tools=nil` call forces a text response.
- **Delegation**: Orchestrator model plans; small local models execute
  tool-heavy work at zero API cost.
- **Model routing**: Scores models on quality, speed, and cost. Routing
  hints (quality floor, speed preference, local-only) propagate through
  delegation.
- **Capability tags**: Tools and talents grouped by semantic tags. Sessions
  start minimal; tags activate on demand, creating delegation pressure by
  architecture.
- **Memory**: SQLite-backed semantic facts with embeddings, conversation
  history with compaction, session archives with FTS5 search.
- **connwatch**: Background health monitoring for external services (HA,
  Ollama, email) with exponential backoff reconnection.
- **Checkpoint/restore**: Conversations survive restarts via SQLite state
  snapshots.

See [docs/understanding/architecture.md](docs/understanding/architecture.md)
for the full picture.

## Gotchas

- **macOS Local Network Privacy**: launchd-launched binaries need explicit
  Local Network permission to reach LAN hosts like HA
  ([issue #53](https://github.com/nugget/thane-ai-agent/issues/53)).
- **Branch protection**: `main` requires PRs with verified commit
  signatures. No direct pushes.
- **Version injection**: Build-time `ldflags`, not hardcoded. The justfile
  handles this.
- **Config discovery**: Auto-searches `./config.yaml`,
  `~/Thane/config.yaml`, `~/.config/thane/config.yaml`, and system paths.
- **Pre-existing test**: `TestFindConfig_SearchPath` may find a real
  config if `~/Thane/config.yaml` exists on the build host.
- **macOS code signing**: The justfile ad-hoc signs macOS builds. No
  Gatekeeper quarantine during development.

## Security

- **Shell exec** gated by config with denied patterns and allowed prefixes
- **File tools** sandboxed to the configured workspace directory
- **Tokens and API keys** in `config.yaml` — keep it `chmod 600`
- **httpkit** never disables TLS verification by default

## Contributing

### Issues

Use the issue templates when filing bugs or feature requests. Good issues
include: what's happening (or what should happen), why it matters, and
concrete acceptance criteria. Link related issues with `Refs #NNN`.

### Pull Requests

- **All commits must be signed.** Configure your signing key before your
  first commit. PRs with unsigned commits will not merge.
- Run `just ci` locally before pushing.
- Keep PRs focused — one logical change per PR.
- Use conventional commit format for PR titles and commits.
- Reference issues: `Refs #NNN` or `Closes #NNN` in commit bodies.
- **Update docs in the same PR.** If your change affects behavior that's
  documented — tool descriptions, configuration options, API surface,
  architectural patterns, CLI flags, deployment — update the relevant
  docs before requesting review. Documentation that drifts from code is
  worse than no documentation. When in doubt, check `docs/` and
  `AGENTS.md` for anything your change might invalidate. GoDoc counts
  as documentation: exported symbols, package comments, and type
  definitions are a primary interface for anyone reading this codebase.
- The PR template will guide you through the description and test plan.

### Common Review Feedback

These are the patterns that most often come up in review. Getting them
right on the first pass saves everyone a round trip:

- **Context propagation** — Don't use `context.Background()` where a `ctx`
  is available. Goroutines need context timeouts.
- **Unbounded data** — Tool results must be capped. Don't return full
  entity lists or raw transcripts without size limits.
- **Model-facing changes** — Read the model-facing docs before touching
  tool schemas or prompt content. This is not optional.
- **Race conditions** — Shared state needs mutex guards. Tests run with
  `-race`; they will catch you.
- **Silent failures** — If something can fail, log it. Debug-level is fine,
  but silent drops waste debugging time later.

### Review Culture

Leave PRs clean and reflective of reality. Open review threads, stale
descriptions, and unchecked test-plan items signal unfinished work.

When addressing review feedback: fix the issue, reply to the thread with
the commit hash and a one-line explanation, then resolve the conversation.
If deferring, say why before resolving.

## Further Reading

- [docs/](docs/) — Full documentation index
- [docs/understanding/philosophy.md](docs/understanding/philosophy.md) — Why Thane exists
- [docs/understanding/architecture.md](docs/understanding/architecture.md) — System design
- [CONTRIBUTING.md](CONTRIBUTING.md) — Development setup and workflow
