# Copilot Instructions for Thane

## Project Overview

Go AI agent for Home Assistant. Module: `github.com/nugget/thane-ai-agent`.
Single binary (`cmd/thane/main.go`) with modular internal packages.

## Build & Test

- Always use `just ci` (runs fmt, lint, vet, test with -race)
- Build requires `-tags "sqlite_fts5"` (handled by justfile)
- Cross-compile targets: `darwin/arm64`, `darwin/amd64`, `linux/arm64`, `linux/amd64`
- Never call `gofmt`, `go vet`, `go test` directly — use justfile recipes

## Conventions

- All exported types and functions must have godoc comments
- All HTTP clients must use `httpkit.NewClient()` / `httpkit.NewTransport()` — never construct `http.Client{}` directly
- Log levels: INFO = operator story, DEBUG = deep troubleshooting, WARN = degraded, ERROR = broken
- Internal prompts (LLM instruction text) live in `internal/prompts/`, not inline
- Tool result sizes must be capped (search: 16KB, transcript: 32KB)
- Config defaults go in `applyDefaults()`, not struct tags
- Use `errors.Is()` for sentinel error checks, not `==`

## Architecture

- `internal/agent/loop.go` — core conversation engine
- `internal/router/` — model selection (urgency × quality matrix)
- `internal/memory/` — conversation store, archive, compaction, extraction
- `internal/tools/` — tool registry and implementations
- `internal/prompts/` — all LLM prompt templates
- `internal/config/` — YAML config with validation
- `cmd/thane/main.go` — wiring only, no business logic

## Review Focus

- Watch for unbounded data returns from tools
- Flag any inline LLM prompt text outside `internal/prompts/`
- Verify `sql.ErrNoRows` is handled distinctly from other DB errors
- Async goroutines must have context timeouts
- No `os.Exit` outside main()
