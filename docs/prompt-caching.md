# Prompt Caching and Section Stability

How Thane orders model-facing prompt sections, how it marks sections by
stability, and how provider adapters translate that into concrete cache
behavior.

This document is intentionally provider-neutral first. Anthropic is the
first provider with explicit prompt-cache controls in this codebase, but
the global prompt-shaping rules apply to every model provider.

## Why This Exists

Thane's system prompt can run tens of kilobytes per turn: persona,
runtime contracts, behavioral guidance, capability state, continuity
context, live state, and tool definitions. Re-sending all of that as
fresh input every turn is expensive and slow.

The cache win depends on one simple invariant:

> Stable prefix first, volatile context last.

Even providers with different caching mechanics benefit from this. A
provider that exposes explicit cache breakpoints can cache the stable
prefix directly. A provider that caches matching prefixes automatically
still needs the stable bytes to appear before per-turn context.

## Global Section Policy

Prompt assembly emits named sections with retained metadata. The section
name is the stable contract for caching policy, request-detail forensics,
and regression tests.

| Section                 | Stability | Rationale |
|-------------------------|-----------|-----------|
| `PERSONA`               | stable    | Identity. Changes only when persona files are edited. |
| `EGO`                   | stable    | Self-reflection file. Stable across long sessions. |
| `RUNTIME CONTRACT`      | stable    | Execution semantics. Model-invariant. |
| `INJECTED CONTEXT`      | stable    | Mission + configured core files. Stable across a session. |
| `TOOL CALLING CONTRACT` | stable    | Model-family-specific tool-calling guidance. |
| `TALENTS ALWAYS ON`     | stable    | Behavioral guidance. Stable across a session. |
| `TALENTS TAGGED`        | semi-stable | Tag-scoped talents. Can change when tags flip or talent files are edited. |
| `ACTIVE CAPABILITIES`   | volatile  | Derived from active tags and tool surface for this run. |
| `TAGGED GUIDANCE`       | volatile  | Tagged KB articles and guidance-oriented providers. Changes with active tags and disk-backed docs. |
| `CONTINUITY CONTEXT`    | volatile  | Channel, session, working-memory, and other continuity providers. |
| `RELATED CONTEXT`       | volatile  | Request/wake-related retrieval providers. Query-sensitive. |
| `LIVE STATE`            | volatile  | Current operational/world state providers. |
| `CURRENT CONDITIONS`    | volatile  | Time, host, version, branch, and uptime. Changes every turn. |
| `CONVERSATION HISTORY`  | volatile  | Append-only, but the latest turn must remain visible. |
| `CONTEXT USAGE`         | volatile  | Per-turn token counts and request metadata. |

Stable and semi-stable sections should appear before volatile sections.
Volatile sections should not receive provider cache markers unless the
provider has a mechanism that can tolerate per-turn mutation without
invalidating the stable prefix.

The typed context buckets (`TAGGED GUIDANCE`, `CONTINUITY CONTEXT`,
`RELATED CONTEXT`, and `LIVE STATE`) each enforce their own 64 KB cap.
That is deliberate: truncating one noisy bucket must not suppress the
other buckets. The tradeoff is an explicit ceiling expansion from one
64 KB aggregate context block to as much as 256 KB across the four
volatile buckets, so new buckets should justify both their ordering and
their prompt-budget impact.

## Adding a New Section

When adding a new system-prompt section, classify it before choosing any
provider-specific cache setting.

1. **Does it change on every turn?**
   Mark it volatile and place it after stable guidance.

2. **Does it change by external input or active tags?**
   Mark it semi-stable if it is still mostly reusable inside a short
   session. Keep it near related guidance, not after per-turn state.

3. **Is it effectively static across a session?**
   Mark it stable and place it in the cached prefix.

Anything tied to wall-clock time, current request text, live provider
state, conversation tail, or token accounting is volatile.

## Provider Adapters

Provider adapters translate section stability into the provider's native
cache controls. Do not bake provider-specific cache language into the
global prompt assembly contract unless the field is explicitly named as
provider-specific.

Today, `llm.PromptSection.CacheTTL` is Anthropic-shaped (`"1h"`, `"5m"`,
or empty). Treat it as the current adapter field, not the long-term
semantic model. A future provider-neutral shape should represent section
stability first, then let provider adapters derive their cache behavior.

## Anthropic

