# The Tag System

Tags are how Thane decides what tools, talents, and knowledge are
available to a given iteration of a given loop. Almost every "what
can the agent see right now?" question routes through them. They are
also the most-touched part of the codebase by a long sequence of
agent-driven refactors, and the model has accumulated parallel paths,
overloaded names, and load-bearing implicit conventions over time.

This doc exists so that the next change to anything tag-related has a
single map to read first — concept, code, smell, and cross-reference
in one place. It is deliberately a narrative companion to the
canonical artifacts, not a replacement for them. Where another doc or
a `doc.go` already owns a topic, this one points there.

## What's a tag, in one paragraph

A capability tag is a string (`ha`, `forge`, `development`,
`msrhouston`) that gates the per-iteration view of the system: which
tools are in the catalog, which talents are folded into the system
prompt, which KB articles load, which providers inject context. Tags
are mutable during a run — the model activates and deactivates them
through `activate_capability` / `deactivate_capability` — and a
subset persists per conversation through the
[`CapabilityTagStore`][cap-store]. A tag is *not* a tool, *not* a
behavioral mode, *not* a routing parameter, and *not* a configuration
profile, even though all of those things sit nearby in the code and
sometimes share vocabulary.

[cap-store]: ../../internal/runtime/agent/capability_scope.go

## Concept matrix

The single biggest source of confusion in this part of the codebase
is that several distinct ideas wear similar names. This table is the
authoritative disambiguation; if a future change blurs any of these
boundaries, that's a smell.

