---
name: documents
tags: [documents]
kind: trailhead
teaser: "Open when working with the managed-markdown corpus — reading, finding, mutating, or curating."
next_tags: [documents_read, documents_discover, documents_mutate, documents_curate]
---

# Documents

The managed document tools wrap a small set of markdown roots (`kb`,
`scratchpad`, `generated`, `core`, dossiers, etc.) with semantic refs
like `kb:network/vlans.md` so the model can think in documents instead
of filesystem paths.

## Choose by what you're doing with the corpus

Activate the next tag based on the shape of the work:

- **You already know the document ref and need to extract from it** —
  activate `documents_read`. Reads, outlines, sections, link graph, and
  frontmatter vocabulary for one known document.

- **The truth lives in the corpus but the path has drifted** — activate
  `documents_discover`. Roots → browse → search funnel for locating the
  right document when the destination is unclear.

- **A managed document needs to change** — activate `documents_mutate`.
  Body edits, journal appends, section transfers, whole-document
  copy/move/delete. Sub-fork inside.

- **Proposing brand-new knowledge into the corpus** — activate
  `documents_curate`. The two-step `doc_intake` → `doc_commit` pipeline
  that decides title/path/tags before writing.

## The single most important disambiguation

**`documents` is the managed-document layer; `files` is raw
filesystem access.** The two share the word "file" but do different
jobs. Pick by whether the path has a semantic ref:

| You want... | Surface |
|---|---|
| Read, edit, or search inside a managed root (`kb:`, `core:`, `scratchpad:`, `dossiers:`, etc.) | `documents` — this leaf. Frontmatter, timestamp, index hygiene are owned here. |
| Read or write a raw workspace path (`./README.md`, `./Cargo.toml`, build outputs, config) | `files` — drop down when the work is genuinely outside any managed root |
| Store a compact key+value that survives the conversation | `memory` (`remember_fact`) — documents are for the source material, memory is for the distilled fact |
| Search past conversation history (not document content) | `archive` — the conversation transcript is in a different store than the corpus |

These tools own frontmatter integrity, timestamp hygiene, and index
updates. Drop to `files` only when the work is genuinely raw
filesystem operations *outside* a managed root — config files, build
outputs, anything the document index doesn't track.

---
name: documents_read
tags: [documents_read]
kind: trailhead
teaser: "Extract content, structure, links, or vocabulary from a document whose ref you already have."
---

# Read a known document

You have a canonical ref (`kb:network/vlans.md`, `dossiers:people/alice.md`,
etc.) and need something out of it. Pick the most specific extractor.

## The whole document

`doc_read` returns frontmatter, body, outline, and derived metadata in
one payload. Right when the document is small enough to read in full and
you want everything.

```json
{
  "ref": "kb:network/vlans.md"
}
```

Large documents may be truncated by tool output limits. When that
happens, switch to outline-then-section.

## Outline first, then sections

When the document is large or you only need one part, walk the heading
tree first. `doc_outline` returns the structural map without the body:

```json
{
  "ref": "kb:network/vlans.md"
}
```

Then pull the specific section with `doc_section`:

```json
{
  "ref": "kb:network/vlans.md",
  "section": "VLAN 30 — IoT"
}
```

`doc_section` accepts either the heading text or its slug
(`vlan-30-iot`). Omit `section` to get the full body without
frontmatter.

`doc_outline` and `doc_read` take the same `{ "ref": ... }` shape, so
picking the right one is a judgment call, not a schema constraint.
Use `doc_outline` when you need the heading tree before deciding what
to read; use `doc_read` when the whole document is worth loading in
one payload.

## Relationship structure

When the important question is graph-shaped — what does this point at,
what already points here — use `doc_links` instead of reading the body:

```json
{
  "ref": "kb:network/vlans.md",
  "mode": "both",
  "limit": 30
}
```

Raise `limit` or `per_backlink_limit` when the graph is broad.

## Corpus vocabulary

Before guessing at tag names or frontmatter filter values, `doc_values`
reports observed values for a key across roots:

```json
{
  "key": "tags",
  "root": "kb",
  "limit": 30
}
```

Especially useful before crafting a `doc_search` call where the wrong
tag spelling silently returns nothing.

---
name: documents_discover
tags: [documents_discover]
kind: trailhead
teaser: "Locate a document when the answer lives in the corpus but the ref has drifted out of mind."
---

# Discover the right document

Three tools, used as a funnel. Skip steps when you already know the
answer to that step's question.

## Step 1 — which root?