Anthropic exposes explicit prompt-cache breakpoints through
`cache_control`. Thane maps system-prompt sections to Anthropic TTLs in
[`internal/runtime/agent/loop.go`](../internal/runtime/agent/loop.go) via
`promptSectionCacheTTL`.

### TTL Mapping

| Section                 | Anthropic TTL | Rationale |
|-------------------------|---------------|-----------|
| `PERSONA`               | 1h            | Identity. Changes only when persona files are edited. |
| `EGO`                   | 1h            | Self-reflection file. Stable across long sessions. |
| `RUNTIME CONTRACT`      | 1h            | Execution semantics. Model-invariant. |
| `INJECTED CONTEXT`      | 1h            | Mission + configured core files. Stable across a session. |
| `TOOL CALLING CONTRACT` | 1h            | Model-family-specific tool-calling guidance. |
| `TALENTS ALWAYS ON`     | 1h            | Behavioral guidance. Stable across a session. |
| `TALENTS TAGGED`        | 5m            | Tag-scoped talents. Can change per turn if tags flip. |
| all volatile sections   | none          | Per-turn content would churn the cached prefix. |

Tools get a blanket `1h` cache marker on the last tool definition in
[`internal/model/fleet/providers/anthropic.go`](../internal/model/fleet/providers/anthropic.go).

### Minimum Cacheable Prefix Length

Anthropic silently ignores cache breakpoints on prefixes that fall below
a per-family threshold:

| Family            | Minimum tokens |
|-------------------|----------------|
| Claude Sonnet 4.x | 1024           |
| Claude Opus 4.x   | 4096           |
| Claude Haiku 4.x  | 4096           |

Thane enforces this in `applyCacheBreakpointGuards`: under-minimum runs
have their `cache_control` stripped at request time with a WARN log.
Unknown model families default to the strictest minimum.

### The 4-Breakpoint Cap

Anthropic rejects requests carrying more than four `cache_control`
markers total across system blocks, tools, and messages. Today's policy
normally emits two system breakpoints plus one tool breakpoint.

The guard in `applyCacheBreakpointGuards` drops excess breakpoints
before the request is sent. It drops the blanket tool breakpoint first,
then trims trailing system breakpoints. Every drop logs a WARN so
operators can see why the cache did not apply.

### Anthropic Anti-Patterns

- Putting `CacheTTL` on changing content such as `CURRENT CONDITIONS`,
  `LIVE STATE`, or `CONTINUITY CONTEXT`.
- Fragmenting stable sections into many TTL runs. Each TTL transition
  can create a breakpoint.
- Assuming four breakpoints is plenty. Tools already take one slot.
- Ignoring minimum prefix lengths. A too-short breakpoint is a no-op.

## Future Providers

Future provider adapters should preserve the global section ordering and
stability model, then translate it into that provider's native mechanism.

For providers with automatic prefix caching, the main requirement is
stable byte ordering: keep the reusable prefix identical across turns and
move volatile data later. For providers with explicit cache controls,
map section stability to the provider's native marker, TTL, or retention
policy. For providers without prompt caching, the same section metadata
still helps request-detail forensics and prompt regression tests.

Do not copy Anthropic's `CacheTTL` values into another adapter unless
that provider actually has matching semantics.

## Validating Caching

Provider-specific metrics differ, but useful validation usually asks:

- Are stable prompt bytes reused after the first turn?
- Do volatile sections avoid invalidating the stable prefix?
- Are cache read/write token counts visible in request logs or usage
  telemetry?
- Does a multi-turn session show improving cache hit behavior after the
  cold start?

For Anthropic today, inspect:

- `cache_hit_rate` on Anthropic debug log lines
- `cache_hit_rate` in the session stats JSON served by `/stats`
- raw `cache_creation_input_tokens` and `cache_read_input_tokens`

## References

- [Anthropic prompt caching docs](https://platform.claude.com/docs/en/docs/build-with-claude/prompt-caching)
- [`internal/runtime/agent/loop.go`](../internal/runtime/agent/loop.go) — section ordering and `promptSectionCacheTTL`
- [`internal/model/fleet/providers/anthropic.go`](../internal/model/fleet/providers/anthropic.go) — Anthropic cache-control enforcement
- [`internal/model/llm/types.go`](../internal/model/llm/types.go) — prompt section and usage types
- [`internal/platform/usage/store.go`](../internal/platform/usage/store.go) — per-TTL cost breakdown