| Term | What it actually is | Lives in | Distinct because |
|------|---------------------|----------|------------------|
| **Tag** / **capability tag** | Mutable runtime gate string. | [`capabilityScope`][scope] active map. | Per-conversation, model-mutable, gates tool/talent/KB visibility. |
| **Capability** | Linguistic synonym for "tag." No dedicated type. | Tool names (`activate_capability`), docs. | Use "tag" in new code; "capability" remains in tool names for stability. |
| **Lens** | Persistent global behavioral mode. | `lens_tools.go`, opstate. | Survives restarts, applies across all conversations, shapes prompt rather than gating tools. |
| **Talent** | Markdown content gated by tags. | `talents/*.md`, `talents.Loader`. | Static content, not executable; loaded into the system prompt when its declared tags are active. See [Context Layers](context-layers.md). |
| **Scope** / **capabilityScope** | The runtime tag-set object for one `agent.Run()`. | [`capability_scope.go:51`][scope]. | Lives in context; mutated by tools; not directly persisted. |
| **AlwaysAvailable** | A boolean flag on a `Tool`. | [`tools.Tool.AlwaysAvailable`][always-available]. | Tool-level, not tag-level. Survives all tag filters. Used for meta-tools (`activate_capability` itself, etc.) and for `RuntimeTools`. |
| **Always-active tags** | Tags pinned in every scope by config. | [`Executor.SetAlwaysActiveTags`][exec-always], config `capability_tags.*.always_active`. | Tag-level, not tool-level. Re-seeded each run. |
| **Delegate profile** (`delegate.Profile`) | Operational bundle for a delegated task: max iterations, max duration, token budget, tool timeout, default tags, router hints. | [`delegate/profile.go`][delegate-profile]. | Operational *constraints*. No relation to tool gating beyond `DefaultTags`. |
| **Loop profile** (`router.LoopProfile`) | Routing/behavior bundle: model selection, mission, quality floor, instructions. | [`router/loopprofile.go`][loop-profile]. | Routing and prompt-shaping. No relation to tool gating except via `ExcludeTools`. |
| **Routing profile** | A user-facing model name (`thane:premium`, `thane:ops`). | [Routing Profiles](../operating/routing-profiles.md). | Selected by Ollama-API model name; resolves to a `LoopProfile`. |
| **Mission** | Routing-hint string identifying task context (`conversation`, `automation`, `metacognitive`). | `LoopProfile.Mission`. | Pure routing input. Not a tag, not a tool gate. |
| **Configured tags** | Read-only snapshot of the configured tag inputs for the run: loop config `Tags` plus request-base/request-override `InitialTags`. | [`loop/tooling.go`][configured-tags] (`ToolingState.ConfiguredTags`). | Read-only telemetry. Distinct from active tags so the dashboard can show "what was configured at launch" vs "what became active." Introduced in [#813][pr-813]. |

[scope]: ../../internal/runtime/agent/capability_scope.go
[always-available]: ../../internal/tools/tools.go
[exec-always]: ../../internal/runtime/delegate/delegate.go
[delegate-profile]: ../../internal/runtime/delegate/profile.go
[loop-profile]: ../../internal/model/router/loopprofile.go
[configured-tags]: ../../internal/runtime/loop/tooling.go
[pr-813]: https://github.com/nugget/thane-ai-agent/pull/813

The glossary in [docs/understanding/glossary.md](glossary.md) covers
the user-facing definitions of *Capability Tag*, *Lens*, *Talent*,
and *Routing Profile*. This doc is the technical companion that adds
the disambiguations the glossary doesn't need to make.

## The lifecycle

The canonical narrative lives in
[`internal/runtime/loop/doc.go`][loop-doc] under "Capability tag
lifecycle." Read that for the full chain. The one-paragraph summary:

A loop's tags flow `Spec.Tags → requestBase.InitialTags →
loop.Request.InitialTags (merged with Launch.InitialTags +
activatedTags from the prior iteration) → agent.Request.InitialTags →
scope.Request(tag) per tag → scope.Snapshot() during the run →
agent.Response.ActiveTags → fed back into activatedTags`. Each layer
is deliberate, not redundant: it's the state of the same concept at a
different point in the loop's life.

[loop-doc]: ../../internal/runtime/loop/doc.go

## Tool catalog construction

This is the part that has *no canonical doc anywhere else* and is
where today's bugs keep surfacing. The order of operations:

```
parent registry  ──┐
                   ├─ FilteredCopy(req.AllowedTools)         (allowlist by name, if set)
                   ├─ WithRuntimeTools(req.RuntimeTools)     (request-scoped tools, marked AlwaysAvailable)
                   ├─ FilteredCopyExcluding(req.ExcludeTools)(blocklist by name)
                   │
                   ▼
   baseTools  ─── currentTools() per iteration ──┐
                                                 │
                  scope.Snapshot()  →  FilterByTags(activeTags)
                                                 │
                  if gating active: FilteredCopy(orchestratorTools)
                                                 │
                                                 ▼
                                        effective_tools logged
                                        on loop_llm_start
```

Code path:
[`agent/loop.go:1770–1800`][loop-base] builds `baseTools`,
[`agent/loop.go:1908`][loop-current] is `currentTools()` (the
per-iteration recompute), [`agent/loop.go:1970`][loop-effective] is
`effectiveToolNames()` (the list emitted to the event log).
[`tools/tools.go:1038`][filter-by-tags] is `FilterByTags`, which
explicitly preserves any tool with `AlwaysAvailable == true` even if
its tag is not active.

[loop-base]: ../../internal/runtime/agent/loop.go
[loop-current]: ../../internal/runtime/agent/loop.go
[loop-effective]: ../../internal/runtime/agent/loop.go
[filter-by-tags]: ../../internal/tools/tools.go

### Filtering knobs, all of them

| Knob | Type | Set by | Effect |
|------|------|--------|--------|
| `req.AllowedTools` | allowlist | request | If non-empty, only these tools survive. |
| `req.RuntimeTools` | layer | request | Adds request-scoped tools, all marked `AlwaysAvailable`. |
| `req.ExcludeTools` | blocklist | request | Removes named tools from the catalog. |
| `req.SkipTagFilter` | bypass | request | Disables the tag-based filter entirely (used by metacognitive). |
| Tag filter | filter | scope | `FilterByTags(scope.Snapshot())` per iteration. |
| `Tool.AlwaysAvailable` | preservation | tool definition | Survives the tag filter. |
| `delegateFamilyToolNames` | blocklist | delegate executor | Hard-coded recursion guard. Applied at *both* layers (see below). |
| Orchestrator gating (`orchestratorTools`) | filter | runtime config | When delegation gating is active, restrict the catalog to orchestrator-only meta-tools. |

The combinatorics are deliberate but easy to mis-compose. Always-on
preservation is at the *tool* level; always-on tagging is at the
*tag* level; explicit exclusions can override either. When in doubt,
read the test for the case you're touching.

### The two-layer delegate exclusion (this is load-bearing)

The `delegateFamilyToolNames` slice is the recursion guard that
prevents a delegate from spawning further delegates of its own. It must
contain every registered delegation front door: `thane_now`,
`thane_assign`, and the deprecated `thane_delegate` alias on branches
that still register that compatibility shim. **It must be applied at
two levels**, and getting either one wrong breaks the guard silently.

**Layer 1 — in-process registry filter.** [`delegateToolRegistry`][delegate-tool-registry]
applies `FilteredCopyExcluding(delegateFamilyToolNames)` to the
parent registry view. This affects what the delegate path sees during
its own setup phase (e.g., model-routing sizing in
[`selectModel`][select-model]).

**Layer 2 — request-level exclusion.** The delegate's
`prep.excludeTools` is propagated into `Launch.ExcludeTools` and
ultimately `agent.Request.ExcludeTools`, which the agent loop applies
via `FilteredCopyExcluding`. This affects what the launched loop's
catalog actually contains. After [`eebdf2c`][commit-eebdf2c] retired
the legacy in-process delegate path, **this is the load-bearing
layer for the production catalog.**

The reason both layers exist: the in-process filter is what the
delegate executor sees while preparing the launch (used for routing
decisions), and the request-level exclusion is what survives into the
launched loop's runtime catalog. Patching only one looks correct in
unit tests but lets the family leak through in production. This is
exactly how the [#820][issue-820] regression went undetected — the
fix in [#828][pr-828] only patched layer 1, and the production
catalog still carried the family until [#833][pr-833] added it to
layer 2 as well.

**If you add a new tool to `delegateFamilyToolNames`, no further
change is needed** — the slice is iterated at both layers. If you
discover a *new* category of tool that needs the same recursion
guard, add it to the same slice or create an analogous list and apply
it in both places. There is currently no helper that expresses "apply
exclusion X at both layers"; consider [#TODO][followup-helper] when
making that change.

[delegate-tool-registry]: ../../internal/runtime/delegate/delegate.go
[select-model]: ../../internal/runtime/delegate/delegate.go
[commit-eebdf2c]: https://github.com/nugget/thane-ai-agent/commit/eebdf2c
[issue-820]: https://github.com/nugget/thane-ai-agent/issues/820
[pr-828]: https://github.com/nugget/thane-ai-agent/pull/828
[pr-833]: https://github.com/nugget/thane-ai-agent/pull/833
[followup-helper]: https://github.com/nugget/thane-ai-agent/issues

## The capability scope

A `capabilityScope` is the runtime data structure for one
`agent.Run()`. It owns the active and pinned tag maps and is mutated
by tool handlers via `Request(tag)` and `Drop(tag)`. The scope is
created at the start of each run and lives in context — there is no
shared scope across concurrent runs.

Three things to know:

**Pinning vs. activation.** Activated tags can be dropped by the
model. Pinned tags cannot — they're set by the channel binding (e.g.
`owner` for owner-pinned channels) or by lens injection, and persist
for the run regardless of model behavior.

**Persistence is per-conversation, not per-loop.** When the loop
ends, the scope's user-activated tags are saved via
[`CapabilityTagStore.SaveTags`][save-tags] keyed by conversation ID.
The next run for that conversation re-seeds them via `LoadTags`.
Always-active and pinned tags are re-seeded from config and channel
binding each run, not from the store.

**The model sees only the tags, not the scope object.** Tools that
inspect the scope (`list_loaded_capabilities`, `inspect_capability`)
return the tag set as strings; the scope object itself is not
visible.

[save-tags]: ../../internal/runtime/agent/capability_scope.go

## Delegation tag handling

[`docs/understanding/delegation.md`](delegation.md) owns the
user-facing story (Tool Gating, Delegation Profiles, etc.). The
technical detail this doc adds:

When a delegate is spawned via `thane_now` / `thane_assign`,
[`mergeDelegateScopeTags`][merge-scope] composes the delegate's
initial tag set from three sources: (1) the caller's inheritable tags
(if `inherit_caller_tags` is true), (2) the explicit `tags` argument
on the tool call, and (3) the profile's `DefaultTags` if no explicit
scope was requested. Some tags never propagate to delegates
(currently `message_channel`, `owner`) — see the dropped-tag handling
in that function for the up-to-date list.

Delegates run in their own loop, their own scope, and their own
catalog. They do not share `capabilityScope` with the parent. Tag
inheritance happens once at spawn time; subsequent activations in
either parent or child are independent.

[merge-scope]: ../../internal/runtime/delegate/delegate.go

## Smells worth knowing about

Honest list. Each is something to be aware of when changing nearby
code; some are queued as cleanup work.

1. **Profile naming is overloaded.** `delegate.Profile` (operational
   constraints) and `router.LoopProfile` (routing hints) are unrelated
   structs that share the term "profile." A consolidating rename to
   `delegate.DelegateConfig` or `delegate.Constraints` would resolve
   the ambiguity. Tracked as a follow-up.

2. **Tag merging is duplicated.** [`mergeDelegateScopeTags`][merge-scope],
   the inline merge in
   [`agent/loop.go:1620+`][loop-merge-inline], and a simple dedup
   helper [`mergeTagLists`][merge-tag-lists] all do variations of
   "combine these tag slices and dedup." A unified helper would
   reduce drift risk. Tracked as a follow-up.

3. **`configured_tags` is undocumented user-facing.** Introduced in
   [#813][pr-813] for telemetry/forensics, but the concept lives in a
   field comment. Worth a glossary entry once we're sure the
   user-facing surface is stable.

4. **Test coverage is path-asymmetric.** Today's bug ([#833][pr-833])
   existed because the empty-scope delegate path was tested but the
   tag-scoped path wasn't. Several other branch-condition pairs in
   [`prepareExecution`][prepare-execution] have similar asymmetry —
   worth a sweep.

5. **AlwaysAvailable has no architectural doc.** The flag exists on
   `Tool`, the docstring says "Survives capability tag filtering,"
   but the *why* (meta-tools must survive the filter so the model can
   navigate, runtime tools join via this mechanism) lives in scattered
   comments. A short note in this doc or in `tools/doc.go` would
   close the gap.

6. **Two-layer exclusion has no helper.** Documented above, but
   currently expressed only as "remember to do this in both places."
   A helper that returns both the in-process filter and the
   request-level exclusion list would make the invariant
   self-enforcing. Tracked as a follow-up.

[loop-merge-inline]: ../../internal/runtime/agent/loop.go
[merge-tag-lists]: ../../internal/runtime/delegate/delegate.go
[prepare-execution]: ../../internal/runtime/delegate/delegate.go

## Where to look first when changing tag-related code

| If you're changing… | Read first |
|---------------------|-----------|
| The tag lifecycle (any field from Spec.Tags through Response.ActiveTags) | [`internal/runtime/loop/doc.go`][loop-doc] — capability tag lifecycle |
| Tool catalog filtering or visibility | This doc, "Tool catalog construction" |
| Delegate tool exclusion / recursion guards | This doc, "The two-layer delegate exclusion" |
| Talents, KB articles, persona, system prompt assembly | [`docs/understanding/context-layers.md`](context-layers.md) and [`docs/model-facing-context.md`](../model-facing-context.md) |
| Tool descriptions, schemas, model-facing tool surface | [`docs/model-facing-tools.md`](../model-facing-tools.md) |
| `activate_capability` / `deactivate_capability` semantics | [`internal/tools/capability_tools.go`][cap-tools] and the scope code |
| Lenses (global behavioral modes) | [`internal/tools/lens_tools.go`][lens-tools] and the [glossary entry](glossary.md#lens) |
| Routing / model selection / quality floor | [`docs/operating/routing-profiles.md`](../operating/routing-profiles.md) |

[cap-tools]: ../../internal/tools/capability_tools.go
[lens-tools]: ../../internal/tools/lens_tools.go

## Related

- [Agent Loop](agent-loop.md) — what each iteration does end to end
- [Delegation](delegation.md) — tool gating from the delegation perspective
- [Context Layers](context-layers.md) — talents, persona, core context
- [Glossary](glossary.md) — user-facing definitions
- [Routing Profiles](../operating/routing-profiles.md) — model selection
- [`internal/runtime/loop/doc.go`][loop-doc] — capability tag lifecycle (canonical)
