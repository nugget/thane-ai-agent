---
name: contacts
tags: [contacts]
kind: trailhead
teaser: "Open for the person directory — look up, mutate, or exchange contact records."
next_tags: [contacts_lookup, contacts_save, contacts_vcf]
---

# Contacts

The contact directory is the canonical record of who counts as a known
person, organization, or group on this host. Other surfaces depend on
it: email send policy gates recipients by their trust zone, message
channels resolve incoming senders against it, owner-only tools assert
identity through it. Get the contact record right and the rest of the
agent's people-shaped work works; get it wrong and the consequences
ripple.

## The single most important disambiguation

**Contacts hold *person identity*. Memory holds *stable host-level
facts*. Documents hold *evolving knowledge*. Archive holds *past
conversation words*.** The four are easily confused because the model
often reaches for one when the right answer is another:

| You want to store / find... | Surface |
|---|---|
| "Frank prefers Signal" / "Alice is Engineering Lead at X" / "Bob's home address" | `contacts` (`contact_save` with `facts` or `note`) — this leaf |
| "Who is the owner of this host" | `contacts` (`contact_owner`) — this leaf |
| "Sump pump runs Tuesdays" / "Garage door takes 23s to close" — stable, compact, *non-person* facts | `memory` (`remember_fact`) — see [`memory.md`](memory.md) |
| "The VLAN renumber plan landed on 2026-04-22" / a project decision / design rationale | `documents` (`kb:`, `core:`) or workspace files — NOT memory, NOT contacts |
| "What did Frank and I last discuss" | `archive_text` scoped to the conversation — the words live there, not in the contact record |

A contact carries *person* identity. If the same fact would belong on
*any* person record (e.g., "we use semantic commit messages"), it
isn't a contact fact. If the fact is large, evolving, or
document-shaped, it isn't a memory fact either — push to documents.

## Choose by the shape of your question

- **You're looking for a person record** — activate `contacts_lookup`.
  Find by name, query, kind, or property. Includes the owner-record
  shortcut for asserting "who is this host's primary user."

- **You're creating, updating, or removing a contact** — activate
  `contacts_save`. This is also where the trust-zone decision lives,
  and trust zones gate downstream tools.

- **You're exchanging vCard data** — activate `contacts_vcf`. Import
  from external sources, export records for sharing (with trust-zone-
  aware field filtering), generate QR codes for scanning.

## Constants across all branches

- **Trust zones gate downstream tools.** Every contact carries a zone:
  `admin` (full access), `household` (family-level), `trusted`
  (established relationship), `known` (default; lower-privilege gated
  access). The zone is what `email_send`'s recipient gate reads, what
  shapes which fields appear in an exported vCard, and what determines
  which contacts get owner-scoped privileges. Assigning a zone is a
  policy decision, not a metadata field.

- **save merges; forget destroys.** `contact_save` with an existing
  name merges into the current record — non-empty scalar fields
  overwrite, facts are additive, origin arrays replace. There's no
  "update" tool separate from save; the save IS the update. By
  contrast, `contact_forget` removes the record entirely with no
  tombstone. Lookup before forgetting; the cost of removing the wrong
  record is real.

- **The owner is a contact too.** The host's primary user lives in the
  same table as everyone else, marked by `identity.owner_contact_name`
  config or (fallback) by being the sole `admin`-zone contact. Treat
  `contact_owner` as authoritative for "who is this host's user"
  rather than guessing from message senders or workspace metadata.

## Cross-references

- For sending mail after looking up a recipient, bounce to `email` —
  the recipient gate reads the trust zone assigned here. Only
  `admin`/`household`/`trusted` zones send through;
  `known` zone is rejected with a "promote-or-authorize" message,
  and missing-contact recipients are rejected with a "no contact
  record" message. The gate is all-or-nothing: any rejected
  recipient aborts the whole send.
- For Signal messages, the same contact directory backs sender
  recognition; activate `signal` for the messaging side.
- For "what did this person and I last discuss" beyond what's in the
  contact note, bounce to `archive_text` scoped to a conversation.
- For project knowledge, technical decisions, or persistent facts that
  aren't person-shaped, `memory` (`remember_fact`) is the right home;
  don't pollute contact notes with non-person content.

---
name: contacts_lookup
tags: [contacts_lookup]
kind: trailhead
teaser: "Find an existing contact — by name, query, kind, property, or via the owner shortcut."
---

# Lookup

You're trying to find a person record. Three tools, picked by how
specifically you can name what you're looking for.

## You know the name

`contact_lookup` with `name` is the fastest path. Case-insensitive,
also matches `nickname`:

```json
{
  "name": "Frank"
}
```

