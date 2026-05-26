# Tool and Talent Audit Process

The model's effectiveness compounds with the quality of the talent
corpus that routes it through its tool surface. Every major tooling
change introduces drift: a new integration ships with no leaf-talent
guiding it, a tag rename leaves dangling next_tags references, an
implementation-shaped tag grouping that worked at 5 leaves becomes
visibly wrong at 25. This document is the methodology for catching
and fixing that drift before it compounds.

See [`talent-authoring.md`](talent-authoring.md) for the craft of
*writing* an individual talent. This doc is about the audit process
that decides *which talents to write*, *what shape they should take*,
and *when the existing taxonomy needs to bend*.

## Why this matters

Three observations from the talent-corpus overhaul (#910) made this
explicit:

1. **The trailhead → leaf → tool routing is the model's mental map of
   what it can do.** When the map is wrong, the model picks the wrong
   tool — not because the model is confused, but because the talent
   confidently pointed it at the wrong surface.
2. **Tool surfaces grow by integration onboarding (Conway's Law);
   model questions cut across integrations.** The tag taxonomy ends
   up shaped like the org chart of the codebase unless something
   deliberately reshapes it around model-facing problems.
3. **Quality of the talent corpus is a force multiplier on inference
   power.** Better talents → less wasted iteration → fewer wrong tool
   calls → smaller, smarter agents work like bigger, dumber ones used
   to.

So this audit is not housekeeping. It's one of the highest-leverage
maintenance disciplines in the codebase.

## When to run it

A full audit should be triggered by:

- **A new integration is onboarded** (an entire new service/system
  worth of tools, e.g., the original Forge integration, future Slack
  integration, future Notion integration)
- **A new tool family is added** (3+ new tools that share a domain,
  e.g., `doc_intake` / `doc_commit` added a curate pipeline)
- **A capability tag is renamed or repurposed** (the alias/parent
  machinery may need updating)
- **A taxonomy reshape lands** (PR-G's hierarchy formalization was
  the last one)
- **Semi-annually as a sweep**, even without a specific trigger.
  Drift accumulates from small unrelated changes.

A *partial* audit (one leaf, one trailhead) is appropriate when
working on a specific area and noticing a gap. Capture the
observation in the [Learnings log](#learnings-log) below even if you
don't fix it in the same PR.

## The seven steps

The audit shape is stable across cycles; only the specifics change.

### 1. Inventory

Map the current surface. The useful baselines:

```bash
# Tools per tag
grep -E 'Tags:.*"<tag>"' internal/model/toolcatalog/catalog.go \
  | awk -F'"' '{print $2}'

# Tag declarations on existing talents
for f in talents/*.md; do
  awk '/^---$/{n++; next} n==1 && /^tags:/{print}' "$f"
done

# Talents per kind / per shape
ls talents/ | grep -v README
```

The output of this step is a table: every leaf tag with its tool
count, existing talent (if any), and existing talent's shape (flat
doctrine / multi-node tree / two-talent split).

### 2. Audit shape

For each leaf in the inventory, ask:

- **What is the model doing when it lands here?** This is the domain
  framing — one sentence the trailhead-bullet teaser will compress
  from.
- **Is there a real fork the model has to make inside this leaf?**
  If yes, multi-node tree. If no, flat doctrine. See the
  [Leaf grammar](talent-authoring.md#leaf-grammar) section in the
  authoring guide for the decision rules.
- **What sibling leaves does this point at?** Cross-references catch
  the dense web of "when to bounce" that single-leaf talents tend to
  miss.
- **What posture or safety doctrine must the leaf carry?** Especially
  for action-shaped surfaces (`shell`, `ha_control`, `email_compose`,
  `doc_mutate`).

### 3. Diagnose mismatches

The recurring anti-patterns:

| Anti-pattern | Symptom | Worked example |
|---|---|---|
| **Implementation-shaped tag** | Tag groups tools by service/package, not by model problem; the model never *lands* on this tag as a starting point, it only passes through en route from somewhere else | `media` originally bundled feeds + transcripts (two different problems); `mqtt` bundles MQTT-protocol tools that only appear inside HA-automation / loop-wake workflows |
| **Ghost trailhead** | Menu tag with no talent at all | `knowledge` for years; flagged in PR-C |
| **Cafeteria talent** | Flat list of "use X when Y" bullets with no decision frame | `documents-knowledge.md` before PR-D — 12 bullets, no shape |
| **Ghost tool reference** | Talent backticks a tool name that doesn't exist | `watch_entity` / `unwatch_entity` in early `loops-doctrine.md` |
| **Wrong-data-store** | Talent points at a real tool that mutates a different store than the reader expects | `add_entity_subscription` (conversation-wide) vs `update_entity_subscriptions` (loop-scoped) — caught in PR-A |
| **Type-name vs runtime mismatch** | Talent describes behavior matching internal type names rather than actual runtime behavior; the type signals one thing, the runtime delivers another | Email trust gate: `TrustResult.Warnings` slice name implied "send proceeds with warnings" but `HasIssues()` treats warnings as rejections — caught in PR-927 Copilot review |
| **Buried safety doctrine** | Action-shaped leaf with safety pattern present but at the bottom rather than as a featured root-level invariant | `ha-trailhead.md`'s find_entity → call_service → get_state pattern lived under a "Verifying device control" section at the file's end; `ha.md` elevated it to the root's "constants across all branches" section |
| **Cross-reference gap** | Leaf carries internal routing but no "and here's when to bounce" section | Most pre-grammar leaves |
| **Missing border doctrine** | Each leaf is internally clean, but the *boundaries* between leaves aren't featured at the model's actual entry points. A model that lands on the wrong leaf reads through the whole talent before any cross-reference disambiguates — by which point it has already burned the turn picking a tool. Disambiguation tables belong **on both sides of the border**, at the top, not only in the cross-references list at the bottom. | `archive.md` features the archive-vs-logs_query split prominently but doesn't disambiguate against memory at all; the pre-refresh `memory.md` carries no disambiguation table. The two stores share lookup-shaped framing yet point at neither each other's surface. Same gap pattern for files-vs-documents, notifications-vs-email, contacts-vs-memory. The follow-up border-audit PR sweeps these (#936 for the on-main pairs; the in-flight leaf PRs cover the rest). |
| **Architecture-stale advice** | Talent captures how the system used to work, routes the model around a current boundary | `loops-tagging.md`'s pre-#696 escalation advice — caught in PR-F review |
| **Developer-doc co-mingling** | Prose explains config / deployment / mechanism that the model doesn't need to decide | site-specific operator config explanations in early `loops-tagging.md` |

The regression tests in
[`internal/model/talents/repo_corpus_test.go`](../internal/model/talents/repo_corpus_test.go)
catch ghost-tool and ghost-trailhead-resolve mistakes mechanically.
The rest are author-craft and require this audit.

### 4. Sequence the fixes (leaf-first)

**The bottom layer informs the trailheads above it.** Build the leaves
first. Each leaf's "what is the model doing when it lands here" is
the trailhead bullet's compression target — and the only way to write
that compression honestly is to know what the leaf says first.

Within leaves, sequence by:

1. **Leverage** — leaves that show up everywhere the model works
   (`ha` because every home automation question; `documents` because
   most curation goes through it; `forge` because every shipping
   loop hits it). HA is the largest force multiplier in this
   codebase; it deserves the first-class treatment.
2. **Safety** — leaves whose absent doctrine is dangerous (`shell`,
   `ha_control`, anything that mutates persistent state).
3. **Sharpness** — leaves where the model demonstrably mis-routes
   today (the archive ↔ logs confusable pair that prompted this
   audit cycle).

After 3–4 leaves exist, the trailheads above them compose naturally
and you can confirm the grammar holds. Don't write trailheads
top-down — that's how cafeteria talents emerge.

#### Redistribute, don't document

Not every leaf in the queue should be audited; some should be
**dissolved**. When inventorying a tag, ask: *does the model ever
land here as a starting point, or only en route from somewhere
else?* If only en route, the tag is implementation-shaped (see the
anti-pattern in step 3) and its tools belong wherever the model
actually starts the work. The right cycle in that case isn't a
leaf rebuild but a **redistribution**: re-tag the tools to their
problem-shaped homes, document the cross-system pattern from those
homes, and drop the implementation-shaped tag from the menu. The
talent corpus gets *smaller*, not larger.

Worked example: `mqtt` carries `mqtt_wake_add` / `mqtt_wake_list` /
`mqtt_wake_remove`. The model never lands on "mqtt" as a starting
point — these tools appear inside HA-automation / loop-wake
workflows (HA publishes on event; a Thane loop registers an
`mqtt_wake_*` to react). Redistributing the tools to `loops` (the
loop-side concern) and documenting the cross-system pattern in
`ha.md` and `loops-examples.md` serves the model better than
building a standalone mqtt leaf the model only traverses to get
back to where the real work lives.

The triage question when staring at a queue of tags-without-talents:
*"will the model ever ask a question that starts here?"* If no, the
tag is a redistribute candidate, not a leaf candidate.

#### Cross-leaf border audit

After enough leaves exist, run a separate pass on the *borders*
between them. Per-leaf audits catch intra-leaf confusion; the
**Missing border doctrine** anti-pattern in step 3 catches the
inter-leaf kind. Walk the confusable pairs (archive ↔ memory,
files ↔ documents, contacts ↔ memory, notifications ↔ email, etc.)
and verify the disambiguation is featured **on both sides of the
border** at the top of each talent, not only in the cross-references
list at the bottom. A model that lands on the wrong leaf should hit
the redirection within the first screen, not after reading the
whole talent.

### 5. Build

Follow the leaf grammar in
[`talent-authoring.md`](talent-authoring.md#leaf-grammar). The
recurring shapes:

| Tool count + fork | Shape |
|---|---|
| 0 tools (context-only) | Pure posture |
| 1–2 tools | Flat doctrine, safety/judgment-focused |
| 3–4 tools, mechanical | Flat doctrine with tool-shape per case |
| 4–6 tools, real fork | Multi-node tree |
| 7+ tools | Multi-node tree, often with sub-trailheads |

Multi-node leaves use [`loops-examples.md`](../talents/loops-examples.md)
and [`documents.md`](../talents/documents.md) as the canonical
reference; their root-asks-the-question + per-leaf-teasers +
concrete-JSON-at-leaves pattern is the standard.

### 6. Pattern-test

Before committing, check each new leaf against:

- **Teaser disqualification** — does the one-line teaser tell the
  model when *not* to activate? (See teaser-writing craft in
  [`talent-authoring.md`](talent-authoring.md).)
- **Concrete JSON** — does every leaf have realistic JSON the model
  can adapt with minimal reshaping, not "use X with Y parameter"
  prose?
- **Cross-references** — does the leaf name its sibling leaves and
  the trigger conditions for bouncing to them?
- **Regression tests pass** — `TestRepoTrailheadNextTagsResolve` and
  `TestRepoTalentToolReferences` catch obvious drift; both run in
  `just ci`.

### 7. Capture learnings

After the audit cycle, feed the observations back:

- New anti-patterns discovered → add to the
  [Diagnose mismatches](#3-diagnose-mismatches) table above
- New grammar refinements → update
  [`talent-authoring.md`](talent-authoring.md)
- Mismatches noticed but deferred → file as follow-up issues so
  they're not lost
- This cycle's specific findings → add to
  [Learnings log](#learnings-log) below

## Worked example: HA

The HA leaf (11 tools across observe / control / automate) was the
first leaf rebuilt under this process. It is the canonical exemplar
because:

- **It is the highest-leverage tool surface in the codebase** (home
  automation is the model's largest concrete-impact area)
- **It has a real safety surface** (`call_service` doesn't validate;
  stale entity IDs silently succeed)
- **It has three clearly-distinct branches** the model has to choose
  between (am I reading? changing? authoring an automation?)
- **It cross-references multiple sibling leaves** (`awareness` for
  sustained attention, `notifications` for delivery, `loops` for
  curate-watcher patterns)

The leaf-talent's shape directly informs trailhead authors above it.
The home menu trailhead's bullet for HA compresses the leaf's
framing; the operations trailhead doesn't try to claim HA at all
because automate-side work is HA-domain, not ops-domain.

See [`talents/ha.md`](../talents/ha.md) for the talent itself and the
[home trailhead](../talents/home-trailhead.md) for how the leaf
bullet compresses.

## Learnings log

A running history of what each audit cycle taught the methodology.
Newest first. Each entry is "what we did + what it changed about
how we work."

### 2026-Q2 — methodology refinements after several leaf cycles

After eight leaf cycles (HA / archive / email / contacts / session /
shell / notifications / memory), two cross-cutting concerns surfaced
that no individual leaf could catch:

- **Borders matter as much as bodies.** Per-leaf audits clean up
  the inside of each leaf but don't address the *boundaries*
  between leaves. A model that lands in `archive` looking for
  "what we know about X" has no in-talent redirection toward
  `memory` at all — the archive↔memory border is missing entirely
  in the current corpus, despite the two stores being a natural
  confusable. The mis-route burns a turn before any cross-leaf
  signal could intervene. **Missing border doctrine** added to
  the diagnose-mismatches table; a cross-leaf border audit pass
  added as a sequencing step in step 4. The remedy: disambiguation
  tables on *both sides* of every confusable pair, featured at
  the top of the talent rather than in the cross-references list
  at the bottom. The follow-up sweeps (#936 for on-main pairs;
  edits inside the in-flight leaf PRs for the rest) apply this
  remedy across the corpus.
- **Not every queue entry deserves a leaf.** The instinct to
  "build a talent for every tag in the catalog" misses the
  implementation-shaped-tag anti-pattern: some tags exist because
  of how the tools are organized in code, not because the model
  ever lands on them as a starting point. `mqtt` was the worked
  example — the protocol-shaped tag bundles wake-loop registration
  tools the model only encounters inside HA-automation workflows.
  Redistribution (re-tag the tools to their problem-shaped homes;
  drop the implementation-shaped tag) serves the model better than
  building a talent that just acts as a way station.
  **Redistribute, don't document** added as a sequencing principle
  in step 4.

Both refinements are about the *shape of the trail system* rather
than the contents of any single leaf. The audit doc previously
treated each leaf as an independent unit; it now also treats the
inter-leaf taxonomy as something the audit can fix.

### 2026-Q2 — email leaf (cycle #3)

- **Type signatures lie about runtime behavior.** The email trust
  gate's internal type names a slice `Warnings`, suggesting "send
  proceeds with a warning result." The actual runtime calls
  `HasIssues() = len(Warnings) > 0 || len(Blocked) > 0` and aborts
  on either. The first draft of `email.md` described the
  type-implied behavior; Copilot caught it in review. New
  anti-pattern (**Type-name vs runtime mismatch**) added to step 3's
  table. The author check: trace the failure path in code, not just
  in type signatures.
- **Safety-surface leaves benefit from compressing the safety
  constant into the trailhead bullet.** Updates to the people
  trailhead's email/contacts bullets and the interactive/operations
  trailheads' session bullets all gained value by including the
  safety qualifier ("only on explicit user request," "contact-trust
  gated") rather than just naming the sub-tags. Listing branches is
  navigation; naming the safety constant is the *reason* the model
  cares the leaf exists.
- **Flat-doctrine vs multi-node grammar held.** Session (4
  mechanical tools, each genuinely distinct → flat) and HA / archive
  / email / contacts (real forks → multi-node) both fell out cleanly
  from the leaf grammar's tool-count + fork-honesty rule. The rule
  appears stable enough that future cycles can apply it without
  re-deriving.

### 2026-Q2 — HA leaf + audit doc (this cycle)

- **Bottom-up beats top-down.** The "let's design the new trailheads
  first" plan that started this cycle was wrong; building the leaf
  first and letting the trailhead compose from it is the honest
  craft. The audit doc captures this as step 4.
- **Tool surfaces shape menus, not the reverse.** A leaf with three
  natural branches (observe / control / automate for HA) probably
  belongs under a menu that respects those three; if the menu name
  fights the leaf's natural shape, the menu name is wrong.
- **Cross-reference density is high.** Every multi-tool leaf in the
  inventory cited 2+ sibling leaves it should point at. Adding a
  cross-references convention to the leaf grammar surfaced the
  density.

### 2026-Q2 — PR-A through PR-G (the corpus overhaul)

- **Regression test infrastructure** catches ghost tools and
  dangling next_tags mechanically; before PR-A they were author-craft
  only.
- **Trailhead-vs-doctrine grammar** (introduced in PR-C's
  authoring guide) crystallized the shape questions for the rest of
  the cycle.
- **Implementation-shaped taxonomy** was the deep diagnosis under
  the visible problems; PR-G's `Kind`/`Parents`/`Aliases` makes the
  hierarchy data instead of prose.
- **`ha_admin` removal** taught that BuiltinTagSpec entries without
  in-repo doctrine are smells — the regression test would have
  caught the dangling next_tags but not the deeper "this concept
  doesn't belong in this catalog" problem. That diagnosis lives in
  the audit, not in tests.
- **Co-mingled developer documentation** in talent prose is a
  recurring author trap (caught in `loops-tagging.md` review). The
  audit checklist now explicitly names it.

## Quick-reference: inventory commands

For starting a new audit cycle:

```bash
# Per-leaf tool count
for tag in $(grep -E '^[[:space:]]*"[a-z_]+":[[:space:]]*\{' \
             internal/model/toolcatalog/catalog_tags.go \
             | sed -E 's/.*"([a-z_]+)".*/\1/'); do
  count=$(grep -cE "\"$tag\"" internal/model/toolcatalog/catalog.go)
  printf "%-15s  %d\n" "$tag" "$count"
done | sort -k2 -rn

# Tag declarations on talents
for f in talents/*.md; do
  if [ "$(basename $f)" = "README.md" ]; then continue; fi
  name=$(basename "$f" .md)
  fm=$(head -10 "$f" 2>/dev/null)
  kind=$(echo "$fm" | grep -E "^kind:" | head -1 | sed 's/kind: *//')
  tags=$(echo "$fm" | grep -E "^tags:" | head -1)
  printf "%-25s  kind=%-12s  %s\n" "$name" "${kind:-none}" "$tags"
done

# Tags in catalog without a talent (assuming POSIX comm)
comm -23 \
  <(grep -E '^[[:space:]]*"[a-z_]+":[[:space:]]*\{' internal/model/toolcatalog/catalog_tags.go \
    | sed -E 's/.*"([a-z_]+)".*/\1/' | sort -u) \
  <(for f in talents/*.md; do
      awk '/^---$/{n++; next} n==1 && /^tags:/{print}' "$f" 2>/dev/null
    done | sed 's/tags: *\[//;s/\].*//;s/,/ /g' \
      | tr -s ' ' '\n' | grep -v '^$' | sort -u)
```
