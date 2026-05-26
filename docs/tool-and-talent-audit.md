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
| **Implementation-shaped tag** | Tag groups tools by service/package, not by model problem | `media` originally bundled feeds + transcripts (two different problems) |
| **Ghost trailhead** | Menu tag with no talent at all | `knowledge` for years; flagged in PR-C |
| **Cafeteria talent** | Flat list of "use X when Y" bullets with no decision frame | `documents-knowledge.md` before PR-D — 12 bullets, no shape |
| **Ghost tool reference** | Talent backticks a tool name that doesn't exist | `watch_entity` / `unwatch_entity` in early `loops-doctrine.md` |
| **Wrong-data-store** | Talent points at a real tool that mutates a different store than the reader expects | `add_entity_subscription` (conversation-wide) vs `update_entity_subscriptions` (loop-scoped) — caught in PR-A |
| **Missing safety doctrine** | Action-shaped leaf with no doctrine on stale IDs, verify-after, blast radius | `ha-trailhead.md` had this for control_device — verify-after-control pattern was buried |
| **Cross-reference gap** | Leaf carries internal routing but no "and here's when to bounce" section | Most pre-grammar leaves |
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
for tag in $(grep -E '^\s*"[a-z_]+":\s*\{' \
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
  <(grep -E '^\s*"[a-z_]+":\s*\{' internal/model/toolcatalog/catalog_tags.go \
    | sed -E 's/.*"([a-z_]+)".*/\1/' | sort -u) \
  <(for f in talents/*.md; do
      awk '/^---$/{n++; next} n==1 && /^tags:/{print}' "$f" 2>/dev/null
    done | sed 's/tags: *\[//;s/\].*//;s/,/ /g' \
      | tr -s ' ' '\n' | grep -v '^$' | sort -u)
```