When you don't know which managed root holds the answer, `doc_roots`
returns each indexed root with document counts, top tags, top
directories, and recent examples:

```json
{}
```

Skip when you already know the right root (most queries about people
go to `dossiers`, most network/infra notes go to `kb`, etc.).

## Step 2 — browse like a phone tree

When the *shape* of the corpus matters more than free-text search,
`doc_browse` shows immediate children of a root/path prefix:

```json
{
  "root": "kb",
  "path_prefix": "network/unifi",
  "limit": 30
}
```

Right when you know the topic area and want to see what's there before
committing to a search query.

## Step 3 — search to narrow

`doc_search` is the right tool once you can constrain by root, tags,
frontmatter shape, or modified-time window. Free-text-only searches
against the whole corpus are usually too broad to be useful:

```json
{
  "root": "kb",
  "query": "vlan",
  "tags": ["network"],
  "modified_after": "-2592000s",
  "limit": 20
}
```

`modified_after` and `modified_before` take RFC3339 timestamps or signed
deltas (`-7d` = past week). Search returns compact summaries with
refs, not bodies — pipe a ref to `documents_read` to actually read the
hit.

If `tags` filtering returns nothing unexpected, run `doc_values` for
`tags` against that root to see the actual vocabulary.

---
name: documents_mutate
tags: [documents_mutate]
kind: trailhead
teaser: "Change a managed document — body, section structure, or whole-doc lifecycle."
next_tags: [documents_mutate_content, documents_mutate_structure, documents_mutate_lifecycle]
---

# Mutate a managed document

Two questions decide which sub-shape fits:

1. **Are you changing what's inside one document, or moving content
   between documents, or operating on the document as a whole?**
   - Inside one document (body, sections, metadata, journal entries)
     → activate `documents_mutate_content`
   - Moving a section from one document to another → activate
     `documents_mutate_structure`
   - Acting on the document as a unit (copy, rename, delete) → activate
     `documents_mutate_lifecycle`

2. **Is this brand-new knowledge being introduced to the corpus?** If
   yes, none of the mutate leaves are quite right — back out and
   activate `documents_curate` instead. The intake step decides the
   title/path/tags before writing, which is the whole point.

## All three sub-shapes preserve the abstraction

Every mutator updates the document index, keeps `created`/`updated`
timestamps current, and operates on semantic refs. The model never has
to know the filesystem path.

---
name: documents_mutate_content
tags: [documents_mutate_content]
kind: trailhead
teaser: "Body, section, or metadata changes inside one existing document."
---

# Mutate content inside one document

Three tools, each owning a different mutation shape.

## Replace or create — `doc_write`

Use when the document should hold *exactly* this body. Creates the
document if the ref doesn't exist yet. Owns `title`, `description`,
`tags`, `created`, `updated`:

```json
{
  "ref": "kb:network/vlans.md",
  "title": "VLAN layout",
  "tags": ["network", "unifi"],
  "body": "# VLAN layout\n\nVLAN 10 — management\nVLAN 20 — trusted\nVLAN 30 — IoT\nVLAN 40 — guest\n"
}
```

Add `journal_entry` to write the body *and* drop a timestamped note
under a managed `Journal` section in one call:

```json
{
  "ref": "kb:network/vlans.md",
  "body": "...",
  "journal_entry": "Renumbered guest VLAN from 50 to 40 to match the new switch layout."
}
```

## Surgical edit — `doc_edit`

Use when only part of the document should change. The `mode` parameter
picks the shape: metadata-only, whole-body replace/append/prepend, or
section-level upsert/delete.

```json
{
  "ref": "kb:network/vlans.md",
  "mode": "upsert_section",
  "section": "VLAN 30 — IoT",
  "level": 2,
  "content": "VLAN 30 carries IoT devices. DHCP pool: 10.30.0.0/24. No outbound WAN.\n"
}
```

Section edits target by heading text or slug. The upsert mode inserts
if the section is missing, replaces if present; the delete mode removes
the named section entirely.

## Rolling journal — `doc_journal_update`

Use for recurring loop notes or any document where each entry gets its
own dated window. The tool owns window grouping and pruning so the
model doesn't have to:

```json
{
  "ref": "kb:metacog/journal.md",
  "entry": "Burn ban lifted in Comal County effective today; web source updated.",
  "window": "day",
  "max_windows": 30
}
```

Right for `mode: journal` service loops (`thane_loop_create`, `operation: service`) and any append-only chronology.
Wrong when the goal is to replace what's there — that's `doc_write` or
`doc_edit` with `replace_body`.

