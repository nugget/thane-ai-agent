# 2026-09 System Prompt & Context Burn Audit

Periodic production audit of what enters Thane's model context and what
it costs: the talents/trailheads prose corpus, the Anthropic
prompt-caching strategy on cloud turns, and the generated context
assembly. Companion to the process doc in
[`tool-and-talent-audit.md`](../tool-and-talent-audit.md) (which audits
routing correctness); this cycle audits **token economy, tone
consistency, and cache behavior**.

Method: nine parallel audit passes (seven prose groups covering all 39
talent files, one caching pass over the Anthropic provider code and
policy doc, one assembly pass over the runtime context providers), each
pass's findings adversarially re-verified against the repo before
inclusion, and a completeness critic run over the verified set. All
106 findings survived verification with zero refutations. Live Anthropic
prompt-caching documentation (minimum cacheable prefixes, TTL pricing,
prefix composition) was consulted for every caching claim; values cited
below are as published 2026-09.

## Headline

**The caching defects cost more than the prose weight.** The talent
corpus is in strong shape — tone is unusually consistent, tool
references resolve, and the trims available are real but incremental
(~22K tokens of per-activation duplication across the corpus). The
Anthropic cache path, by contrast, has four defects that defeat the
strategy the policy doc describes:

1. **The EGO section churns the stable prefix every turn.** A
   minute-granularity freshness delta is interpolated into the cached
   1h-TTL EGO section content
   (`internal/runtime/agent/core_context.go:265`:
   `(updated %s by %s, revision %d)` with `FormatDeltaOnly(...now)`).
   Because cache validity is a byte-exact prefix match, every section
   after EGO in the stable prefix — INJECTED CONTEXT, RUNTIME CONTRACT,
   TOOL CALLING CONTRACT, TALENTS ALWAYS ON — is re-written at the 2×
   1h-write premium on essentially every turn. This single line
   converts most of the "stable" prefix into per-turn cache churn.

