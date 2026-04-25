# Anthropic Prompt Caching

How Thane uses Anthropic's prompt-caching feature, what the per-section
TTL policy is, and how to add a new system-prompt section without
accidentally breaking the cache.

## Why we cache

Anthropic's prompt-cache charges **0.1× the base input rate** on cache
reads and reduces time-to-first-token by roughly **85%** for cached
prefixes. Thane's system prompt runs 30–100 KB per turn (persona, ego,
mission, talents, context providers, conversation history) plus a full
tool catalog. Without caching, every turn re-sends that whole prefix
as paid input tokens. With caching, turn 2 and beyond read almost all
of it from the cache for 10% of the cost.

The cost win is obvious. The latency win matters more for interactive
channels: a cached turn answers noticeably faster, especially with
Opus and 100+ KB prefixes.

## Per-section TTL policy

The system prompt is assembled as a list of named sections, each with
a `CacheTTL` annotation that controls where (and whether) a cache
breakpoint gets placed. The policy lives in
[`internal/runtime/agent/loop.go`](../internal/runtime/agent/loop.go) in
`promptSectionCacheTTL`:

| Section                 | TTL | Rationale |
|-------------------------|-----|-----------|
| `PERSONA`               | 1h  | Identity. Changes only when persona files are edited.  |
| `EGO`                   | 1h  | Self-reflection file. Stable across long sessions. |
| `RUNTIME CONTRACT`      | 1h  | Execution semantics. Model-invariant. |
| `INJECTED CONTEXT`      | 1h  | Mission + knowledge files. Stable across a session.   |
| `TOOL CALLING CONTRACT` | 1h  | Model-family-specific tool calling guidance.          |
| `TALENTS ALWAYS ON`     | 1h  | Behavioural guidance. Stable across a session.        |
| `TALENTS TAGGED`        | 5m  | Tag-scoped talents. Can change per turn if tags flip. |
| `ACTIVE CAPABILITIES`   | —   | Derived from active tags; changes per turn.           |
| `TAG CONTEXT`           | —   | Dynamic capability knowledge; changes per turn.       |
| `CURRENT CONDITIONS`    | —   | Environment / timezone. Changes every turn by definition. |
| `DYNAMIC CONTEXT`       | —   | ContextProvider output; per-turn.                     |
| `CONVERSATION HISTORY`  | —   | Append-only, but uncached so turn N+1 sees turn N.    |
| `CONTEXT USAGE`         | —   | Per-turn token counts.                                |

Volatile sections (no TTL) sit **after** the cached sections in the
final prompt so the cache prefix stops at the last stable byte. Tools
get a blanket `1h` cache on the last tool definition in
[`internal/model/models/providers/anthropic.go`](../internal/model/models/providers/anthropic.go).

## Decision tree for new sections

When adding a new system-prompt section, pick a TTL this way:

1. **Does the content change on every turn?** → `CacheTTL: ""` (no
   cache). Placing a breakpoint here invalidates every prior cached
   section. Put it *after* the cached sections so it doesn't break
   the prefix.
2. **Does it change per day or by external input (talents pulled
   from disk, KB articles, user's active tags)?** → `CacheTTL: "5m"`.
   The shorter TTL matches how often you expect the content to
   churn; 5-minute writes are 1.25× base input price.
3. **Is it effectively static across a session (persona, tool
   contract, compiled-in behavioural guidance)?** → `CacheTTL: "1h"`.
   1-hour writes are 2.0× base input price but pay off after ~3
   cache reads inside the hour.

Anything that a human would describe as "this never changes" is a 1h
candidate. Anything that updates on background heartbeats or config
reloads is 5m. Anything tied to the current turn's content is
uncached.

## Minimum cacheable prefix length

Anthropic silently ignores cache breakpoints on prefixes that fall
below a per-family threshold:

| Family                   | Minimum tokens |
|--------------------------|----------------|
| Claude Sonnet 4.x        | 1024           |
| Claude Opus 4.x          | 4096           |
| Claude Haiku 4.x         | 4096           |

Thane enforces this in
[`anthropic.go:applyCacheBreakpointGuards`](../internal/model/models/providers/anthropic.go):
under-minimum runs have their `cache_control` stripped at request time
with a WARN log, so the request doesn't silently miss caching while
appearing to request it. Unknown model families default to the
strictest minimum.

## The 4-breakpoint cap

Anthropic rejects requests carrying more than **4 `cache_control`
markers total** across system blocks, tools, and messages. Today's
policy emits 2 system breakpoints + 1 tool breakpoint = 3. Any future
edit to `promptSectionCacheTTL` that introduces another TTL transition
could push the count over and break every Anthropic turn.

The guard in `applyCacheBreakpointGuards` drops excess breakpoints
before the request is sent: it drops the blanket tool breakpoint
first (it's a catch-all policy), then trims trailing system
breakpoints. Every drop logs a WARN so operators can see why the
cache didn't apply.

## Validating that caching is working

Three signals, in order of usefulness:

1. **`cache_hit_rate`** on every Anthropic debug log line. Values in
   [0, 1]; a mature session on a tight task loop should run above 0.9.
   Cold start shows 0.0 on the first call (nothing to read yet);
   subsequent calls should climb quickly.
2. **`cache_hit_rate` in the session stats JSON** served by the API
   server at `/stats`. Same formula, aggregated across the session.
   `cache_read_input_tokens` / (`cache_read_input_tokens` +
   `cache_creation_input_tokens`).
3. **Raw `cache_creation_input_tokens` and `cache_read_input_tokens`**
   on debug log lines. Useful for sanity-checking absolute sizes
   (e.g., tool definitions alone are ~5k tokens, so a cold turn
   should write at least that).

## Anti-patterns

- **Cache breakpoints on changing content.** Putting a TTL on
  `CURRENT CONDITIONS` would destroy the cache prefix every turn.
  `promptSectionCacheTTL` explicitly returns `""` for volatile
  sections; keep it that way.
- **Fragmenting into many TTL runs.** Each `promptSectionCacheTTL`
  transition creates a new breakpoint. Try to order sections so
  same-TTL runs are contiguous; don't interleave 1h and 5m.
- **Assuming 4 breakpoints is plenty.** Tools already take one slot,
  so system has 3 real slots. Adding a third system TTL pushes you to
  the cap; a fourth transition drops the tool cache (see
  `applyCacheBreakpointGuards`).
- **Forgetting the minimum.** A small cached section (say,
  `CURRENT CONDITIONS` if someone miscategorized it as 5m) attached
  to a short prefix produces a silent no-op. The guard will warn,
  but better to not emit the breakpoint in the first place.

## References

- [Anthropic prompt caching docs](https://platform.claude.com/docs/en/docs/build-with-claude/prompt-caching)
- [`internal/runtime/agent/loop.go`](../internal/runtime/agent/loop.go) — `promptSectionCacheTTL` (policy)
- [`internal/model/models/providers/anthropic.go`](../internal/model/models/providers/anthropic.go) — `applyCacheBreakpointGuards` (enforcement)
- [`internal/model/llm/types.go`](../internal/model/llm/types.go) — `CacheHitRate` helper
- [`internal/platform/usage/store.go`](../internal/platform/usage/store.go) — per-TTL cost breakdown