---
name: documents_mutate_structure
tags: [documents_mutate_structure]
kind: trailhead
teaser: "Move or copy a section from one managed document into another."
---

# Move or copy a section between documents

When a section belongs in a different document than the one currently
holding it. Both tools upsert the destination section, so the
destination document is created if needed.

## Copy — leave the source intact

`doc_copy_section` is right when the source still needs the section
(canonical home) and the destination needs its own copy (digest,
summary, cross-reference):

```json
{
  "ref": "kb:network/vlans.md",
  "section": "VLAN 30 — IoT",
  "destination_ref": "kb:dashboards/iot-overview.md",
  "destination_section": "Network segment"
}
```

Omit the destination section parameter to reuse the source heading.
Set `destination_level` to change the heading depth in the destination.

## Move — disappear from the source

`doc_move_section` copies then deletes, so the section lives in exactly
one place after the call. Right when reorganizing or splitting a
document:

```json
{
  "ref": "kb:network/vlans.md",
  "section": "Switch port layout",
  "destination_ref": "kb:network/switches.md"
}
```

Use when the section's content was misfiled, not when it should live in
two places.

---
name: documents_mutate_lifecycle
tags: [documents_mutate_lifecycle]
kind: trailhead
teaser: "Whole-document operations — copy a doc, rename its ref, or remove it from the corpus."
---

# Whole-document lifecycle

Operations on the document as a unit.

## Branch from a template — `doc_copy`

Use when a new document should start from an existing one without
disturbing the source. Templating, variants, forking a draft:

```json
{
  "ref": "kb:templates/postmortem.md",
  "destination_ref": "kb:postmortems/2026-05-23-imap-outage.md"
}
```

Set `overwrite: true` only when an existing destination should be
replaced.

## Rename in place — `doc_move`

Use when the document should live at a new ref but remain the same
document in the corpus. Reorganizing, promoting from `scratchpad:` to
`kb:`, fixing a misfiled path:

```json
{
  "ref": "scratchpad:vlan-notes.md",
  "destination_ref": "kb:network/vlans.md"
}
```

## Remove from the corpus — `doc_delete`

Use when the document should leave the managed corpus entirely. The
tool removes the file and updates the index:

```json
{
  "ref": "kb:obsolete/old-vpn-config.md"
}
```

Prefer `doc_move` to a `scratchpad:archive/` ref over `doc_delete` when
the content might still be useful as reference. Delete is for things
that should be gone, not things that should be quiet.

---
name: documents_curate
tags: [documents_curate]
kind: trailhead
teaser: "Introduce brand-new knowledge into the corpus — intake decides title/path/tags before writing."
---

# Curate new knowledge into the corpus

A two-step pipeline for proposing new managed knowledge: `doc_intake`
analyzes where the knowledge belongs, then `doc_commit` performs the
mutation through the approved plan. Use this instead of `doc_write`
when the document doesn't exist yet and you want the system to decide
the title, path, tags, and target action.

## Step 1 — propose and analyze

`doc_intake` searches related documents, normalizes the proposed
title/tags/path against the corpus, checks the target root's policy,
and returns an `intake_id` plus a `commit_plan`:

```json
{
  "root": "kb",
  "intent": "Capture the new VLAN renumbering decision as a fresh knowledge document; expect to create it.",
  "summary": "Renumbered guest VLAN from 50 to 40 to match the new switch layout; documents the rationale and the rollback plan.",
  "body_snippet": "# Guest VLAN renumber\n\nOn 2026-05-20 we renumbered the guest VLAN from 50 to 40...",
  "tags": ["network", "vlan", "decision"],
  "path_prefix": "network/decisions"
}
```

The response tells you the recommended action — create-new,
update-existing, append-existing, or draft-for-review — along with the
normalized destination ref and any cautions worth reconsidering.

## Step 2 — commit through the plan

`doc_commit` takes the `intake_id` and applies the approved action.
Pass the full body content with the commit; the intake step doesn't
hold it for you:

```json
{
  "intake_id": "intake_01HXY...",
  "action": "create_new",
  "body": "# Guest VLAN renumber\n\nOn 2026-05-20 we renumbered the guest VLAN from 50 to 40 to match the new 48-port switch layout. Rollback: restore the prior DHCP pool and re-tag port 47 to VLAN 50.\n"
}
```

Set `confirm: true` only when overriding a caution the intake step
flagged, or when intentionally choosing a different action than the
recommendation. For `append_existing`, add `window: "day" | "week" |
"month"` to control the journal window grouping.