2. **The minimum-prefix guard strips breakpoints that would have
   cached.** `minCacheablePrefixTokens`
   (`internal/model/fleet/providers/anthropic.go:843`) guards all Opus
   models at 4096 tokens and Sonnet 4.6 at 2048. Published minimums:
   Opus 4.8 (the production default) is **1024**, Opus 4.7 is 2048,
   only Opus 4.6 is 4096; Sonnet 4.6 is **1024** (the code comment
   "Sonnet 4.6 raised the minimum to 2048" is not supported by current
   docs, and the repo's own `prompt-caching.md` says 1024). The
   Claude 5 generation has no entries at all (Opus 5/Fable 5 are 512;
   Sonnet 5 is 1024) and falls into wrong buckets. An over-strict
   guard is not conservative here — it strips `cache_control` markers
   from prefixes the API would happily cache, silently forfeiting the
   cache.

3. **The guard measures the wrong prefix.** `applyCacheBreakpointGuards`
   (anthropic.go:917-923) computes the prefix from system-block
   characters only, but Anthropic's cacheable prefix is
   `tools → system → messages` — a breakpoint on a system block caches
   the serialized tool definitions ahead of it too. Tool schemas run
   tens of kilobytes, so the true prefix is systematically undercounted
   and valid breakpoints get stripped, compounding defect 2.

4. **Conversation history is never cached on the production path.** No
   message-level `cache_control` is ever emitted, and the sectioned
   path disables the request-level automatic breakpoint
   (`anthropicPromptCacheControl` returns nil when explicit caching is
   on, anthropic.go:754-759). Every turn re-pays full input price for
   the entire conversation tail. `docs/prompt-caching.md` claims
   history is "covered through the message-level cache breakpoints
   described below" (PR #852) — no such breakpoints exist in the code.
   For long interactive sessions this is likely the single largest
   recoverable input-token cost.

## What production pays today (baseline)

Estimates use the repo's own 4-chars-per-token heuristic.

- **Always-on talent prefix** (six persona-tagged foundation files):
  12,556 bytes ≈ **3.1K tokens** in the cached prefix of every persona
  turn. This is a reasonable size for identity prose; the issue found
  is composition, not bulk (see foundation findings — ~460 tokens of it
  is concrete tool doctrine that belongs behind the `memory`/session
  tags).
- **Worst tagged activation**: the `loops` tag co-loads
  loops-trailhead.md + loops.md + loops-tagging.md = 28,849 bytes ≈
  **7.2K tokens** per activation; `loops_examples` adds 26.6KB ≈ 6.7K
  more. The three files carry substantial near-verbatim duplication
  (see below).
- **Multi-node files load per node, not per file** (verified against
  `internal/model/talents/loader.go`) — ha.md's 24.4KB is three
  independent activations (`ha` ≈3.1K, `ha_control` ≈1.1K,
  `ha_automate` ≈1.7K tokens), not one 6.1K-token hit. Economy work
  should target the node, not the file.
- **Trailheads**: median ≈410 tokens; two outliers
  (awareness-trailhead ≈1,320, message-channel ≈970). The
  message-channel weight is earned (unique mid-flight-arrival
  doctrine); the awareness weight is not (it is a parameter reference
  wearing `kind: trailhead`, duplicating loops.md).
- **Identified prose trims**: ~21.9K tokens of per-activation savings
  across 49 economy findings, dominated by cross-file duplication
  rather than verbose writing.

## Theme A — Anthropic caching strategy

Beyond the four headline defects:

- **Tag flips that change the tool surface bypass the 5m firewall.**
  The TALENTS TAGGED section is marked 5m precisely to protect the 1h
  prefix from tag churn, but any tag flip that adds/removes tools
  rewrites the tool definitions — which render at position 0 and
  invalidate *everything*. The firewall only helps for guidance-only
  flips. Worth documenting, and worth considering tool-set
  stabilization (keep the union of recently-active tags' tools) if
  telemetry shows tag flips are frequent mid-session.
- **The blanket 1h TTL is unvalidated.** 1h writes cost 2× base (5m:
  1.25×); reads refresh the timer free on either TTL, so 1h only pays
  off when start-to-start gaps between turns run 5-60 minutes. That is
  plausibly Thane's real cadence (an always-on agent with bursty
  contact), so 1h is likely right — but
  `internal/platform/usage/store.go` already tracks per-TTL cost and
  nothing reports whether the 2× premium is earning reads. Add the
  per-TTL breakeven to `/stats`.
- **Doc/code drift in `docs/prompt-caching.md`**: the section table
  says `ACTIVE CAPABILITIES` where the code emits `ACTIVE TAGS`; the
  `SESSION ORIGIN CONTEXT` section the code emits is absent from the
  table; the doc says under-minimum drops "log a WARN" but the code
  deliberately demoted them to Debug (the code comment explains why —
  update the doc, not the code); and the PR #852 message-breakpoint
  claim described above.
- **Fragile ordering invariants.** `promptCacheRuns` extends a same-TTL
  run across interleaved no-TTL sections, and the 1h-before-5m ordering
  Anthropic requires for mixed TTLs holds today only by construction.
  Both deserve a pinning test so a future section insertion doesn't
  silently break them.

**Recommended sequence** (each its own PR): (1) move the EGO freshness
line out of cached content — into CURRENT CONDITIONS, or coarsen to
day-granularity — it's a one-line fix with the largest cache-hit-rate
impact; (2) update `minCacheablePrefixTokens` to the published
per-family table including the Claude 5 generation, and include an
estimate of serialized tool bytes in the guard's prefix measurement;
(3) add a message-level breakpoint on the last content block of the
most recent turn (the standard multi-turn pattern) so history accrues
into cache; (4) correct `docs/prompt-caching.md`; (5) add per-TTL
breakeven telemetry.

## Theme B — Prose economy

The dominant anti-pattern is **the same doctrine living in two or three
co-loading files**, not verbose writing. The principle for fixes: pick
one canonical home per mechanic; everywhere else gets a one-line
cross-reference with a trigger condition.

Largest verified duplications (tokens are per-activation):

| Doctrine | Currently lives in | Cost |
|---|---|---|
| Stream wiring / wake mechanics (`wake`, debounce, `mqtt_wake_add` routing, area-layering) | awareness-trailhead.md AND loops.md, near-verbatim; both load in every loop-authoring session | ~180 |
| `thane_loop_create` scaffolding, `output.initial`, `loop_definition_set` escape hatch | loops-trailhead.md AND loops.md (co-load on the same tag) | ~100 |
| Facets rationale | three times on one walk of the loops_examples trail | ~150 |
| Publish-interface contract (one arg per projection, over-budget rejected, ceilings) | restated across loops-examples nodes | ~150 |
| Wake-routing three-way fork | restated across loops files | ~120 |
| Working-memory "signature noticing" | working-memory.md (always-on!) AND memory.md | ~70 |
| Persistent-facts vs working-memory split | stated four times in memory.md alone | ~90 |
| Contacts merge mechanics | contacts root AND leaf, near-verbatim | ~100 |
| forge.md read-tool examples | five shape-identical `{repo, number}` JSON literals after the first | ~140 |

Always-on specifics (these ride every persona turn forever):

- **working-memory.md (~460 tokens) is concrete tool doctrine for
  `session_working_memory`** riding in the persona prefix —
  talent-authoring.md's own rule says concrete-tool guidance does not
  belong in foundation prose. Move it behind the tag that carries the
  tool; keep at most a two-line pointer in foundation.
- **presence.md's Anticipations section describes the v0.6
  anticipation engine, scrapped in v0.9** — always-on instructions
  about a subsystem that no longer exists (~75 tokens).
- communication.md duplicates energy-mirroring/anti-summarize doctrine
  with interactive-communication.md (~60).
- foundation.md restates itself (paragraphs 2/5 vs 1/3, ~70) — notable
  because it is the corpus preamble.

## Theme C — Correctness and staleness in prose

These misroute the model rather than merely costing tokens:

- **knowledge-trailhead.md routes semantic refs to `files`**,
  contradicting the files/documents border both leaves feature
  ("if the path has a semantic ref (`kb:`, `dossiers:`), use
  `documents`"). Pre-PR-D fossil; one wasted tag activation per
  mis-route.
- **ha.md never names `ha_automation_create`** — the
  automation-authoring section is the leaf's centerpiece and its tool
  door is missing (the tool exists, catalog.go:236).
- **documents.md's mutate/curate border predates `doc_create`** landing
  in `documents_mutate_content`; the mutate root still routes "new
  knowledge" away.
- **feeds.md never names `media_follow`/`media_unfollow`** — the
  Follow/Stop-following sections give adaptable JSON with no tool name
  to call it with (both exist, catalog.go:288,291).
- **contacts.md contradicts itself on `recipient_trust_zone`**: line
  450 says non-self exports ignore it; the QR example then passes it
  for "Frank Smith" and credits the filtering with keeping the QR
  scannable (lines 476-483).
- **loops-examples.md says "Omit to inherit the core tag set"** where
  the runtime skips tag filtering entirely on an empty set —
  contradicting loops-tagging.md and the code.
- **loops-tagging.md's hard-coded core-tools list is stale** (omits
  `thane_loop_create`, registered `Core: true`) — hard-coded lists of
  live state rot; point at Active Capabilities instead.
- **forge.md's review-loop rides the `gh` CLI** (lines 405-435) with no
  note that this needs the shell surface, which the `forge` tag does
  not itself grant.
- **archive.md's memory-border row says recall is "not text search"**
  — `recall_fact` has a full-text query mode; the row contradicts the
  file's own next paragraph.
- **notifications tag carries `request_ai_escalation`**, the
  documented phantom-symmetry stub (handler returns not-implemented);
  the talent should warn until the tool is dissolved or implemented.

## Theme D — Structure and mechanism

Frontmatter that looks declarative but is inert:

- **`kind: doctrine` is an undeclared value** (loops.md,
  diagnostics.md). `kind.go` declares only `trailhead` (plus the
  deprecated `entry_point` alias); anything else passes through as an
  unrecognized string that merely fails `isTrailheadKind`. It works by
  accident. Either declare a doctrine kind or drop the key; add loader
  validation so unknown kinds are refused like missing tags are.
- **`teaser:` on non-trailhead talents is dead weight** — the menu-hint
  harvester (`KBMenuHints`, tag_context.go:955) hard-gates on
  trailhead kind, so the authored teasers on session.md, shell.md,
  feeds.md, and attachments.md never render anywhere.
- **companion.md declares `kind: trailhead` with no `next_tags` and no
  fork** — the authoring doc's own doctrine-in-disguise case. Same
  diagnosis for awareness-trailhead.md, which is a subscription
  parameter reference with no "choose the next move" fork; its
  mechanics belong in a leaf under the awareness tag with the
  trailhead rewritten to the standard ~400-token chooser shape.
- **next_tags/body divergence** in five trailheads (menu renders
  next_tags as bare names; bodies route to overlapping-but-different
  sets). The regression test checks resolution, not coverage — a
  coverage assertion would catch this class.

## Theme E — Context assembly

- **~650 bytes of static Signal behavioral guidance ride the volatile
  CONTINUITY CONTEXT bucket every turn** as a JSON `note` field
  (channel_provider.go:186,223). Static channel doctrine belongs in
  tagged guidance (cacheable), not per-turn JSON.
- **The CONTEXT USAGE percentage understates reality** — computed from
  messages alone (loop.go:2107) while tool schemas run tens of
  kilobytes; the model reads a % that can be materially wrong.
- **Discriminator drops during materialization are unreported** —
  offers dropped on provider error/overrun aren't added to the
  rendered withheld count, violating the "a capped rail must never
  read as complete" contract in model-facing-context.md.
- Minor: loop self-context uses `json.MarshalIndent` where every
  sibling provider emits compact JSON (~50 tokens/iteration); host/OS/
  version data is recomputed per turn including /proc I/O for
  process-static values; the EGO byline interpolates a commit subject
  after the word "by" (`updated -26h45m by third-write`), promising an
  author and delivering a message.

## Tone assessment

Cross-corpus tone is the audit's good news. All ten trailheads open
trigger-led teasers under 100 characters; eight of ten share the same
skeleton (two-sentence opinionated framing, the "Choose the next move
deliberately:" chooser, "If X, activate `tag`" bullets, a domain-tuned
delegation closer). Doctrine voice is uniformly past-self-to-present-
self imperative. The wobbles worth a sweep, none urgent:

- Third-person slips ("the model", "this agent") in documents.md,
  ha.md, and the contacts/email/notifications trees.
- One deictic slip: loops-trailhead's "the doctrine that follows this
  file" (files don't follow each other in-context).
- Punctuation drift: interactive- and knowledge-trailheads use unspaced
  em dashes where the corpus sets them open.
- message-channel-trailhead mixes posture imperatives into its chooser
  list where peers keep the chooser pure.

## Suggested follow-up sequence

1. **Cache fix PR** — EGO delta out of cached content; minimum-prefix
   table refresh + tools-inclusive measurement; doc corrections.
   Smallest diff, largest recurring savings.
2. **History caching PR** — message-level breakpoint; per-TTL breakeven
   telemetry on `/stats`.
3. **Always-on slimming PR** — working-memory.md → tagged; presence.md
   Anticipations removal; foundation/communication/awareness trims
   (~800 tokens off every persona turn).
4. **Loops-corpus dedup PR** — single canonical home per mechanic
   across the four loops files (~1.3K tokens per loops session).
5. **Correctness sweep PR** — Theme C items; each is a small,
   verifiable prose fix.
6. **Loader/menu mechanics PR** — refuse unknown `kind:` values;
   harvest teasers from any talent (the systemic fix for the
   inert-teaser class — see Cross-group synthesis); next_tags/body
   coverage assertion; a regression test that every tag's primary
   tools are named somewhere in its talent surface.
7. Remaining per-file economy trims batched by cluster (comms, ops,
   home/docs, small leaves) using the appendix below.

## Verification status

Every finding in this report has passed adversarial verification: an
independent pass re-read each cited file at the cited location,
confirmed the quoted evidence, resolved tool names against the catalog,
and re-checked behavior claims in code. Zero findings were refuted
(claims were tightened in several places). The initial run lost five
groups' verifiers to a usage-credit interruption; the high-priority
findings from those groups were hand-verified immediately (anthropic.go
and core_context.go line-by-line, caching claims against current
Anthropic documentation), and the full verifier pass was then re-run to
completion — the caching group's external numbers were additionally
re-validated a third time, independently, by the completeness critic
(per-model minimums, 1.25x/2.0x write and ~0.1x read multipliers, free
timer refresh on read, longer-TTL-first ordering: all confirmed against
the live reference).

## Cross-group synthesis (completeness critic)

Connections no single audit pass could see:

- **The EGO fix is more urgent than either finding conveys.** The
  per-turn delta rewrite (assembly) multiplied by the 2.0x 1h write
  premium (caching) means every turn in the churn window bills the
  entire stable prefix at twice base price with zero cache reads —
  strictly worse than having caching disabled.
- **The inert-teaser class has one systemic fix.** Dead teasers on
  session/shell (ops), feeds/attachments (small_leaves), and
  companion.md wearing `kind: trailhead` just to surface its teaser
  (small_leaves) all trace to the harvester gating on trailhead kind.
  Fixing the leaves by adding `kind: trailhead` would reproduce the
  companion anti-pattern; stripping the teasers discards authored copy.
  The right fix is harvesting teasers from any talent.
- **Three findings, one merge.** communication.md's overlap with
  interactive-communication.md (foundation) and the always-co-loading
  interactive-doctrine/interactive-communication split (comms) resolve
  together in one consolidated interactive-posture file.
- **The awareness knot needs a single owner.** Demoting
  awareness-trailhead.md to doctrine (trailheads) collides with the
  persona file awareness.md already occupying the `awareness` tag's
  conventional doctrine filename (foundation). Executed independently
  these fixes conflict; sequence them with a rename plan.
- **The voice sweep must start at the standards doc.** Third-person
  wobbles were flagged independently by four groups (~15+ instances),
  but docs/talent-authoring.md's own quoted exemplar blesses the
  third-person voice — sweep the corpus after correcting the doc, or
  the drift regenerates on the next authoring pass.
- **One new regression test covers a repeated defect class.**
  "Primary tool never named" appeared twice (ha_automation_create,
  media_follow), and the existing CI tests resolution, not coverage. A
  test asserting every tag's primary tools are named somewhere in its
  talent surface closes the class.
- **Duplicate filings to reconcile when fixing:** the
  ACTIVE CAPABILITIES/ACTIVE TAGS + SESSION ORIGIN CONTEXT doc drift is
  filed by both caching and assembly; the undeclared `kind: doctrine`
  value by both loops and ops. One fix each closes both filings.
- **Critic's own spot-check:** media-trailhead's first fork routes to
  bare `media` — its own tag, not a tool, and absent from its
  next_tags — a routing dead-end the trailheads sweep missed; the
  media tag's surface sits unexamined between the group boundaries.

## Coverage gaps — scope for the next cycle

The completeness critic identified production context this cycle never
examined. In rough priority order:

1. **`internal/model/prompts` (~25KB of model-facing constants)** —
   BaseSystemPrompt, RuntimeContract (in the 1h-cached prefix of every
   turn), the entire delegate-mode prompt surface,
   CoreAttentionReplyContract (~2.5KB beside every loop determination),
   plus compaction/extraction/media/vision/metadata prompts. Zero
   coverage for tone, economy, or staleness — and the delegate prompts
   are exactly the compact prompts the stale minimum-prefix guard
   denies caching to.
2. **The unaudited context providers** — assembly covered 7 of the
   registered providers; at least 8 more always-on ones (calendar
   snapshot, notification history, episodic memory with a configurable
   per-turn token budget, working memory, entity watchlist,
   self-assessment, unverified-trust) plus the tagged set (forge,
   companion devices, older-sessions catalog, metacognitive panel,
   loop subscriptions, HAInject) render into the uncacheable volatile
   tail — the most expensive real estate per token.
3. **An assembled cost baseline** — no total for the stable prefix
   (core files up to ~80KB of operator content + contracts + tool
   schemas + capability manifest) or worst-case tagged activation
   (one encouraged authoring path stacks ~19K tokens of talent prose
   before tool schemas and KB buckets). Without a baseline and
   ceiling, trims can't be prioritized and CI has no budget number to
   regress against.
4. **Doctrine-less destination tags** — `models`, `scheduler`, `owu`,
   `owner` carry tools (some policy-bearing and mutating) but no
   talent, and trailhead next_tags route to them; arrival yields bare
   tool schemas. The next_tags regression test checks resolution, not
   whether the destination carries doctrine.
5. **Tool description and schema prose** — the catalog's ~169 entries
   and ~4.7KB of always-on tag-menu descriptions were never audited as
   prose, though talent-vs-schema duplication was found in both
   directions; dedup that examines only the talent side is one-eyed.
6. **Cache-scoped model routing** — the complexity router can
   alternate models per turn, and Anthropic caches are model-scoped:
   every flip forfeits the whole cached prefix and re-pays the 2x
   write. No finding, telemetry, or doc covers per-model cache
   fragmentation, nor how local providers consume the TTL metadata.
7. **The standards docs themselves** — talent-authoring.md and
   tool-and-talent-audit.md still teach watch_entity/unwatch_entity as
   ghost-tool examples (both now exist) and bless the third-person
   voice the corpus sweep would remove. Every fix from this audit gets
   reviewed against these docs; audit them first.

## Appendix — full findings

Grouped by audit pass; `[priority/category]`, file, location, claim,
recommended fix, and estimated per-activation token savings where the
finding is a size finding. All entries adversarially verified.

### Trailheads (all ten, cross-file)

- **[high/border]** `talents/knowledge-trailhead.md` — first branch bullet, lines 15-16
  - The trailhead routes dossiers and semantic paths to `files`, directly contradicting the files/documents border doctrine both leaves feature at their tops.
  - Fix: Rewrite the first two bullets to match the leaf border: "If the truth has a semantic ref (`kb:...`, `dossiers:...`), activate `documents`." / "If it lives at a raw workspace path, activate `files`." The current wording sends a model holding a kb: ref into `files`, whose own leaf immediately bounces it back — one wasted tag activation per mis-route. Likely architecture-stale from before the PR-D documents restructuring (referenced in docs/talent-authoring.md); the file's own closing line ("Preserve references like `kb:article.md` exactly") sits beside the wrong door.

- **[high/economy]** `talents/awareness-trailhead.md` — feed bullet 2 (Wake) and the mechanics bullets, lines 37-85 *(~180 tok/activation)*
  - Three blocks duplicate loops.md's 'Choose stream wiring by attention cost' section nearly verbatim, and the two files co-inject whenever both tags are active — which the corpus actively encourages: loops-trailhead's next_tags routes loop authors to `awareness`, so the authoring path for any entity-watching loop pays both copies. (A running watcher loop itself typically tags [awareness] without [loops] — e.g. loops-examples.md line 154 — so the double cost lands on the operator/authoring side.)
  - Fix: Pick one canonical home per mechanic. Subscription mechanics belong on the awareness side; cut loops.md's stream-wiring bullet to a two-line pointer ("entity subscriptions are the awareness surface; `wake: true` when the loop should act on change — mechanics live under `awareness`"), or if awareness is slimmed per trailheads-4, invert the direction. Either way, stop paying ~180 tokens twice on every co-activation.

- **[high/economy]** `talents/loops-trailhead.md` — the thane_loop_create paragraph, lines 41-50 *(~100 tok/activation)*
  - The trailhead and loops.md share tags: [loops] so they co-load on every activation, yet both carry the thane_loop_create scaffolding, output.initial seeding, and loop_definition_set escape-hatch doctrine.
  - Fix: Trim the trailhead paragraph to the fork-relevant core: "`thane_loop_create` is the shorter front door to the durable path — one output document, facets, a working-notes document, `dry_run: true` to preview; `loop_definition_set` remains the door when a loop needs more than one published document." Scaffolding and seeding mechanics stay only in the co-loaded doctrine. ~100 tokens saved on every loops activation.

- **[medium/anti-pattern]** `talents/awareness-trailhead.md` — whole file (5281 bytes ≈ 1320 tokens; peer trailheads run 731-1648 bytes ≈ 180-410 tokens)
  - The file is leaf doctrine wearing kind: trailhead — a full parameter reference for the subscription surface with no fork-first structure (only two bounce bullets buried at the end of the mechanics list); of next_tags [ha, home, notifications], only notifications gets a body trigger.
  - Fix: Split: a ~300-token awareness trailhead keeping the three-question frame plus the bounces (one-shot check → visible HA tool; delivery → `notifications`; device work → `ha`), and an awareness-doctrine.md node (awareness.md is taken by the persona foundation file, tags: [persona]; the -doctrine.md suffix is the README-sanctioned disambiguation) holding the parameter mechanics. Co-load makes the split token-neutral, but it restores the trailhead grammar, gives the mechanics a home that trailheads-2's dedup can point at, and stops the model reading a reference manual when it only needed the fork.

- **[medium/assembly]** `talents/operations-trailhead.md` — frontmatter next_tags vs body bullets, lines 5 and 14-46
  - next_tags and body routing diverge in both directions across five group files, and the menu renders next_tags as bare names (render.go:59: "next: %s"), so an entry with no body trigger carries no disqualification test at the decision point.
  - Fix: Align both directions in each file: every body-routed tag joins next_tags (operations should add session, ha, awareness, companion), and every next_tags entry gets a one-line trigger in the body or is dropped (interactive/people: add a message_channel trigger or remove it; message-channel: same for interactive; awareness: same for ha/home). TestRepoTrailheadNextTagsResolve only checks resolution, not coverage, so this drift is invisible to CI.

- **[medium/economy]** `talents/loops-trailhead.md` — ## Where to go next, lines 52-63 *(~70 tok/activation)*
  - The closing section is meta-navigation that references co-loaded content deictically and restates next_tags with content descriptions instead of triggers.
  - Fix: Cut bullet 1 entirely (the doctrine needs no advertisement to a reader who already has it). Convert bullets 3-4 to trigger form or drop them: "Activate `loops_examples` before composing any spec — adapt the closest recipe, don't write from the schema." is the only line that earns the section (and loops.md's own closing line already carries it).

- **[medium/economy]** `talents/development-trailhead.md` — ## Workspace gotchas, lines 34-41 *(~70 tok/activation)*
  - The gotcha section duplicates a border row files.md already features in its top disambiguation table, and short-circuits the file's own 'shell is the escape hatch, activate it first' routing by prescribing a bare exec call (exec is shell-tagged in the catalog, so it needs the activation the bullet above frames).
  - Fix: Delete the section; the shell bullet above it already owns this border. If the sandbox failure mode is worth a trailhead-level warning, fold it into that bullet as a clause: "...activate `shell` — also the door for reads outside the workspace sandbox, where `file_read` fails."

- **[medium/economy]** `talents/operations-trailhead.md` — first branch bullet, lines 15-21 *(~50 tok/activation)*
  - The request_core_attention bullet leads the trailhead with seven lines of mechanism already carried by loops.md and notifications.md, ahead of the routing forks most readers arrive for.
  - Fix: Compress to two lines and move below the diagnostics bullet (the "something is off → `system_health`" path should lead): "A service loop escalating a decision calls `request_core_attention` directly — always available, no tag needed; file the concern and continue." The mechanism and cost doctrine stay in the leaves.

- **[low/economy]** `talents/message-channel-trailhead.md` — ## The conversation can move while you work, lines 43-75 *(~60 tok/activation)*
  - The mid-flight-arrival section (unique, load-bearing content) restates its own framing: the not-a-runtime-instruction point is made twice, and the closing paragraph re-summarizes the pre-bullet framing ("one more thing the person said" echoes "read it as the next thing they said") while its only bullet-derived clause repeats bullet 3.
  - Fix: Keep the marker quote, the three bold bullets, and the batch paragraph; cut the "What arrival never means" paragraph down to its one novel clause ("A tangent is not a stop order" is already in bullet 3) — e.g. end the bullet list with: "Arrival never means restart the turn; weigh it the way you would weigh their last sentence." ~60 tokens per activation of one of the highest-frequency tags in the system.

- **[low/anti-pattern]** `talents/people-trailhead.md` — contacts bullet, lines 18-19 *(~20 tok/activation)*
  - Developer-doc co-mingling: a deployment-variation caveat the model cannot act on.
  - Fix: Delete the sentence. It matches the co-mingling signal in talent-authoring.md ("this depends on the deployment") exactly: the model sees the actual menu at runtime, so a remapped name explains itself; a host that remaps it can carry the note in its overlay.

- **[low/economy]** `talents/interactive-trailhead.md` — session bullet, lines 18-22 (paired with operations-trailhead.md lines 35-38) *(~40 tok/activation)*
  - Both interactive and operations trailheads enumerate all four session operations with parentheticals, duplicating the session leaf's own catalog (session.md's teaser plus its destructive/non-destructive table) in two places when only the trigger and the safety constant earn trailhead space.
  - Fix: Keep the safety qualifier (docs/tool-and-talent-audit.md's 2026-Q2 email-cycle learning endorses naming the safety constant in the trailhead bullet, not the branch list) and drop the four-op catalog in both files: "If the turn is about the session's own lifecycle — resetting only on explicit user request — activate `session`." The leaf owns the operation menu.

- **[low/tone]** `talents/message-channel-trailhead.md` — bullet list under 'Choose the next move deliberately:', lines 14-41
  - The list mixes unconditional posture imperatives with activation forks under a chooser header — the only trailhead that does so; peers' non-activation bullets (interactive/people's owner-identity lines) are still conditional "If X, use Y" forks, and the other chooser-header trailheads keep the list to decision bullets.
  - Fix: Split the list: routing bullets (archive/memory/signal/respond) stay under the chooser header; the posture lines (trust history, reactions as conversational acts) move to a short unheaded paragraph before or after it. No tokens saved, but the model gets a real menu instead of a menu interleaved with stance.


### Always-on foundation prose

- **[high/stale]** `talents/presence.md` — ## Anticipations bridge the gap (lines 21-27) *(~110 tok/activation)*
  - The entire Anticipations section describes the v0.6 anticipation engine, which was scrapped and removed in v0.9 — the always-on persona prefix instructs the model to create/check/resolve objects that no longer exist.
  - Fix: Rewrite the section mechanism-neutral around what replaced it (task_schedule's `action` field — real, internal/tools/tools.go:926-929 "What to do when the task fires" — and loop working notes): before -> 7 lines naming a dead lifecycle; after -> ~2 sentences, e.g. "A future wake without its why is an arbitrary alarm. When you schedule one, put the reason in the wake itself — 'Dan's flight lands at 14:45, check status, offer pickup' — so you arrive knowing what matters." Saves ~110 tokens per persona turn and stops teaching a scrapped architecture.

- **[high/anti-pattern]** `talents/working-memory.md` — whole file; tool reference at line 7 *(~468 tok/activation)*
  - The file is concrete-tool guidance for `session_working_memory` (a memory-tagged tool) riding in the always-on persona prefix, which talent-authoring.md explicitly says does not belong in foundation prose.
  - Fix: Retag to `tags: [memory]` (moving ~468 tokens out of the 1h-cached persona prefix into the tagged block) and update memory.md's two pointers (lines 220-223 and 236-240) that call it "the working-memory.md foundation talent ... loads wherever the persona is present". Note the counter-signal: memory.md documents the craft/operations split as deliberate, so this is a design decision to revisit, not a mechanical fix — the compromise is keeping only a 2-3 sentence identity-level kernel (capture texture before compaction) in persona and moving the what/when lists to the memory tag.

- **[medium/assembly]** `talents/foundation.md` — whole file vs loader ordering (internal/model/talents/loader.go lines 120, 358-367, 413-418)
  - foundation.md is the corpus preamble telling the model how to read talents, but alphabetical file ordering renders it fourth of the six persona files in the Behavioral Guidance block — after awareness, communication, and delegation have already been read (and after the auto-generated capability manifest, which is prepended to the talent list and renders first).
  - Fix: Make the framer lead: special-case foundation first among turn-shape talents in talentOrderKey (or add an order/weight frontmatter key); decide deliberately whether it also precedes the prepended capability manifest. Zero token cost; the fix is loader-side, plus the README claim becomes true instead of drifted.

- **[medium/economy]** `talents/communication.md` — ## What to trust (lines 23-30) and lines 17-19 *(~60 tok/activation)*
  - Energy-mirroring, anti-summarize, and compress-commands doctrine is duplicated between always-on communication.md (tags: [persona]) and the interactive-tagged interactive-communication.md.
  - Fix: Dedupe one side of the border. Cheapest cached-prefix win: compress communication.md's "What to trust" to its one novel sentence (distrusting the read is the mistake) and let interactive-communication.md own the anti-summarize/mirror-energy elaboration, since that doctrine only fires in live exchanges. Alternatively cut interactive-communication's restatements (no prefix savings but removes the duplicate read on interactive turns). ~60 tokens either way.

- **[medium/economy]** `talents/working-memory.md` — ## When texture is actually a fact (lines 37-46) *(~70 tok/activation)*
  - The closing section restates memory.md's signature noticing doctrine nearly verbatim rather than being the short border pointer the border-doctrine convention (docs/tool-and-talent-audit.md's "Missing border doctrine" / cross-leaf border audit) calls for.
  - Fix: Both-sides border doctrine is intentional, but the persona side only needs the redirect, not the doctrine: before -> ~8-line section; after -> "Texture that is actually a stable fact (a preference, a layout, a routine) belongs in `remember_fact` under `memory` — working memory dies with the session." Saves ~70 always-on tokens. (Dissolves entirely if foundation-2 is adopted.)

- **[medium/economy]** `talents/foundation.md` — lines 10-12 and 22-25 *(~70 tok/activation)*
  - Paragraphs two and five restate the past-you-for-now-you frame that paragraph one already establishes, in a file that rides every persona turn.
  - Fix: Merge: before -> 7 paragraphs; after -> cut lines 10-12 entirely and compress lines 22-25 to one sentence ("When a line sounds like an editor describing a document, prefer the self-facing reading — 'you' is now you."). Keeps the closer intact; saves ~70 tokens of the file's ~295.

- **[low/economy]** `talents/awareness.md` — ## Space, line 33; cf. communication.md line 40 *(~30 tok/activation)*
  - The use-human-names doctrine, including the identical kitchen-light example, appears in two always-on persona files.
  - Fix: Naming is voice, so let communication.md own it: drop awareness.md line 33 (its Space section still teaches specificity via the 'in the office' example at line 27). ~30 tokens and removes an exact duplicate from the cached prefix.

- **[low/economy]** `talents/awareness.md` — ## Time, line 21 *(~30 tok/activation)*
  - The delta-rot paragraph's second sentence is triple-qualified hedging that could be one imperative sentence.
  - Fix: Before -> the 45-word hedge chain; after -> "Never persist a delta — it rots the moment it's written. Durable prose gets an absolute time, and only when the time itself is the point." The per-surface deferral adds nothing decidable here (loops.md lines 79-83 already own the temporal-template mechanism). ~30 tokens.

- **[low/economy]** `talents/awareness.md` — ## Curiosity, lines 39-41 *(~20 tok/activation)*
  - The Curiosity section states its not-performative/intrinsic-motivation point twice.
  - Fix: Drop the line-41 restatement (end that paragraph at "the default is curiosity"); the philosophical framing survives once. The dropped sentence is ~74 chars, so the realistic saving is ~20 tokens (the original ~35 estimate was inflated), from the group's largest file (3414 bytes).

- **[low/border]** `talents/awareness.md` — filename vs the `awareness` capability tag
  - The persona file's filename occupies the doctrine-convention slot (`<tag>.md`) for the live `awareness` tag, whose actual surface is awareness-trailhead.md — a naming collision waiting for the day awareness-tag doctrine is written.
  - Fix: No content change needed now (the loader keys off frontmatter, and content overlap is nil); note it so any future awareness-tag doctrine gets `awareness-doctrine.md` (the pattern interactive-doctrine.md already uses), or rename the persona file (e.g. perception.md) in a housekeeping pass.


### Loops corpus

- **[high/doc-code-drift]** `talents/loops-examples.md` — loops_examples_curate node, "Tags scope the loop's tools", lines 103-105
  - The curate node's "Omit to inherit the core tag set" contradicts both loops-tagging.md and the runtime, which skips tag filtering entirely on an empty tag set — handing the loop the full registry, not core-only.
  - Fix: Replace the sentence with loops-tagging's position: "Omitting `tags` skips scoping entirely — name an explicit narrow set for every service loop." Both talents co-load on the walked trail (loops-trailhead.md next_tags includes loops_examples; loops-tagging.md is tagged [loops]), so the model currently sees the same fact in two conflicting shapes — the exact anti-pattern docs/model-facing-context.md names. Also flag the thane_loop_create schema description ("Omit to use only core tags", internal/tools/loop_create_tool.go:958) for the tool-group auditor — it matches the wrong side of this contradiction.

- **[high/anti-pattern]** `talents/loops-examples.md` — loops_examples_curate_dashboard node, JSON block lines 140-170
  - The dashboard leaf's only full JSON literal is the hand-authored loop_definition_set spec (the path its own prose calls the exception), so the recommended thane_loop_create front door — the default path — has no adaptable literal, violating the leaf success criterion that docs/talent-authoring.md cites this exact leaf as exemplifying.
  - Fix: Make the primary literal a thane_loop_create call (`entities`, singular `output` with `facets` and `initial`); keep the hand spec as a clearly-labeled secondary block or drop it and point at loops_examples_advanced. A model minimally adapting today's literal into the front-door call produces field names the front door does not define.

- **[high/economy]** `talents/loops-examples.md` — curate node Q1 lines 74-78 and dashboard node lines 117-123 (vs loops.md lines 43-48) *(~140 tok/activation)*
  - The facets rationale (status_line/teaser/digest ladder + "blind truncation") appears near-verbatim twice on the walked trail — loops.md doctrine and the dashboard leaf — with a third compressed echo in the curate sub-trailhead's question 1.
  - Fix: Doctrine (loops.md) keeps the full rationale. Curate Q1 compresses to: "Will anyone else consult this? Yes -> declare `facets` (activate `loops_examples_curate_dashboard`); a loop nobody reads but its owner needs none." Dashboard opens with one sentence ("Faceted publish: each reader takes the length it can afford") and goes straight to the JSON. Cuts ~2 of 3 copies.

- **[high/economy]** `talents/loops-examples.md` — dashboard "Publishing" section, lines 185-209 (vs loops.md lines 58-74) *(~150 tok/activation)*
  - The publish-interface doctrine (one argument per projection, pass all in one call, headings rendered for you, over-budget rejected not trimmed, ceiling-not-target) is duplicated near-verbatim from loops.md; only the concrete budgets (120/500/2048, verified against internal/runtime/loop/output_facets.go:20-22) are new information in the leaf.
  - Fix: Leaf keeps the example JSON, the budget numbers, and the "what each length is for" paragraph (unique, high-value); delete the duplicated interface/rejection sentences, replacing with "Doctrine covers the write contract; the budgets are 120/500/2048 runes and over-budget is rejected, not trimmed."

- **[medium/stale]** `talents/loops-tagging.md` — "What's always there", lines 55-59 *(~40 tok/activation)*
  - The hard-coded core-tools list is already stale — it omits `thane_loop_create`, which is Core (registered with Core: true, internal/tools/loop_create_tool.go:29, per #1106 A) — and freezing a live registry state into static prose guarantees recurring drift.
  - Fix: Replace the enumeration with the shape of the guarantee: "A core surface — tag/lens management, delegation (`thane_now`/`thane_assign`), escalation (`request_core_attention`), and log query — loads regardless of your `tags` array." The very next paragraph already names `## Active Tags` as ground truth; let the runtime list carry the specifics (matches the PR #917 precedent in docs/talent-authoring.md and the "static markdown for live operational state" anti-pattern in docs/model-facing-context.md).

- **[medium/economy]** `talents/loops-examples.md` — circle node, wake-routing paragraphs lines 287-307 (vs loops.md lines 162-175) *(~120 tok/activation)*
  - The three-way wake-routing doctrine (entity `wake: true` is the first door; mqtt_wake_add only for HA-side logic; producer `wake_loop` targets) is restated in the circle node in nearly the same words as loops.md's stream-wiring bullets.
  - Fix: The circle node keeps one routing line per door plus its unique payload (the worked mqtt example); delete the restated rationale sentences. Before: three paragraphs re-deriving the door choice. After: "Single-entity change -> `wake: true` on the entry. Producer event -> `wake_loop` on the producer tool. HA-side derived condition -> `mqtt_wake_add` + HA automation (worked example below)."

- **[medium/economy]** `talents/loops-examples.md` — circle node, "Why this shape is the canonical event-bridge", lines 472-488 *(~150 tok/activation)*
  - The closing justification section explains why the architecture exists (HA trigger semantics "mature and well-tested", "MQTT is the dumb pipe") — rationale the model cannot act on at launch time; only the final generalization list earns its place.
  - Fix: Cut the three rationale bullets; keep one sentence of the generalization: "The same two-artifact pairing fits any 'HA notices -> Thane decides' workflow — sump pump cycling, garage door open late, freezer drift." The division-of-labor insight is already implicit in the worked example's structure.

- **[medium/economy]** `talents/loops-examples.md` — dashboard "Where the reasoning goes", lines 216-227 (vs loops.md lines 121-139) *(~100 tok/activation)*
  - The working-notes doctrine (private by construction, rewrite-don't-append, next turn reads a position not a history, `notes` argument couples publish and thinking) is duplicated from loops.md's "Where the thinking goes" section.
  - Fix: Leaf compresses to two sentences: what to put there ("present view: what you believe, what you're watching, what would change your mind") and the mechanism hook ("`publish_output_*` takes `notes`, so publish and thinking are one call"). Drop the duplicated rewrite-not-append rationale — doctrine owns it.

- **[medium/assembly]** `talents/loops-examples.md` — loops_examples_curate node, decision frame lines 72-83
  - The curate sub-trailhead's two-question decision frame is asymmetric: question 1 answers itself with facets doctrine and never routes to `loops_examples_curate_dashboard`, question 2 lists only the yes-arm, and the plain no-facets/no-escalation service loop — arguably the simplest shape — has no destination leaf with a JSON literal at all.
  - Fix: Give each question both arms and an explicit destination: Q1 "Yes -> activate `loops_examples_curate_dashboard`; No -> the minimal shape is the dashboard JSON minus `facets`/`initial`" (or add a three-line plain-service literal to the curate node itself). A model landing here today must infer the dashboard leaf from the menu teaser alone while the body's numbered frame silently dead-ends.

- **[low/economy]** `talents/loops-examples.md` — circle node, lines 443-447 *(~15 tok/activation)*
  - A fenced JSON block containing only `{}` is spent to show that mqtt_wake_list takes no arguments — a code block that teaches nothing and momentarily reads as a broken example.
  - Fix: Delete the block; fold into the prose: "`mqtt_wake_list` (no arguments) returns registered subscriptions — each entry carries a `subscription_id`...". Keep the removal-by-ID JSON (lines 458-460), which does carry shape.

- **[low/economy]** `talents/loops.md` — lines 185-188 (also loops-examples.md circle step 3 lines 253-257 and wake_loop note lines 400-402) *(~50 tok/activation)*
  - The request_core_attention cost warning ("forces a supervisor turn — costlier than a normal wake — reserve it for concerns that genuinely warrant the extra capacity") appears three times across the group and a fourth time near-verbatim in the tool's own description, which every caller sees at call time.
  - Fix: Keep the doctrine copy in loops.md (posture emphasis is legitimate); trim the circle-node copy to "costlier than a normal wake — the tool description carries the bar" and drop the repeated "reserve for genuinely high-stakes wakes" aside on `force_supervisor`.

- **[low/other]** `talents/loops.md` — frontmatter, line 2
  - `kind: doctrine` is an undeclared frontmatter value — the README's contract says kind is "trailhead or empty" and kind.go declares only trailhead (+ deprecated entry_point); the loader passes the unknown value through unvalidated (CanonicalKind returns the trimmed raw), so it works by accident, not by contract.
  - Fix: Drop the line (doctrine is the kind-absent default) in both loops.md and diagnostics.md, or extend kind.go/README to declare `doctrine` as a legal value. As-is, any future kind validation will trip on the corpus's own canonical doctrine file.


### ha.md and documents.md

- **[high/assembly]** `internal/model/talents/loader.go` — talentsFromBlocks (lines 291-332), shouldIncludeTalent (lines 437-447)
  - Multi-node talent files load per node, not per file: ha.md is three independent activations (ha ~3,150 tokens, ha_control ~1,100, ha_automate ~1,675 — 12612/4391/6707 bytes of injected content after frontmatter strip) and documents.md is eight (~250-980 tokens each, 1003-3918 bytes), so neither file is ever a single 24KB/20KB injection.
  - Fix: No change needed — but note the corollary for auditing: since every HA tool in catalog.go carries only Tags:["ha"] (e.g. line 154 ha_call_service, line 236 ha_automation_create; no tool declares an ha_control or ha_automate tag), activating ha_control/ha_automate loads guidance only, never additional tools; the control tools are already callable the moment `ha` is active. The root's framing ("one deliberate step further in") gates doctrine, not capability. Also note voluntary tags accumulate until tag_deactivate/tag_reset (internal/tools/capability_tools.go), so in practice ha_control/ha_automate co-load with the ha root — duplication across those borders is a real per-turn cost.

- **[high/doc-code-drift]** `talents/ha.md` — ha_automate node, "## Author a new automation" (lines 515-592)
  - The automation-authoring section — the leaf's centerpiece — never names its tool: `ha_automation_create` exists in the catalog (catalog.go:236) and is promised by the node's teaser ("create") but appears nowhere in ha.md, while every sibling section names its tool in the heading or first line (ha_automation_list/get/traces/update/delete all do).
  - Fix: Name the tool where every sibling does: retitle to "## Author a new automation — `ha_automation_create`" or open with "`ha_automation_create` takes a `config` + `metadata` pair:". The leaf success criterion (docs/talent-authoring.md: adapt the displayed JSON into a working call) fails here — without the tool name the model must fish the schema list for the verb the talent taught it to want.

- **[high/stale]** `talents/documents.md` — documents_mutate root question 2 (lines 294-297) and documents_mutate_content opening (line 314)
  - The mutate/curate border advice predates doc_create landing in documents_mutate_content: the mutate root still says new knowledge means "none of the mutate leaves are quite right — back out and activate documents_curate," yet the mutate_content leaf now carries doc_create ("The default way to make an ordinary document exist"), which does the intake analysis in one call — and that leaf's own "Three tools" count is stale (it lists four: doc_create, doc_write, doc_edit, doc_journal_update).
  - Fix: Rewrite question 2 to match the current split: "Brand-new knowledge? `doc_create` (in `documents_mutate_content`) handles the common create safely; activate `documents_curate` only to inspect the intake plan first or when the knowledge likely belongs in an existing document." Fix "Three tools" -> "Four tools" (or drop the count).

- **[medium/border]** `talents/ha.md` — ha root, "## Presence and zones" (lines 306-320) vs "## Cross-references" (lines 327-329)
  - The root teaches concrete presence mechanics (read `in_zones`, read the zone entity for occupancy) and then two paragraphs later disclaims the whole topic — "`awareness` owns presence-shaped questions" — pointing the model away from the answer the same node just gave.
  - Fix: Reconcile the border: either scope the cross-reference to sustained presence attention ("for presence you'll keep watching across turns, subscribe via `awareness`; the one-shot read mechanics above are yours") or move the in_zones/zone-entity mechanics to the awareness side and keep only a pointer here. As written the two blocks contradict each other inside one activation.

- **[medium/economy]** `talents/ha.md` — ha root, intro paragraph (lines 18-22) and "## When you need to act, step further in" (lines 279-288) *(~90 tok/activation)*
  - The two-branch fork is stated twice in the root, and the second statement restates the branches' own teasers — the "Restating next_tags in the body" anti-pattern named in docs/talent-authoring.md — paid on every `ha` activation.
  - Fix: Before: full "## When you need to act, step further in" section (10 lines). After: delete the section; the intro paragraph already frames the escalation and the tag menu surfaces the teasers. If a marker is wanted where the read tools end, one line suffices: "Everything above reads; to act, activate `ha_control` or `ha_automate` — each costs more when wrong."

- **[medium/economy]** `talents/ha.md` — ha root: ha_device (lines 91-96), ha_get_state include paragraph (lines 161-165), "**Hidden entities.**" block (lines 194-203) *(~90 tok/activation)*
  - Hidden-entity salience doctrine (hidden = operator curation, still readable when you specifically need it) is stated three times inside the single ha root node.
  - Fix: Keep the dedicated "**Hidden entities.**" block as the single home (it already names the three escape hatches). In ha_device, cut to one clause: "shows hidden entities too, marked `\"hidden\": true` — naming a device means you want its whole instrument panel." In ha_get_state, drop the sentence pair about hidden-but-enabled/salience and let the block carry it.

- **[medium/economy]** `talents/documents.md` — documents_read, paragraph after doc_section example (lines 105-109) *(~55 tok/activation)*
  - The doc_outline-vs-doc_read judgment paragraph restates the trigger conditions the two preceding sections already carry.
  - Fix: Delete the paragraph. The two section openers already encode the fork, and the same-shape observation adds no decision value.

- **[medium/economy]** `talents/documents.md` — documents_discover: `search: "on_request"` bullet (lines 199-203) and funnel-precondition paragraph (lines 216-220) *(~50 tok/activation)*
  - The empty-search-proves-nothing doctrine is stated twice in full within the documents_discover node.
  - Fix: Keep the vivid bullet (it sits at the point of application, beside the field that causes the miss — matching model-facing-context.md's constraints-at-point-of-application rule) and compress the later paragraph to one sentence: "So when a search comes back empty, check `doc_roots` and search any unnamed on_request root before concluding the thing does not exist."

- **[low/economy]** `talents/ha.md` — ha root ha_device (lines 100-108) and ha_control ha_call_service targets paragraph (lines 406-419, quote at 412-415) *(~55 tok/activation)*
  - The HA-2026.8 one-device-per-integration twin explanation is duplicated across the root and ha_control, which co-load in practice since ha_control is reached through the root and voluntary tags accumulate.
  - Fix: Keep the full explanation in the root's ha_device section (where ambiguity first surfaces) and trim ha_control's copy to the actionable clause only: "a name shared by several devices fails fast listing each candidate with its integration — pick the integration that owns the capability (same-name twins are normal since HA 2026.8; see `ha_device`)."

- **[low/economy]** `talents/documents.md` — documents_mutate_content: doc_create (lines 317-320), doc_write (lines 345-346), doc_edit (lines 371-375) *(~20 tok/activation)*
  - The contract-owned-document exception (use contact_dossier_write / generated output tools instead) is restated in all three prose-bearing tool sections of one node.
  - Fix: Hoist one statement to the node's opening paragraph ("All three operate on *ordinary* documents; a contract-owned document — a contact dossier, a loop output — writes through its own structured tool: `contact_dossier_write`, `publish_output_*`/`replace_output_*`"), keep doc_edit's worked drift example since it carries the why, and drop the other two restatements. Net saving is modest once the hoisted sentence is paid for.

- **[low/economy]** `talents/documents.md` — documents root, paragraph after disambiguation table (lines 49-52) *(~40 tok/activation)*
  - The paragraph after the disambiguation table restates two of the table's own rows.
  - Fix: Delete the paragraph; the table already carries both facts at the border where they matter.

- **[low/tone]** `talents/documents.md` — root lines 12-14 ("so the model can think in documents"), documents_mutate lines 301-303 ("The model never has to know the filesystem path"); also talents/ha.md line 11 ("the largest lever this agent has")
  - Three third-person voice wobbles ("the model", "this agent") break the past-self-to-present-self second-person voice both files otherwise hold — the convention docs/talent-authoring.md states ("past-self writing for present-self") and model-facing-context.md enforces ("Behavioral guidance should address the model directly").
  - Fix: Rewrite in second person: "you never have to know the filesystem path"; "so you can think in documents instead of filesystem paths"; "the largest lever you have on the physical world." Zero size cost.


### Communication cluster

- **[high/doc-code-drift]** `talents/contacts.md` — contacts_vcf node, '## Generate a QR code' (lines 470-484)
  - The QR example passes recipient_trust_zone for a non-self contact, but the runtime applies trust-zone filtering only when name is "self", so the example teaches a silently ignored parameter and contradicts the talent's own export section earlier in the same node.
  - Fix: Change the QR example to name: "self" (the only case where the parameter does anything) and add one line: 'As with contact_export_vcf, filtering applies only to the self card — a non-self QR export carries full data, and the only size lever is the contact record itself.' Note the tool's own over-capacity error ('Use recipient_trust_zone to reduce fields', tools.go:990-991) gives the same dead-end advice for non-self contacts and is worth a follow-up code fix.

- **[high/assembly]** `talents/contacts.md` — contacts_vcf node, stray bullet after '## Cross-references' (lines 499-501); root '## Choose by the shape of your question' (lines 41-58)
  - contact_whereabouts routing is orphaned as a dangling bullet at the bottom of the vCard-exchange node, while the root — the entry point where a 'where is Frank' question actually lands — offers no whereabouts branch at all.
  - Fix: Move the bullet to the root's branch list ('You're asking where a person is — `contact_whereabouts`, no leaf activation needed') or into contacts_lookup's cross-references, and delete it from contacts_vcf where a model doing vCard work has no use for it.

- **[high/anti-pattern]** `talents/notifications.md` — notifications_ask node, thane_now paragraph (lines 240-244)
  - request_ai_escalation is registered under the notifications tag with a handler that returns not-implemented, but the talent never names it — instead the thane_now paragraph ends with a contrast clause the model cannot decode.
  - Fix: Name the stub explicitly: 'Do not reach for `request_ai_escalation` — it is registered but returns not-implemented. For inline frontier-model judgment, call `thane_now` from a premium-routed turn so the delegate inherits the higher-capability routing.' This converts the dangling clause into the disqualification the model needs, and matches the stub's own error text.

- **[medium/economy]** `talents/contacts.md` — root node, '## Constants across all branches', 'save merges; forget soft-deletes' bullet (lines 73-82) *(~80 tok/activation)*
  - The root carries contacts_save's full merge mechanics — duplicate-key property triples, origin-array replacement — duplicated nearly verbatim in the leaf's 'Update semantics', violating the root-asks-the-question rule (leaf mechanics are leaf work).
  - Fix: Before: the full ~95-word bullet. After: '**save merges; forget removes.** There is no separate update tool — `contact_save` on an existing name IS the update, and `contact_forget` has no undo the tools can reach, so look up before forgetting. Merge mechanics live in `contacts_save`.' The leaf keeps the authoritative detail. Coordinate wording with comms-save-devdoc's root/leaf delete-semantics alignment.

- **[medium/economy]** `talents/email.md` — root node, '## Constants across all branches', first bullet (lines 47-58) *(~70 tok/activation)*
  - The root's trust-gate constant enumerates both rejection categories, their messages, and their distinguishability — leaf detail restated in full by email_respond's three-result-category section, when the root only needs the safety constant compressed.
  - Fix: Compress the root bullet to: '**Recipients must be contacts at a send-eligible zone.** `email_send` and `email_reply` abort the entire send on any issue — `known`-zone and missing-contact recipients are both rejected. Confirm via `contact_lookup` before drafting; the rejection after composing is avoidable.' Failure-mode taxonomy stays in email_respond. (Gate behavior verified: trust.go:34-76 puts known-zone in Warnings and missing contacts in Blocked with distinct messages, and tools.go:372 aborts on HasIssues.)

- **[medium/anti-pattern]** `talents/contacts.md` — contacts_save node: 'Create or update' closing sentence (lines 293-296) and 'Remove a contact' (lines 344-365, soft-delete prose 352-361) *(~70 tok/activation)*
  - Store-mechanism exposition the model cannot act on: multi-value replacement is routed to 'the contact store's CardDAV-style overwrite path' (no tool reaches it), and contact_forget explains DB internals while the root describes the same delete as 'no tombstone' — the same fact in conflicting shapes.
  - Fix: Replace the CardDAV sentence with the actionable rule: 'No tool replaces a multi-valued property — ask the operator to clean it up via their contact client, or forget-and-recreate deliberately.' Trim the forget section to decision value: 'There is no undo through any tool you can reach — once forgotten, every directory-resolving gate treats the person as unknown.' Align the root bullet's wording with it (matches the talent-authoring 'developer-doc co-mingling' anti-pattern).

- **[medium/economy]** `talents/notifications.md` — notifications_ask node, opening paragraph (lines 207-224) *(~60 tok/activation)*
  - The request_core_attention redirect — right doctrine, rightly placed — is padded with catalog-mechanism exposition and internal-loop trivia that add context weight without changing the decision.
  - Fix: Before: the two quoted clauses. After: '(always available — no activation needed)' and drop the metacog/ego sentence entirely; 'the canonical service-loop → operator attention path' already covers internal loops. Keep the 'send the ask, finish the turn, and let the answer find you' body.

- **[medium/tone]** `talents/contacts.md` — contact_owner section (lines 209-211); also notifications.md Constants (line 95), signal.md disambiguation (line 17), notifications_resolve (lines 340-341)
  - The three large trees repeatedly slip from the corpus's second-person past-self voice ('past-self writing for present-self', per docs/talent-authoring.md) into third-person editor framing ('the model'), worst where one sentence mixes third and first person.
  - Fix: Sweep 'the model' to second person: 'Right tool when you need to assert...', 'the misroute you walk into', 'When you're inside an inbound Signal conversation', 'Your job is to extract...'. Also fix the changelog-tense leak 'A zone now confers inherited authority' (contacts.md:239) → 'A zone confers...', and see comms-mark-changelog for email.md.

- **[medium/economy]** `talents/email.md` — email_organize node, '## Mark messages' paragraph (lines 296-301) *(~30 tok/activation)*
  - The add-flag paragraph states the add:false behavior twice and carries changelog rationale about schema/handler agreement — diff commentary the model cannot act on.
  - Fix: Before: the quoted five clauses. After: '`add: true` (the default) adds the flag; `add: false` removes it. Single-message mode accepts `uid` instead of `uids`.' (Default verified at internal/channels/email/tools.go:184, BoolOr(args, "add", true).)

- **[low/economy]** `talents/contacts.md` — root node: paragraph after the disambiguation table (lines 35-39) and cross-reference bullets for archive/memory (lines 108-109, 113-115) *(~110 tok/activation)*
  - The root states the contacts/memory/archive border three times inside one node — the table, a restating paragraph, and cross-reference bullets — when only the table plus the litmus sentence carry decision value.
  - Fix: Keep the table and the litmus sentence ('If the same claim would belong on *any* person record ... it isn't contact knowledge at all'); delete the paragraph's first two sentences and the archive/memory cross-ref bullets (the table already routes both; the email/signal/dossier bullets add new information and stay).

- **[low/economy]** `talents/email.md` — root Constants (lines 63-67), email_triage 'Read one in full' (lines 157-160), email_organize 'UIDs are folder-scoped — the canonical gotcha' (lines 323-340) *(~40 tok/activation)*
  - Folder-scoped UIDs are fully explained three times; both leaves re-teach it at point of use, so the root's move-then-relist walkthrough is redundant with email_organize's canonical section.
  - Fix: Shrink the root constant to one line: '**UIDs are folder-scoped** — a UID only resolves in the folder it was listed from; the leaves carry the consequences.' Leaves keep their per-branch treatments (constraints at point of application).

- **[low/assembly]** `talents/interactive-doctrine.md` — whole file, alongside talents/interactive-communication.md (both `tags: [interactive]`, neither declares kind) *(~25 tok/activation)*
  - Two flat posture files share the [interactive] tag and always co-load, so the split buys no decision-time selectivity and fails the authoring rule 'Default to single-talent; split only when the combined file becomes unreadable' (docs/talent-authoring.md:244-245) — this is not the blessed posture/examples split, it is posture split from posture.
  - Fix: Merge interactive-doctrine.md's 'keep the other side oriented / research ahead / status legibility' content into interactive-communication.md as a closing section and delete the second file. Saves one frontmatter block and heading, removes the tone seam, and eliminates the standing risk of the two files drifting into duplicated doctrine.


### Operations cluster

- **[high/economy]** `talents/forge.md` — forge_known_pr node, '## Read the PR' section, lines 155-220 (identical blocks at 160-165, 185-190, 196-201, 206-211, 215-220); sixth repeat in forge_review_loop step 1, lines 390-395 *(~100 tok/activation)*
  - Five of the six read-tool JSON examples are shape-identical {repo, number} payloads that teach nothing after the first one.
  - Fix: Keep the forge_pr_get example and the forge_pr_diff example (it adds max_lines). Replace the other four JSON blocks with one line: 'forge_pr_files / forge_pr_commits / forge_pr_reviews / forge_pr_checks all take the same {repo, number} shape.' The per-tool prose bullets stay. Also drop the sixth repeat of the same literal in forge_review_loop step 1 — its parenthetical (lines 397-399) already names the four tools.

- **[high/border]** `talents/forge.md` — forge_review_loop node, steps 1 and 5, lines 404-406 and 432-444
  - The canonical review-loop workflow routes its thread-read and reply/resolve steps through the gh CLI with no mention that this requires exec (shell tag, off-by-default) and a GitHub-specific authenticated CLI — a model with only forge tags active cannot execute the GraphQL fetch in step 1 or the reply/resolve in step 5.
  - Fix: Annotate both bash blocks: 'requires exec (activate shell; may be disabled at this site) and an authed gh — GitHub only, no Gitea equivalent; when unavailable, reply via forge_pr_review_comment on the thread's path/line and note the resolution in the review body.' Or move the gh path into the 'Delegate when the patch is large' escape hatch, since a delegate with tags [shell, forge] is the shape that can actually run it.

- **[medium/doc-code-drift]** `talents/diagnostics.md` — frontmatter, line 2 (same in talents/loops.md line 2)
  - kind: doctrine is an undocumented frontmatter value that works only by accident: kind.go declares only trailhead (plus the deprecated entry_point alias), the README documents kind as 'trailhead or empty', and the loader's CanonicalKind passes 'doctrine' through untouched into a field every consumer compares only against KindTrailhead (loader.go:362 sort, tooling_tags.go:389 menu hints, tag_context.go:974).
  - Fix: Delete the 'kind: doctrine' line from diagnostics.md and loops.md — behavior is identical with it absent (non-trailhead kinds all fall to talentOrderKey's default case), and its presence teaches authors a phantom enum value. Alternatively, if a doctrine kind is wanted, declare it in kind.go and the README and add loader validation; today's silent pass-through means a typo like 'kind: trailheda' would also load silently as non-trailhead.

- **[medium/doc-code-drift]** `talents/session.md` — frontmatter line 4 (same pattern in talents/shell.md line 4)
  - session.md and shell.md declare teaser: without kind: trailhead, and mergeTalentMenuHints only harvests teasers from trailhead-kind talents — so both teasers are dead metadata and the capability menu falls back to the far weaker BuiltinTagSpec descriptions ('Conversation/session lifecycle and checkpoint tools.', 'Shell execution tools for local command work.').
  - Fix: Decide per file: either promote the carefully written teaser copy (shell's 'almost everything has a more specific tool' disqualifier, session's lifecycle framing) into the BuiltinTagSpec descriptions in catalog_tags.go where the model will actually see it, or delete the dead teaser lines. Note shell's teaser is ~115 chars, over the ~100-char guideline in docs/talent-authoring.md, if it is kept anywhere. No prompt-token cost either way (the loader strips frontmatter from Content), but the authors' safety/disqualification copy is currently silently unshipped. Do not fix by adding kind: trailhead — a no-fork, no-next_tags node is the 'trailhead that is doctrine in disguise' anti-pattern.

- **[medium/border]** `talents/archive.md` — 'The single most important disambiguation' table, line 26
  - The memory row's claim that recall is 'not text search' is wrong — recall_fact has a full-text query mode — and it sits three lines above archive.md's own 'Three have free-text search', muddying the exact archive-vs-memory border the disambiguation table exists to sharpen.
  - Fix: Rewrite the row's tail: 'Activate `memory` — the search axis there is `recall_fact`, not text search' → 'Activate `memory` — `recall_fact` searches distilled facts, not conversation text.' The distinction that matters at the border is what corpus is searched, not whether text search exists.

- **[medium/economy]** `talents/session.md` — '## Choosing the right one', lines 165-176 *(~90 tok/activation)*
  - The closing 'Choosing the right one' section is a third pass over ground already covered by the destructive/non-destructive table and the conversation_reset section, including a third restatement of the explicit-request-only invariant.
  - Fix: Delete the section. Its three-bullet mapping (snapshot → checkpoint, clean room → close, dead weight → split) is exactly the table at the top plus each tool's own 'Use this when' list; the reset invariant is already featured twice. If any residue is wanted, append one line to the table intro: 'Tempted to reset? Unless the user asked, one of the other three is the move.'

- **[medium/economy]** `talents/memory.md` — '## Two stores share this tag', lines 86-104 *(~90 tok/activation)*
  - The persistent-facts vs session-working-memory distinction is stated three times in the file — disambiguation table row (line 24), this dedicated section, and the operational 'Session working memory' section (lines 189-223) — with a fourth echo in the closing cross-references bullet; this section is the compressible copy.
  - Fix: Compress the section to two sentences: 'The `memory` tag carries two stores: persistent facts (`remember_fact`/`recall_fact`/`forget_fact`) survive across sessions; `session_working_memory` is a per-conversation scratchpad that dies with the session. Six-months-from-now truths go in the first; this-conversation texture goes in the second.' The per-store detail already lives in the operational sections below.

- **[medium/economy]** `talents/archive.md` — root node, paragraphs after the disambiguation table, lines 29-40 *(~80 tok/activation)*
  - The paragraph following the disambiguation table re-walks the surfaces with four example questions (archive, logs, memory, archive+logs — working memory gets none), then a closing paragraph asks the same four-way question as a third layer — three passes where the table plus one contrast would carry the border.
  - Fix: Keep the table and the final 'When you're not sure, ask...' litmus (it is the compressed form). Cut the example-walk paragraph, or fold its sharpest pair into the table rows themselves ('what did I tell the user about X' / 'what do I know about X') — the two VLAN 30 questions already make the archive-vs-memory contrast in one pair; the other two examples add little the table doesn't.

- **[medium/economy]** `talents/shell.md` — '## The diagnose-before-mutate pattern', parenthetical note, lines 89-93 *(~55 tok/activation)*
  - The closing parenthetical restates the deny-list doctrine already featured in Safety constants nearly verbatim and spends its remaining lines explaining how the pattern-match mechanism can be routed around.
  - Fix: Delete the parenthetical. The `find ... -delete` example above it already demonstrates the pattern; the backstop-not-sandbox doctrine already sits in Safety constants where it belongs. If the find-vs-rm preference is worth keeping, one clause on the example itself ('prefer find -delete — it fails fast on bad paths') carries it without narrating the deny-list mechanics.

- **[low/economy]** `talents/memory.md` — '## Storing a fact', category constraint block, lines 121-126 *(~45 tok/activation)*
  - The per-category glosses duplicate both the remember_fact schema description the model already holds in-context and the category assignments already made in the 'Noticing what to remember' bullets above.
  - Fix: Trim to: '**Categories are a closed enum** — the schema rejects anything outside `user`/`home`/`device`/`routine`/`preference`; pick the closest fit.' The gloss lives in the tool schema and is already exercised concretely by the Noticing bullets.

- **[low/economy]** `talents/memory.md` — '## Session working memory' closing paragraph (lines 220-223) and final cross-references bullet (lines 235-240) *(~40 tok/activation)*
  - The operational-vs-craft split with working-memory.md is stated twice within seventeen lines, in nearly the same words.
  - Fix: Keep the cross-references bullet (that is the designated home per the leaf grammar in docs/talent-authoring.md) and delete the section-closing paragraph, leaving only its first clause pointing at working-memory.md if a local pointer is wanted.

- **[low/economy]** `talents/archive.md` — archive_text node: 'Read distilled first' tail (lines 108-112) and '## Following the trail' (lines 135-140); archive_session node '## The canonical pairing' (lines 278-294) *(~60 tok/activation)*
  - The search-then-transcript pairing is stated three times across the file, twice within the archive_text node alone, and the second archive_text statement includes a vague self-reference.
  - Fix: In archive_text, delete the two tail sentences of 'Read distilled first' ('When a session summary hit looks promising...' onward) and let '## Following the trail' be the single statement. Leave archive_session's numbered pairing — it is that leaf's concrete shape — but its second numbered variant (browse-then-transcript) already appears in that node's intro ('Two tools that work as a pair: browse to identify, transcript to read', lines 232-233) and could drop to one line.


### Small leaves

- **[high/doc-code-drift]** `talents/feeds.md` — ## Follow a feed (lines 28-49)
  - The primary operation's tool name `media_follow` is never written anywhere in feeds.md — the Follow section gives adaptable JSON but no door to call it through.
  - Fix: Open the section with the name: "`media_follow` registers the feed:" before the JSON. The talent names media_feeds, media_unfollow, and even sibling tools (mqtt_wake_add, forge_repo_follow) but not its own follow door; the leaf-grammar success criterion (docs/talent-authoring.md, Concrete JSON in leaves) is a working call adapted from the leaf, which needs the tool name.

- **[high/anti-pattern]** `talents/companion.md` — frontmatter lines 1-6 + heading line 8
  - companion.md declares kind: trailhead but has no next_tags and no fork — the exact 'trailhead that is doctrine in disguise' anti-pattern; it is flat doctrine wearing the wrong kind.
  - Fix: Drop kind: trailhead (the corpus already has a `kind: doctrine` precedent in loops.md and diagnostics.md), retitle to "# Companion". Caveat before doing it: the kind is currently load-bearing for the menu teaser — mergeTalentMenuHints (internal/app/tooling_tags.go:388-390) only harvests teasers from KindTrailhead talents — so pair the fix with either extending the harvest to doctrine talents or accepting the built-in tag description (catalog_tags.go:67, which is serviceable) in the menu. See small_leaves-3.

- **[high/assembly]** `talents/feeds.md` — frontmatter line 3 (same defect in attachments.md line 3)
  - The teaser: frontmatter on feeds.md and attachments.md is inert — the menu-hint harvester skips non-trailhead talents, so the authored disqualification copy never reaches the tag menu and the built-in one-liner shows instead.
  - Fix: Decide the mechanism once: either harvest teasers from all talents (one-line change to mergeTalentMenuHints, making the two authored teasers live) or delete the dead teaser lines so future authors don't rely on them. The current state is the worst option — good disqualification copy exists, and the model never sees it at the decision moment it was written for. This also explains companion.md's shape-wrong kind: trailhead (small_leaves-2).

- **[high/economy]** `talents/feeds.md` — intro lines 8-16 + "notify: false is the third configuration mode" lines 51-57 *(~90 tok/activation)*
  - notify: false is explained twice nearly verbatim within the talent, and both passes largely restate what the notify parameter's own schema description already says on every activation.
  - Fix: Before: two paragraphs plus the intro clause. After: keep the intro clause, cut lines 51-57 to one additive sentence: "Use `notify: false` for a passive audit trail — entries stay visible via `media_feeds` without spending loop attention." The "handler is still registered; it just doesn't fire" detail restates the same fact a third time. Saves ~90 tokens per activation.

- **[medium/economy]** `talents/feeds.md` — second JSON example lines 59-70 + ## Pairing with service loops lines 101-114 *(~140 tok/activation)*
  - The digest-loop pattern is stated three times: the first wake_loop JSON, an identical-shape second JSON, and a prose Pairing section that also duplicates cross-references bullet 2.
  - Fix: Delete the second JSON block and its lead-in; collapse ## Pairing with service loops into one sentence attached to the first example ("point wake_loop at a thane_loop_create service loop and let its task judge each entry — see loops_examples_curate"), keeping the cross-reference bullet as the only pointer. Also drop the aside "the output document is now optional" (line 110) — changelog voice, and it is loops doctrine already stated in loops-examples.md:67-68 ("may maintain a managed markdown document, but need not"). Saves ~140 tokens.

- **[medium/economy]** `talents/attachments.md` — ## Why this is a receive-side surface (lines 117-128) + intro parenthetical (lines 13-14) *(~80 tok/activation)*
  - The receive-side section is mostly mechanism the model cannot act on, and the intro's parenthetical contradicts it by implying signal_send_message/email_send carry outbound attachments.
  - Fix: Before: 12 lines of why. After: two sentences — "No tool sends an attachment outward, on any channel. If asked to send a photo, describe it in prose and ask the human to attach it from their own client." Fix the intro parenthetical to "those are channel-side concerns, and no channel tool currently takes one." Saves ~80 tokens and removes the internal tension.

- **[medium/stale]** `talents/files.md` — opening lines 7-10 + ## Trust these instincts (lines 33-39) *(~110 tok/activation)*
  - Everything outside the disambiguation table reads like a pre-split stratum of documents doctrine: the opener has no files referent, and instinct bullet 1 pulls in the semantic refs the file's own table routes to `documents`, not `files`.
  - Fix: Before: koan + two posture paragraphs + four instinct bullets. After: one framing line ("`files` is raw filesystem access inside the workspace") + the table + the .md-extension gotcha + keep only the memory-border bullet ("long content lives in files or documents, never `remember_fact`"). The read-before-widening posture already lives in web.md ("widen only when...", "do not replace a named local `kb:` read with a speculative web search"). Saves ~110 of the file's ~459 tokens.

- **[medium/other]** `talents/files.md` — table row 1 (line 20) vs the files tag surface
  - The files tag carries 13 tools but the talent names three plus 'etc.', leaving the repo_git_* read subfamily and create_temp_file invisible in the entire talent corpus.
  - Fix: Add one short block: "For history questions on a workspace repo — who changed it, what changed, a file at a revision — the `repo_git_*` family (`repo_git_log`, `repo_git_blame`, `repo_git_diff`, `repo_git_show`) reads git directly; don't reconstruct history from `file_read`." Per the leaf grammar (talent-authoring.md shape table) a 13-tool surface with a real fork (file CRUD vs git history) deserves at least a family-level pointer; today files.md is a border leaf, not a files leaf.

- **[medium/border]** `talents/web.md` — cross-domain routing gap (whole file, lines 7-23)
  - web.md treats every named URL as web_fetch retrieval, but media_transcript is also tagged web and is the right door for video/podcast URLs — a border the sibling feeds.md draws and web.md doesn't.
  - Fix: Add one bullet: "a video or podcast URL wants `media_transcript`, not the page fetch — the page is a player, the transcript is the content." One line closes the border from this side.

- **[medium/economy]** `talents/companion.md` — intro lines 16-22 + observations bullet lines 32-38 *(~70 tok/activation)*
  - companion.md restates, nearly verbatim, the framing prose the live '### Companion Devices' block already injects on every companion-tagged turn — the model pays for both copies together.
  - Fix: Cut the duplicated sentences from the talent and keep only what the block doesn't say: the observed_ago vs received_ago backlog distinction, and that offline is normal, not a fault. While editing, trim the 132-char teaser (over the ~100 guidance in talent-authoring.md) to e.g. "Open when a paired device holds the answer — Mac calendar, contacts, reminders; iPhone observations." Saves ~70 tokens per activation.

- **[low/economy]** `talents/attachments.md` — ## Browse filter bullets (lines 41-45) *(~50 tok/activation)*
  - The four filter bullets restate the attachment_list parameter descriptions the model already has in the tool schema when the tag is active.
  - Fix: Delete the bullet list; keep the two genuinely additive gotchas that follow — the `signal-15551234567` hyphen/bare-digits conversation_id shape (verified against sanitizePhone in internal/channels/messaging/signal/bridge.go:1341-1343 and its use at bridge.go:856) and the no-time-range-filter warning. Saves ~50 tokens.

- **[low/economy]** `talents/web.md` — lines 7-12 and bullet 1 (line 16) *(~35 tok/activation)*
  - web.md paraphrases its own tool ('the visible page-fetch tool') instead of naming web_fetch, and the opener's second and third sentences duplicate the sharper paragraph that follows.
  - Fix: Before: "`web` is the branch for remote sources. Sometimes that means widening the world. Sometimes..." After: "`web` is the branch for remote sources. A named URL is retrieval — `web_fetch` it. An unnamed question is discovery — `web_search`, then fetch the source you found." Merge overlapping bullets 3 and 4 (both say fetch-what-you-found). Saves ~35 tokens and removes the periphrasis.


### Anthropic caching (code + policy doc)

- **[high/stale]** `internal/model/fleet/providers/anthropic.go` — minCacheablePrefixTokens, lines 843-860 (pinned by anthropic_test.go:371-391) *(~3000 tok/activation)*
  - The minimum-prefix guard uses 4096 tokens for all Opus models, but the live Anthropic doc says Claude Opus 4.8 — the Opus model in the repo's default pricing table (internal/platform/config/config.go:3026) and example fleet configs — needs only 1024, so the guard strips valid breakpoints and silently disables caching on compact (task/delegate-mode) prompts.
  - Fix: Replace the flat per-family switch with a per-version table matching the live doc (opus-4-8→1024, opus-4-7→2048, opus-4-6/4-5→4096), update the pinned test values in anthropic_test.go:379-380, and update the docs/prompt-caching.md table (line 137 says "| Claude Opus 4.x | 4096") in the same change.

- **[high/caching]** `internal/model/fleet/providers/anthropic.go` — applyCacheBreakpointGuards step 1, lines 916-940 *(~5000 tok/activation)*
  - The under-minimum guard measures the prefix using system-block characters only, but Anthropic's cacheable prefix starts at tools (hierarchy tools -> system -> messages), so on the normal agent path — where Thane's tool schemas are tens of kilobytes — under_minimum_prefix drops are false positives that discard breakpoints whose true prefix is far above the minimum.
  - Fix: Seed prefixChars with the serialized tool-definition length (the same JSON marshalling estimateToolDefsContextTokens at context_sizing.go:62-71 uses) before walking system blocks, so the estimate matches what the API actually measures. This is the cheap fix that also makes findings 1-2 far less likely to bite in practice.

- **[high/caching]** `internal/model/fleet/providers/anthropic.go` — convertToAnthropic (lines 1161-1253) sets no message CacheControl; anthropicPromptCacheControl (lines 754-759) *(~8000 tok/activation)*
  - Conversation history and intra-turn tool results are never cached: no message-level cache_control is ever emitted, and the sectioned production path suppresses the request-level automatic-caching control, so every iteration of a tool loop (up to 50 per turn, loop.go:2136) re-bills the entire messages array at full input price despite a free breakpoint slot (3 of 4 used: two system runs plus the blanket tool marker).
  - Fix: Add a 5m breakpoint on the final message block (or stop suppressing the request-level automatic cache control when sections are present, if the API composes them). Extend applyCacheBreakpointGuards to count message-level markers toward the 4-cap when doing so, and respect the live doc's 20-block lookback note for long histories. This is the largest unexploited saving in the file.

- **[medium/stale]** `internal/model/fleet/providers/anthropic.go` — minCacheablePrefixTokens, lines 850-856; anthropic_test.go:378; docs/prompt-caching.md:136 *(~1500 tok/activation)*
  - The code's Sonnet 4.6 special case (2048) contradicts both the live Anthropic doc (1024) and the repo's own policy doc (Sonnet 4.x = 1024), so requests on production model claude-sonnet-4-6 with 1024-2048-token prefixes lose caching for no reason.
  - Fix: Delete the sonnet-4-6 special case (return 1024 for all Sonnet 4.x per the live doc), fix the test pin at anthropic_test.go:378, and drop the stale "raised the minimum" comment.

- **[medium/border]** `internal/model/fleet/providers/anthropic.go` — minCacheablePrefixTokens (lines 843-860, default arm 857-859) and anthropicMaxTokens (lines 819-831)
  - Claude 5-generation model families now published by Anthropic get wrong or accidental guard coverage: Fable 5 and Mythos 5 (minimum 512) and Mythos Preview (minimum 2048) match no substring arm and fall to the strictest 4096 default (8x too strict for Fable/Mythos) plus the legacy 4096 output ceiling in anthropicMaxTokens; Opus 5 (minimum 512) hits the generic opus arm's 4096 (also 8x too strict); only Sonnet 5 lands correct (1024 via the sonnet arm) by coincidence.
  - Fix: Add explicit cases for the 5-generation names (fable→512, mythos-5→512, mythos-preview→2048, opus-5→512; a sonnet-5 case is optional since the sonnet arm already returns the correct 1024) before the fleet adopts them, extend anthropicMaxTokens to cover fable/mythos, and extend the repo doc's minimum-prefix table beyond 4.x. Today's config fleet is all 4.x (claude-opus-4-8, claude-sonnet-4-6, claude-haiku-4-5 in config.go:3026-3028 and examples/config.example.yaml), so this is preparatory rather than an active loss.

- **[medium/doc-code-drift]** `docs/prompt-caching.md` — lines 52-54 ("Conversation history is not a system-prompt section")
  - The doc claims conversation history is covered "through the message-level cache breakpoints described below" (PR #852), but no message-level breakpoints are described below nor emitted anywhere in code, so the doc asserts a caching mechanism that does not exist.
  - Fix: Either implement the message breakpoint (prompt-caching-5) and document it, or correct the sentence to state that conversation history currently rides uncached after the last system breakpoint.

- **[medium/caching]** `internal/runtime/agent/loop.go` — promptSectionCacheTTL TALENTS TAGGED 5m (lines 1379-1380); per-iteration ToolDefs (lines 2235-2243); currentTools FilterByTags (lines 2141-2153)
  - The 5m firewall on TALENTS TAGGED protects the 1h prefix only for guidance-only tag flips: any tag flip that adds or removes tools changes the tool definitions recomputed each iteration, which per the live doc invalidates the entire cache (tools sit first), making the 1h run rewrite anyway.
  - Fix: Instrument which flavor dominates (tag flips with vs without tool changes — the CacheBreakpointDrop/outbound-markers debug line plus usage per-TTL writes can distinguish full-prefix rewrites from tagged-tail rewrites). If tool-bearing flips dominate, promote TALENTS TAGGED to the 1h run for session-sticky tag sets and accept full invalidation on flips; if guidance-only flips dominate, the current split is correct — keep it and document the reasoning.

- **[medium/caching]** `internal/platform/usage/store.go` — anthropicCacheWrite multipliers lines 328-338; Record fields lines 40-49
  - The blanket 1h TTL on all stable sections pays a 2.0x write premium (vs 1.25x for 5m, which refreshes free on every use), but nothing in telemetry validates the choice against actual turn cadence — per-TTL write counts are stored per record yet no metric relates 1h-write recurrence to inter-turn gaps or surfaces a per-TTL breakdown in /stats.
  - Fix: Add a derived metric (per-session 1h cache-write frequency and inter-turn gap histogram from usage_records timestamps) to /stats. If turns arrive more often than every 5 minutes, dropping stable sections to 5m saves 37.5% of write premium at zero read cost; 1h remains right only if 5m-60m idle gaps between wakes are common. The data to decide is already being recorded.

- **[low/doc-code-drift]** `docs/prompt-caching.md` — stability table lines 33-50 (ACTIVE CAPABILITIES row at line 44) vs loop.go:1210,1220
  - The doc's section table names ACTIVE CAPABILITIES while the code emits ACTIVE TAGS, and the SESSION ORIGIN CONTEXT section the code emits between the cached prefix and the context buckets is missing from the doc's stability classification entirely.
  - Fix: Rename ACTIVE CAPABILITIES to ACTIVE TAGS in the table and add a SESSION ORIGIN CONTEXT row (volatile — origin/contact context rendered per run). The doc states "The section name is the stable contract for caching policy" (lines 29-31), so the table should match the emitter exactly.

- **[low/doc-code-drift]** `docs/prompt-caching.md` — lines 140-142 and 150-153 vs anthropic.go:892-896,924
  - The doc says under-minimum runs are stripped "with a WARN log" and that "Every drop logs a WARN", but the code deliberately demoted under_minimum_prefix drops to Debug (only over-cap drops warn), so an operator following the doc will grep the wrong log level.
  - Fix: Update the doc to say under-minimum drops log at Debug (visible aggregated on the `outbound cache markers` line) while over-cap drops WARN — the code's rationale comment is good copy to lift.

- **[low/border]** `internal/model/fleet/providers/anthropic.go` — promptCacheRuns, lines 1108-1127
  - promptCacheRuns extends a same-TTL run across interleaved no-TTL sections, so a future volatile section inserted between two stable ones would silently land inside the 1h cache entry (the breakpoint closes after it) and churn the whole prefix at the 2x write premium every turn, with no guard or test pinning stable-section contiguity.
  - Fix: Close the current run when a no-TTL section interrupts it (start a new run at the next TTL section), or add a test asserting that promptSectionCacheTTL's 1h set is contiguous in the assembled order so the doc's "Fragmenting stable sections" anti-pattern can't happen silently in reverse.

- **[low/border]** `internal/model/fleet/providers/anthropic.go` — applyCacheBreakpointGuards (lines 912-1004) and promptSectionCacheTTL (loop.go:1375-1384)
  - The live doc's mixed-TTL ordering constraint — 1h cache entries must appear before any 5m entries — is satisfied today only by construction (tool 1h, then 1h system run, then 5m TALENTS TAGGED tail) and is neither documented in docs/prompt-caching.md nor validated by the guard, so a future TTL-map edit placing a 1h section after TALENTS TAGGED would produce an invalid request with no local diagnosis.
  - Fix: Add a cheap ordering check to applyCacheBreakpointGuards (demote any 1h marker that follows a 5m marker to 5m, with a WARN and a CacheBreakpointDrop-style record — consistent with the file's convention that WARNs mark assembly-plan changes) and add the constraint to the doc's Anthropic Anti-Patterns list.


### Context assembly (runtime providers)

- **[high/caching]** `internal/runtime/agent/core_context.go` — readEgoFromProvenance, lines 264-266 (with loop.go:1377 TTL mapping)
  - A per-turn-changing time delta is embedded in the EGO section, which is marked stable with a 1h Anthropic cache TTL, so every turn after an ego.md edit rewrites the cached stable prefix from EGO through TALENTS ALWAYS ON.
  - Fix: FormatDeltaOnly (promptfmt/timefmt.go:49-65) emits second-granular deltas under 1h and minute-granular under 48h, so the EGO bytes change on essentially every turn for two days after any ego edit, invalidating the prefix cache mid-run — per the live Anthropic caching doc, a change in a system block invalidates that block and everything after it, so EGO + INJECTED CONTEXT + RUNTIME CONTRACT + TOOL CALLING CONTRACT + TALENTS ALWAYS ON are re-written as cache_creation tokens each turn (easily 5-15k tokens at 2x base pricing for the 1h TTL — the doc prices 1h cache writes at 2x, 5m writes at 1.25x — instead of 0.1x reads). Either quantize the freshness line to a coarse bucket that only changes daily (FormatDayDelta-style day words), move the ego-freshness fact into CURRENT CONDITIONS/live state, or drop the delta and keep only the revision count, which changes exactly when the content changes.

- **[high/border]** `internal/runtime/agent/channel_provider.go` — channelDefaults map, lines 117-129; rendered at line 232 into the Continuity bucket *(~160 tok/activation)*
  - ~660 bytes of static behavioral guidance for the Signal channel is injected as a JSON string field inside the volatile CONTINUITY CONTEXT bucket on every Signal turn, violating the instructions-vs-data separation rule and paying full uncached token price for prose that never changes.
  - Fix: docs/model-facing-context.md: "Behavioral guidance belongs in talents and prompts. Runtime facts belong in context providers... Do not hide instructions inside what claims to be data." Channels already pin capability tags (loop.go:1852-1855 via capabilityScope.PinChannelTags), and a doctrine talent talents/signal.md tagged [signal] already exists — move the Signal style prose there (or a sibling signal-tagged node) so it rides TALENTS TAGGED with a 5m cache TTL instead of the uncacheable volatile bucket, and keep the envelope to runtime facts: source, contact profile, trust policy. Before: {"source":"signal","note":"Signal (mobile chat app). Plain text only...","contact":{...}}. After: {"source":"signal","contact":{...}} plus the talent carrying the prose. Verify the deployment's channel-tags config pins "signal" on Signal turns so the guidance still arrives.

- **[high/other]** `internal/runtime/agent/loop.go` — line 2107 (usageInfo.TokenCount) vs context_sizing.go:57-79
  - The CONTEXT USAGE percentage the model reads is computed from messages alone while tool schemas — which context_sizing.go's own comments call "tens of kilobytes of JSON schema, large enough to overrun a window sized from the messages alone" — are excluded, so the model acts on a systematically understated occupancy figure it has no way to correct.
  - Fix: Use estimateRequestContextTokens (messages + tool defs) for the reported TokenCount, or report both numbers explicitly. On a 200K window the gap is a few percent, but on small local-runner windows the tool surface can be 20-40% of the window — a model deciding whether to compact or defer a large pull is doing so on the wrong denominator. This is the doc's offload-the-arithmetic principle ("do the time math before the model sees the data", generalized in Philosophy #2) failing at the last step. While in the block: req:<id> in the IDs line (context_usage.go:102-103) is per-turn operator forensics the model never acts on; consider dropping it from the prompt (it already rides request-detail retention via seedLiveRequestDetail).

- **[medium/anti-pattern]** `internal/runtime/agent/context_discriminator.go` — materializeContextAdvertisements, lines 218, 232-276, 298-308
  - Offers dropped during materialization (provider error, empty render, estimate overrun, budget overflow) are not added to the rendered withheld count, so a rail that lost selected offers still reads as complete; and the refusal line hardcodes doc_search as the pull door even when withheld offers come from non-document providers.
  - Fix: The Context Advertisements contract (docs/model-facing-context.md:377-379) says the refusal is rendered "because a capped rail must never read as a complete one" — the budget-overflow continue at lines 267-276 is exactly the limits refusing a genuinely selectable offer, yet it vanishes from the model's view; the error/empty/overrun drops likewise leave the rail silently incomplete. Increment a materialization-drop counter in each continue path and fold it into the rendered line. Derive the pull door from the withheld advertisements' Source/Kind fields instead of hardcoding doc_search, which is the wrong door for the existing non-document advertiser (the metacognition self-assessment in internal/state/awareness/system_self_assessment_provider.go, Source "metacognition") and any future one.

- **[medium/economy]** `internal/state/awareness/conditions.go` — CurrentConditions + detectEnvironment, lines 48-112 *(~35 tok/activation)*
  - Host, OS, arch, environment, version, commit, and branch are process-static operator telemetry recomputed (including per-turn /proc and /.dockerenv I/O) and resent uncached in the volatile tail every turn, though the model almost never acts on them.
  - Fix: These fields can only change on process restart, which invalidates every cache anyway — litmus question 5 inverted: live-computed state that never changes in practice. Compute detectEnvironment/hostname once at init; move the deploy-identity fields (host/os/arch/environment/version/commit/branch) into a stable cached section or drop os/arch/commit outright (version+branch suffice for self-identification), leaving time/time_zone/time_zone_abbrev/weekday/uptime_seconds as the genuinely volatile payload. Update the CURRENT CONDITIONS row rationale in docs/prompt-caching.md to match. Before: 12-field JSON (~65 tokens uncached/turn). After: ~5-field volatile JSON (~25 tokens) plus a cached identity blob.

- **[medium/doc-code-drift]** `docs/prompt-caching.md` — Global Section Policy table, lines 33-50
  - The section table names ACTIVE CAPABILITIES while the code emits ACTIVE TAGS, and the SESSION ORIGIN CONTEXT section (loop.go:1220) is absent from the table entirely, though the doc declares the section name is "the stable contract for caching policy".
  - Fix: Update the table to the actual emitted names (ACTIVE TAGS) and add SESSION ORIGIN CONTEXT with an explicit stability class (it is per-session semi-stable in practice but gets the default no-TTL volatile treatment from promptSectionCacheTTL). Since promptSectionCacheTTL switches on exact string names, a table that names sections that do not exist invites a future TTL being added under a name the code never emits — a silent cache no-op.

- **[medium/assembly]** `internal/runtime/agent/prompt_order_test.go` — assertPromptSectionOrder call, lines 70-84; loop.go:2108-2113
  - TOOL CALLING CONTRACT, SESSION ORIGIN CONTEXT, RELATED CONTEXT, and CONTEXT USAGE orderings are conventional rather than pinned, and CONTEXT USAGE is appended outside the tracked builder with content beginning with a newline — the exact shape assertPromptSectionsStartAtContent forbids for every other section.
  - Fix: Extend the ordering test through the full documented table (the caching doc's ordering invariant is only as strong as its regression guard). CONTEXT USAGE cannot literally ride appendTracked — it is computed after routing, outside buildSystemPromptWithProfileSections — but it should follow the same convention appendTracked establishes (loop.go:1116-1123 writes separators before marking section start): keep the "\n" separator out of Content and assert its position last. Add a case exercising a non-empty ToolCallingContract profile so the 1h-cached TOOL CALLING CONTRACT's position ahead of TALENTS TAGGED is guarded rather than assumed (DefaultModelInteractionProfile leaves it empty, so today's tests never see it).

- **[medium/economy]** `internal/runtime/loop/loop_self_context.go` — SelfContextMarkdown, line 95 *(~50 tok/activation)*
  - The per-iteration loop self-context payload is rendered with json.MarshalIndent while the convention and every sibling provider (channel context, channel overview, current conditions) emit compact JSON, spending indentation whitespace on every iteration of every loop.
  - Fix: Switch to the compact marshal helper. The ~20-field payload (21 top-level keys plus nested sleep_envelope and effective_tags) ships every iteration in the LIVE STATE bucket via LoopSelfContextProvider; two-space indentation plus per-line newlines adds roughly 150-250 bytes per render for zero model value (the doc's own convention: "compact JSON objects for single records with stable fields"). Before: multi-line indented block; after: one compact object, same schema.

- **[low/tone]** `internal/runtime/agent/core_context.go` — readEgoFromProvenance, line 265
  - The EGO freshness line interpolates a commit message where the word "by" promises an author, producing text like "(updated -26h45m by third-write, revision 3)" that forces the model to puzzle out the referent.
  - Fix: Rephrase so the field's nature is explicit from the model's vantage point (per the "Write from the model's vantage point" convention), e.g. "(updated -26h45m, revision 3; last change: \"third-write\")", or drop the message entirely — the revision count already conveys churn. Coordinate with ego-cache-churn-1: whatever survives should not include a fine-grained delta in this cached section.

- **[low/doc-code-drift]** `internal/runtime/agent/channel_provider.go` — InteractionRef doc comment, lines 87-88; also internal/state/awareness/context_usage.go:15-21
  - Comments documenting model-facing value shapes are stale: InteractionRef.Ago is documented as "a signed-second delta such as -3600s" but is populated with tiered FormatDeltaOnly output, and ContextUsageInfo.Model is documented as "the default model name" with "(routed)" meaning "the router may select a different model" while loop.go:2100-2101 passes the post-routing selected model.
  - Fix: Fix both comments to describe the actual emitted shape. These comments are the spec future providers copy from — channel_provider_test.go:303/601 hardcode the obsolete "-3600s"/"-7200s" fixture shapes, evidence the stale doc is propagating. For context_usage.go, re-document Model as the router-selected model and Routed as "a router made this selection".

- **[low/border]** `internal/runtime/agent/channel_overview.go` — channelEntry, lines 89-107
  - The rendered loop_id is an 8-rune prefix that the code itself documents as colliding for every loop created in the same ~65 second window, so the model can be shown two channel entries with identical loop_id values and no way to address the right one.
  - Fix: The collision was fixed for sorting (unexported fullLoopID) but not for the model-facing identifier. If loop_id is meant as a handle for loop-addressing tools, lengthen the prefix until unique within the rendered set (git-style dynamic disambiguation) or render conv_id as the primary handle; if it is not meant as a handle, drop it — an ambiguous identifier the model cannot act on is decision noise.

- **[low/doc-code-drift]** `internal/state/awareness/conditions.go` — package doc comment lines 6-8 and H1 heading at line 83
  - The package comment claims Current Conditions is "placed early in the system prompt (after persona and inject files, before talents)" with "heading level (H1) signals importance", but the section is actually rendered last (loop.go:1262, per the stable-prefix-first cache policy), leaving a stale rationale and the prompt's only generated H1 heading among uniformly H2 sections.
  - Fix: Rewrite the comment to reflect the volatile-last placement and its caching rationale, and demote the heading to "## Current Conditions" to match every other generated section (appendTrackedMarkdown level 2, "## Active Tags", "## Behavioral Guidance", "## Runtime Contract") — a lone H1 at the prompt tail is a leftover emphasis signal from the abandoned early-placement design.