Returns the contact record if found, including all facts, trust zone,
origin policy, and metadata. Missing returns a not-found result with
search hints — that's your signal to either re-query with `query` or
decide to `contact_save` deliberately.

## You have partial information

`contact_lookup` with `query` searches across name, nickname, org, and
facts:

```json
{
  "query": "Anthropic"
}
```

Returns matching contacts ranked. Useful when the name in the input is
the person's company, their title, or a partial spelling.

## You know a property value

When you have an email or phone number and want to know whose it is,
filter by key/value:

```json
{
  "key": "email",
  "value": "frank@example.com"
}
```

The key is matched against vCard property names (`email` → `EMAIL`,
`phone` → `TEL`, etc.) plus custom keys like `ha_companion_app` for
ones without standard mappings. Both `key` and `value` are required;
key alone is not a valid filter.

## You want to browse the directory

`contact_list` is the right tool when you don't have a specific
anchor — useful for "show me everyone" or "show me all orgs":

```json
{
  "kind": "individual",
  "limit": 50
}
```

`kind` is `individual` / `group` / `org` / `location`. Without `kind`,
all types appear. Use `limit` to bound the result size.

## You need the host's owner

`contact_owner` returns the primary operator's record with rich
detail plus a structured summary of currently active owner-scoped
channels:

```json
{}
```

No arguments needed. Uses `identity.owner_contact_name` from config
when set; otherwise falls back to the sole `admin`-zone contact if
exactly one exists. Right tool when the model needs to assert "this is
the user I'm talking to" or "what channels does the owner have active
right now."

## Cross-references

- If lookup returns no match and you want to create the contact,
  bounce to `contacts_save`. The two are paired — search-then-save is
  the canonical pattern.
- For finding sender history beyond the contact record itself (past
  conversations, past emails), bounce to `archive_text` with the
  resolved name.
- For "send a mail to this person" after lookup confirms they exist,
  bounce to `email`.

---
name: contacts_save
tags: [contacts_save]
kind: trailhead
teaser: "Create or update a contact — the trust-zone decision is the central call."
---

# Save

You're mutating the directory. Two tools — one to write, one to
delete — and one big decision: what trust zone does this contact
belong in.

## The trust-zone decision

`contact_save` assigns a `trust_zone`, and that zone gates downstream
tool access. The four zones:

- **`admin`** — full access. The host's primary user(s). Mail sends
  through, channels resolve, owner-scoped tools work. Almost always
  exactly one contact in this zone (the owner).
- **`household`** — family-level. Mail sends through. Routine
  conversational access. Spouse, kids, anyone in the household.
- **`trusted`** — established external relationship. Mail sends
  through; some scoped tool gates may add friction. Colleagues,
  long-time collaborators, vetted vendors.
- **`known`** — *send-blocked* zone for someone you've encountered
  but not vetted. The contact record exists (so signal/email can
  recognize incoming traffic from them), but outbound mail to a
  `known` recipient is **rejected** by the trust gate until
  explicitly promoted or authorized. Sensitive fields are also
  stripped from vCard exports targeting `known` recipients.

When uncertain, **`known` is the safe default**: the record exists,
the contact is recognized inbound, but no outbound action goes through
without a deliberate decision. Promoting to `trusted` later is easy;
demoting after the contact has been used for sends is messy.

## Create or update a person

`contact_save` with `name` and `trust_zone`. Properties are person
attributes; the merge semantics let you add details incrementally:

```json
{
  "name": "Frank Smith",
  "kind": "individual",
  "trust_zone": "trusted",
  "given_name": "Frank",
  "family_name": "Smith",
  "org": "Anthropic",
  "title": "Backend Engineer",
  "ai_summary": "Backend engineer at Anthropic; prefers Signal for low-latency, email for async.",
  "facts": {
    "email": "frank@anthropic.com",
    "ha_companion_app": "mobile_app_frank_phone"
  }
}
```

Update semantics: when the record exists, **non-empty scalar fields
overwrite**, **facts are additive** (new keys add, existing keys
update), and **origin arrays are replaced** when provided (pass `[]`
to clear). To leave a field alone, omit it.

## Standard keys map to vCard properties automatically

In `facts`, `email` → `EMAIL`, `phone` → `TEL`, etc. Custom keys are
stored as-is. The QR-card and vCard exports use the mapped property
names; the model-facing lookup syntax accepts either form (`key:
"email"` and `key: "EMAIL"` both work).

## What does NOT belong in a contact

`contact_save`'s description is explicit: **do not store project
knowledge, design philosophy, technical insights, or collaboration
patterns in contact facts**. Those are `memory` (`remember_fact`) or
workspace-file material. The contact directory is *who*, not *what we
decided about*.

If you find yourself writing `facts: { "decision_about_X": "..." }` in
a contact, that's a smell. Refactor: store the decision in a doc;
keep the contact record about the person.

