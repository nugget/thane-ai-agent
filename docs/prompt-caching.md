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
| `AXIOMS`                | stable    | Highest-level preamble. Changes only when axioms.md is edited. |
| `PERSONA`               | stable    | Identity. Changes only when persona files are edited. |
| `MISSION`               | stable    | Durable mission framing. Changes only when mission.md is edited. |
| `EGO`                   | stable    | Self-reflection file. Stable across long sessions. |
| `INJECTED CONTEXT`      | stable    | Supplemental configured core files. Stable across a session. |
| `RUNTIME CONTRACT`      | stable    | Execution semantics. Model-invariant. |
| `TOOL CALLING CONTRACT` | stable    | Model-family-specific tool-calling guidance. |
| `TALENTS ALWAYS ON`     | stable    | Behavioral guidance. Stable across a session. |
| `TALENTS TAGGED`        | semi-stable | Tag-scoped talents. Can change when tags flip or talent files are edited. |
| `ACTIVE CAPABILITIES`   | volatile  | Derived from active tags and tool surface for this run. |
| `TAGGED GUIDANCE`       | volatile  | Tagged KB articles and guidance-oriented providers. Changes with active tags and disk-backed docs. |
| `CONTINUITY CONTEXT`    | volatile  | Channel, session, working-memory, and other continuity providers. |
| `RELATED CONTEXT`       | volatile  | Request/wake-related retrieval providers. Query-sensitive. |
| `LIVE STATE`            | volatile  | Current operational/world state providers. |
| `CURRENT CONDITIONS`    | volatile  | Time, host, version, branch, and uptime. Changes every turn. |
| `CONTEXT USAGE`         | volatile  | Per-turn token counts and request metadata. |

