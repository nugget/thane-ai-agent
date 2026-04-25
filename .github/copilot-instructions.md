# Copilot Instructions for Thane

For full project conventions, see [AGENTS.md](../AGENTS.md).

## Build Notes

- Build requires `-tags "sqlite_fts5"` (handled by the justfile)
- Cross-compile targets: `darwin/arm64`, `darwin/amd64`, `linux/arm64`,
  `linux/amd64`
- Never call `gofmt`, `go vet`, `go test` directly — use `just` recipes

## Review Focus

When reviewing or generating code, watch for:

- Unbounded data returns from tools (search: 16 KB cap, transcripts: 32 KB)
- Inline LLM prompt text outside `internal/model/prompts/`
- `sql.ErrNoRows` must be handled distinctly from other DB errors
- Async goroutines must have context timeouts
- No `os.Exit` outside `main()`
- Config defaults go in `applyDefaults()`, not struct tags
- Use `errors.Is()` for sentinel error checks, not `==`