## Origin policy (advanced)

`origin_tags` and `origin_context_refs` shape future sessions where
this contact is the *origin* (the asserted user of the run). Setting
them pins capability tags and injects supplemental KB docs whenever
the conversation runs as this person. Most contacts don't need them;
reach for them when a person has a habitual workflow that benefits
from auto-loaded context.

```json
{
  "name": "Frank Smith",
  "origin_tags": ["forge", "development"],
  "origin_context_refs": ["kb:projects/network-overhaul.md"]
}
```

Pass `[]` to either field to clear it.

**Caveat**: don't set `origin_tags` to `owner` or `message_channel` —
those are runtime-asserted (the runtime knows who's authenticated and
which channel a message came in on; manually pinning them via a
contact would shadow the trustworthy assertion).

## Remove a contact

`contact_forget` deletes the record entirely:

```json
{
  "name": "Frank Smith"
}
```

There's no tombstone, no soft-delete, no undo. Past references in
archive transcripts and email threads still mention the person by
name, but the contact record itself is gone — and any tool that
resolved against it (email send policy, signal sender recognition)
will treat that person as unknown on the next encounter.

**Lookup before forgetting.** Confirm you have the right record. The
cost of removing the wrong contact is real and unrecoverable from
within the tool surface.

## Cross-references

- Before saving, almost always do a `contacts_lookup` first — the
  merge semantics mean partial duplicates are easy to create through
  typos in the name.
- After a save assigns a trust zone, the `email` send gate reads
  that zone immediately — no extra step needed.
- For "I want to remember a project decision but the natural place
  is a person record," that's a smell that you actually want
  `memory` (`remember_fact`) instead.

---
name: contacts_vcf
tags: [contacts_vcf]
kind: trailhead
teaser: "Exchange vCard data — import from a file, export records, generate QR codes."
---

# vCard exchange

Bringing contact data in or out. Four tools: one importer, three
exporters with different shapes.

## Import a vCard

`contact_import_vcf` reads single- or multi-vCard data from a file path or
inline text:

```json
{
  "path": "/tmp/exported.vcf",
  "merge": true,
  "dry_run": true
}
```

`merge: true` (default) matches against existing contacts by email or
name and **fills empty fields only**. `merge: false` always creates
new records (use when you know the existing records should not be
touched). `dry_run: true` previews the import without writing —
**preview before bulk imports**, especially when `merge: false` could
create duplicates.

Trust-zone semantics on import: **`trust_zone` and `ai_summary` are
never overwritten by import.** The import can fill missing fields, but
it cannot demote a `trusted` contact to `known` just because the
incoming vCard didn't carry a zone. Promoting/demoting a contact's
trust is a deliberate `contact_save` action, not a side effect of
import.

## Export one contact as a vCard

`contact_export_vcf` produces a vCard for one contact. The `recipient_trust_zone`
parameter is the trust *of the person you're sharing the card with* —
it filters which fields are included so you don't leak sensitive
attributes:

```json
{
  "name": "Frank Smith",
  "recipient_trust_zone": "known",
  "format": "file"
}
```

`format: "file"` writes to a temp file (default); `format: "text"`
returns the vCard inline for direct inclusion in a message body.

**`name: "self"` is special** — it exports the agent's own contact
card with `recipient_trust_zone`-aware field filtering. Right tool for
"send the recipient your contact info." Lower zones get fewer fields
(e.g., a `known` recipient won't see your home address).

## Export all contacts (backup or bulk transfer)

`contact_export_all_vcf` produces a multi-vCard file:

```json
{
  "kind": "individual",
  "trust_zone": "household"
}
```

Both `kind` and `trust_zone` filter the exported set. Without filters,
the whole directory exports. Useful for backups before destructive
operations or for migrating to another host.

## Generate a QR code

`contact_export_vcf_qr` produces a PNG containing the vCard, scannable from a
phone:

```json
{
  "name": "Frank Smith",
  "recipient_trust_zone": "trusted"
}
```

QR codes have capacity limits; the `recipient_trust_zone` filtering
keeps the encoded vCard small enough to scan reliably. As with
`contact_export_vcf`, `name: "self"` exports the agent's own card.

## Cross-references

- For bulk *deduplication* after import (multiple records that should
  collapse), the loop is `contact_lookup` → identify duplicates →
  `contact_save` on the canonical one to absorb facts → `contact_forget`
  on the duplicates. Multi-step; consider whether a curate loop is the
  better shape (`loops_examples_curate`).
- For *sending* the exported card, bounce to `email` or `signal`
  depending on the channel.
- For "merge two contacts" — there's no native merge tool. The
  workflow above (save absorbs, forget removes) is the supported
  pattern.