Conversation history is not a system-prompt section: it rides in the
`messages[]` array (PR #852), where providers with prefix caching cover
it through the message-level cache breakpoints described below.

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

Fixed core prompt files (`axioms.md`, `persona.md`, `mission.md`,
`ego.md`, and any configured supplemental injected file) share the same
read/verify/frontmatter-strip/truncate mechanics. Their cache policy
follows the section they render into, not a separate file-specific path.

Between tool-loop iterations, a generated prompt refresh replaces both
the plain text and its structured sections. Providers that serialize
sections therefore see the same updated capabilities, guidance, and live
state as providers that consume plain text. The initial context-usage
estimate is omitted after a refresh because it describes the earlier
prompt. Caller-supplied system prompts remain intact across iterations
and are represented as a section without an explicit cache TTL.

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
| `AXIOMS`                | 1h            | Highest-level preamble. Changes only when axioms.md is edited. |
| `PERSONA`               | 1h            | Identity. Changes only when persona files are edited. |
| `MISSION`               | 1h            | Durable mission framing. Changes only when mission.md is edited. |
| `EGO`                   | 1h            | Self-reflection file. Stable across long sessions. |
| `INJECTED CONTEXT`      | 1h            | Supplemental configured core files. Stable across a session. |
| `RUNTIME CONTRACT`      | 1h            | Execution semantics. Model-invariant. |
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

### Conversation History

Requests worth caching also carry Anthropic's automatic, request-level
`cache_control`. It lands on the last message block and moves forward
as the conversation grows, so each call in a tool loop reads the
transcript the previous call wrote and pays full input price only for
what is new since then. It composes with the explicit system and tool
markers: the explicit markers pin the stable prefix, the automatic one
follows the tail. Without it, a long tool loop re-sends its whole
growing transcript as uncached input on every iteration.

`anthropicPromptCacheControl` decides which requests are worth it. The
automatic breakpoint is sent when the system prompt carries explicit
markers, or when `shouldUseAnthropicPromptCaching` sees any of: tool
definitions, three or more messages, an assistant turn, or a system
prompt of at least 4096 characters. A short one-shot request with none
of those goes out without it, because there is nothing a later call
would read back.

It keeps the default 5m TTL. Anthropic requires longer-TTL entries to
precede shorter ones, and the tail sits after the `5m` system run.

### The 4-Breakpoint Cap

Anthropic rejects requests carrying more than four `cache_control`
markers total across system blocks, tools, and messages, and the
automatic breakpoint counts as one. Today's policy normally emits two
system breakpoints, one tool breakpoint, and the automatic one when the
request qualifies for it.

The guard in `applyCacheBreakpointGuards` reserves the automatic slot
when one is being sent, then drops excess explicit breakpoints before
the request is sent. It drops the blanket tool breakpoint first, then
trims trailing system breakpoints. Each over-cap drop logs a WARN,
since exceeding the cap means the assembly plan changed. Under-minimum
drops log at DEBUG instead: a short section is a structural fact of
the prompt that recurs on every request. Every drop, at either level,
is also listed on the debug `outbound cache markers` line.

### Anthropic Anti-Patterns

- Putting `CacheTTL` on changing content such as `CURRENT CONDITIONS`,
  `LIVE STATE`, or `CONTINUITY CONTEXT`.
- Fragmenting stable sections into many TTL runs. Each TTL transition
  can create a breakpoint.
- Assuming four breakpoints is plenty. Tools and the conversation tail
  already take two.
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

Usage is priced per model call, including provider-reported usage from
retries, partial failures, and forced-response recovery. The durable
ledger and live API statistics use the same per-call cost, with separate
5-minute and 1-hour cache-write rates. Request token totals sum those
calls; changing models during a request does not reprice earlier work.
Calls without reported usage and static fallback text create no usage
record. A failed request retains the usage already reported.
Usage absent before an interruption cannot be reconstructed.

The ledger also captures reported chat-completion usage for compaction, fact
extraction, session summaries, media summaries, and vision analysis. These
records have an `auxiliary` role and a separate `purpose`; they do not inflate
the live API request statistics. New records preserve full session and
conversation identifiers and, when the work originates in a loop, its ID,
name, and parent ID. Each record includes the individual call's outcome and
elapsed milliseconds, including partial responses from failed calls. Loop
identity is recorded at execution time; it is never reconstructed from a
conversation's name. Incoming Signal vision is attributed to the attachment's
conversation before the agent turn starts. Tool-requested analysis instead
retains the caller's conversation when one is supplied.

`pricing_status` distinguishes configured prices (`priced`, including an
explicit zero rate) from missing prices (`unpriced`). Historical records have
unknown pricing coverage and keep their stored costs. Summary responses and
`cost_summary` expose priced, unpriced, and unknown record counts. A zero cost
with missing pricing is not evidence of free usage. Totals retain event-time
costs and are not recalculated when prices change. Missing prices make cost
coverage incomplete; unknown historical coverage makes it uncertain without
establishing that any cost is missing. Configured zero API rates count as
priced, but do not measure hardware, energy, or capacity costs. Inspect token
volume independently of dollars when assessing resource demand.

Live API session statistics also expose `priced_records`, `unpriced_records`,
and `unknown_pricing_records` alongside the top-level `estimated_cost_usd`,
as well as in each breakdown. These counts cover reported API model calls
since server startup, including failed requests; auxiliary calls remain in
the durable ledger. Use these counts to distinguish configured zero-cost
usage, missing prices, and uncertain pricing coverage.

This is reported-usage accounting, not a complete invoice or an attempt log:
calls without provider token counters and embedding calls are outside this
ledger. Older rows cannot recover missing loop identity or full session IDs.

`system_health` and the metacognitive panel expose the same cached `spend`
snapshot: trailing 24-hour usage, the preceding 24 hours, pricing coverage,
and unattributed usage. Start with the recent window's `by_provider` and
`by_role` headlines, each containing up to three groups. `top_loops` is
secondary direct-attribution detail; its shares use the full recent
recorded-dollar total, not a complete invoice. Missing loop identity does
not establish non-loop work. Collection starts at boot and refreshes every
five minutes with a five-second timeout;
rendering this spend block does no ledger work. Check its state and `sampled_ago`:
pending or unavailable has no measurement, while stale retains the last good
windows. `comparison.recorded_cost_change_usd` subtracts stored totals when
both windows contain records. Incomplete coverage can change that delta; it
is not a bound on the real cost change. The separate `comparison.cost_change_usd` and
percentage require complete known pricing in both windows; a zero prior
cost leaves percentage change undefined. The health latency rollup is a
request-log-span estimate, not GPU or model execution time. These are
observations for baseline judgment, with fresh detail available through
`cost_summary`. See [recorded spend in system health](reference/tools.md#recorded-spend-in-system-health)
for states, bounds, and truncation.

`cost_summary` returns typed JSON for a selected time window. Use
`{"loop_id":"self","since":"-3600s"}` inside a loop to inspect its
last hour. Use `group_by: "provider"` or `group_by: "role"` for the overall
distribution, `group_by: "loop_name"` to combine exact captured names
across restarts, or `group_by: "loop"` to discover separate lifetimes.
Name grouping combines reused names and splits renamed records; it does
not establish persistent identity. An exact recorded `loop_id` remains
queryable after the instance stops; `loop_name` selects the captured name across matching lifetimes rather
than resolving only the current live instance. Query the ID to include usage
captured under other names. Loop selectors default to separate loop-ID groups
whose `key` is the ID and whose `loop_name` is the latest captured label in
the selected window; an explicit `group_by`, such as `model`, breaks down
the selected usage instead. These are direct recorded totals, not descendant
rollups or reconstructed historical attribution.

The summary covers every matching record even when the bounded group list
is truncated. Global loop discovery reports unattributed usage separately
instead of guessing a loop owner. Inspect pricing coverage with the stored
cost estimates: no matching records is not evidence of no cost. See
[cost and usage queries](reference/tools.md#cost-and-usage-queries) for
selectors, custom windows, and response limits.

Usage summaries and `cost_summary` count records,
while live API `total_requests` counts successful logical requests.
Historical records written before per-call accounting can contain
several iterations in one row; their counts cannot recover call totals.

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
- `request_cache_control_ttl` on the debug `outbound cache markers`
  line: `default` means the conversation tail carries the automatic
  breakpoint. Empty is expected on a short one-shot request; on a
  tool-bearing or multi-turn request it means history is being re-sent
  uncached

## References

- [Anthropic prompt caching docs](https://platform.claude.com/docs/en/docs/build-with-claude/prompt-caching)
- [`internal/runtime/agent/loop.go`](../internal/runtime/agent/loop.go) — section ordering and `promptSectionCacheTTL`
- [`internal/model/fleet/providers/anthropic.go`](../internal/model/fleet/providers/anthropic.go) — Anthropic cache-control enforcement
- [`internal/model/llm/types.go`](../internal/model/llm/types.go) — prompt section and usage types
- [`internal/platform/usage/store.go`](../internal/platform/usage/store.go) — per-TTL cost breakdown
