# CLAUDE.md

Instructions for Claude Code working on the Thane codebase.

## Build & Test

All workflows go through [just](https://just.systems/). Never call `go` tools directly.

```bash
just build              # Build for current platform → dist/
just ci                 # Full CI gate: fmt check + lint + test (run before every push)
just test               # Tests only (always with -race)
just lint               # golangci-lint v2
just fmt-check          # gofmt check
```

**MANDATORY: `just ci` must pass locally before every `git push`. No exceptions.** Do not rely on GitHub Actions to catch lint or test failures — run the full gate locally first and fix any issues before pushing.

## Project Structure

```
cmd/thane/              Main binary (CLI, server setup, wiring)
internal/
  agent/                Agent loop (context assembly → planning → tool execution → response)
  api/                  HTTP API server (OpenAI-compatible + Ollama-compatible)
  httpkit/              Centralized HTTP client construction (all outbound HTTP goes through here)
  homeassistant/        HA REST + WebSocket client
  llm/                  LLM providers (Anthropic, Ollama) and model routing
  search/               Web search providers (SearXNG, Brave) with pluggable interface
  fetch/                Web page content extraction
  memory/               Conversation storage and compaction (SQLite)
  facts/                Semantic fact store with embeddings
  checkpoint/           State snapshot and restore
  conditions/           Current Conditions system prompt section (time, host, version)
  embeddings/           Embedding generation via Ollama
  tools/                Tool registry and implementations (HA, shell, files, search, fetch)
  config/               Configuration loading and validation
  talents/              Markdown-based agent behavior guidance
  web/                  Built-in web chat UI
  buildinfo/            Version injection via ldflags
  router/               Model selection routing
  scheduler/            Time-based task scheduling
  anticipation/         Event-based trigger system
  ingest/               Markdown document ingestion
  connwatch/            Service health monitoring with exponential backoff
```

## Code Conventions

- **Go 1.24+** required
- **Conventional commits**: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`
- **All HTTP clients** must use `httpkit.NewClient()` / `httpkit.NewTransport()` — never construct `http.Client{}` directly
- **Prefer the standard library**. Third-party module imports are expensive — they add supply chain risk, version churn, and transitive dependencies. If `net/http`, `encoding/json`, `crypto/tls`, or another stdlib package can do the job, use it. Only reach for an external module when the stdlib genuinely can't.
- **Error handling**: Always drain response bodies (`httpkit.DrainAndClose`), bound error reads (`httpkit.ReadErrorBody`)
- **Go doc comments**: Every exported symbol (function, type, const, var) must have a doc comment that starts with the symbol name and reads as a complete sentence. Every package must have a `// Package foo ...` comment. Follow the [Go Doc Comments](https://go.dev/doc/comment) conventions. Run `go doc ./internal/yourpkg` to verify rendering.
- **Tests**: Table-driven where possible, always with `-race`
- **Logging**: Structured via `slog`. Include relevant context fields (method, URL, entity_id, etc.)
- **Tool registration**: Use `tools.Register()` with JSON schema parameters
- **Provider pattern**: New integrations implement a provider interface (see `search.Provider`)

## Architecture Notes

- **Dual-port**: Port 8080 (native OpenAI API) + port 11434 (Ollama-compatible for HA)
- **Agent loop**: Iterates up to 10 times per request. Each iteration: LLM call → tool execution → repeat or respond. On exhaustion, makes a final `tools=nil` call to force a text response.
- **httpkit**: Single source of truth for outbound HTTP. Includes retry transport for transient errors, User-Agent injection, and connection pool management. All HTTP client packages route through it.
- **Model routing**: Selects between local (Ollama) and cloud (Anthropic) models based on task complexity.
- **Checkpoint/restore**: Conversations survive restarts via SQLite-backed state snapshots.
- **connwatch**: Background health monitoring for external services (HA, Ollama) with exponential backoff reconnection.

## Things to Watch For

- **macOS Local Network Privacy**: launchd-launched binaries need explicit permission to access LAN hosts (System Settings → Privacy & Security → Local Network). Internet targets work without it. This was a tricky diagnosis (issue #53).
- **Branch protection**: `main` requires PRs with verified signatures. No direct pushes.
- **Version injection**: Uses build-time `ldflags`, not hardcoded strings. The justfile handles this.
- **Config discovery**: Auto-searches `./config.yaml`, `~/Thane/config.yaml`, `~/.config/thane/config.yaml`, and system paths.
- **Pre-existing test**: `TestFindConfig_SearchPath` may find a real config file if `~/Thane/config.yaml` exists on the build host.
- **macOS code signing**: The justfile ad-hoc signs (`codesign -s -`) macOS builds so Gatekeeper doesn't quarantine rebuilt binaries. Distribution builds would need Developer ID signing + notarization.

## Security Considerations

- **Shell exec** is gated by config (`shell_exec.enabled`) with denied patterns and optional allowed prefixes
- **File tools** are sandboxed to the configured workspace directory
- **HA tokens** and **API keys** live in `config.yaml` — keep it `chmod 600`
- **httpkit** never disables TLS verification by default (`WithTLSInsecureSkipVerify` is opt-in)

## Workflow Notes

- The repository uses GitHub with SSH remotes (`git@github.com:nugget/thane-ai-agent.git`)
- **All commits must be signed.** The SessionStart hook configures repo-local signing automatically each session (see `~/.claude/CLAUDE.md` for identity details). Verify signing is active before your first commit: `git config commit.gpgsign` should return `true`.
- PRs require review before merge to `main`
- **Always run `just ci` before pushing** — it catches formatting, lint, and race conditions. This is a hard requirement, not a suggestion.
- Keep PRs focused: one feature or fix per PR
