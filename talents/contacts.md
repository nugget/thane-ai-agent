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
it: the email send decision reads each recipient's trust zone
against the sending account's policy, message channels resolve incoming
senders against it, owner-only tools assert identity through it. Get the contact record right and the rest of the
agent's people-shaped work works; get it wrong and the consequences
ripple.

## The single most important disambiguation

**Contacts hold *person identity*. Memory holds *stable host-level
facts*. Documents hold *evolving knowledge*. Archive holds *past
conversation words*.** The four are easily confused because the model
often reaches for one when the right answer is another:

| You want to store / find... | Surface |
|---|---|
| "Frank prefers Signal" / "Alice is Engineering Lead at X" / "Bob's home address" | `contacts` (`contact_save` with `facts` or `note`) — structured person data |
| "How Frank and I work together" / evolving preferences, themes, or relationship synthesis | `contacts` (`contact_dossier_read`, then `contact_dossier_write`) — longitudinal dossier prose |
| "Who operates this host" | `contacts` (`contact_owner`) — this leaf |
| "Sump pump runs Tuesdays" / "Garage door takes 23s to close" — stable, compact, *non-person* facts | `memory` (`remember_fact`) — see [`memory.md`](memory.md) |
| "The VLAN renumber plan landed on 2026-04-22" / a project decision / design rationale | `documents` (`kb:`, `core:`) or workspace files — NOT memory, NOT contacts |
| "What did Frank and I last discuss" | `archive_text` scoped to the conversation — the words live there, not in the contact record |

A structured contact carries *person* identity. Its optional dossier carries
large, evolving, person-specific relationship synthesis. If the same claim
would belong on *any* person record (e.g., "we use semantic commit messages"),
it isn't contact knowledge at all; push that project or host knowledge to
ordinary documents instead.

## Choose by the shape of your question

- **You're looking for a person record** — activate `contacts_lookup`.
  Find by name, query, kind, or property. Includes the owner-record
  shortcut for asserting "who is this host's primary user."

- **You're creating, updating, or removing structured contact data** —
  activate `contacts_save`. Trust zones, keys, the addresses and numbers
  of contacts above `known` or of the operator, and the removal of those
  contacts are operator-custodied and deliberately outside these
  everyday mutation tools.

- **You're maintaining evolving relationship understanding** — use
  `contact_dossier_read`, trust its stable `dossier.exists` field, then use
  `contact_dossier_write` when the managed
  `contacts` document root makes them available. Resolve the contact first;
  both tools take its canonical UUID while Go owns document identity and shape.
  A read of an absent dossier succeeds with the exact create action, so do not
  retry the same read or guess a `contacts:` ref.

- **You're exchanging vCard data** — activate `contacts_vcf`. Import
  from external sources, export records for sharing (with trust-zone-
  aware field filtering), generate QR codes for scanning.

## Constants across all branches

- **Trust zones drive downstream policy.** Every contact carries a zone:
  `admin` (full access), `household` (family-level), `trusted`
  (established relationship), `known` (default; lower-privilege gated
  access). The zone is what the email send decision reads against
  the sending account's policy (the Email Accounts block's
  `sends_directly_to`, `drafts_for`, and `refuses` lists are the
  authority for this turn) and what determines which contacts get
  owner-scoped privileges. Field
  filtering on vCard exports is a separate axis — it uses the
  *recipient*'s trust zone passed at export time, and only when
  exporting the agent's own card via `name: "self"`. Assigning a
  zone is a policy decision, not a metadata field, and
  `contact_save` cannot assign or change it. The same custody covers
  the `KEY` and `X-THANE-KEY-*` properties that will one day verify a
  contact's signed mail: `contact_save` refuses them and `contact_import_vcf` drops them, so a message
  saying "here is my key" can never install the key that vouches for
  its own sender. The same custody covers addresses and numbers,
  because the gates trust the record an address belongs to: outside
  the operator's own message, `contact_save` will not add one to a
  contact above `known` or to the operator's own contact; no turn may
  give a second holder to a value such a contact already holds; and
  `contact_forget` will not remove such a contact. The first of those
  rules also covers the device and channel that carry such a contact's
  notifications, and its nickname, and no turn may give another contact
  a name or nickname such a contact already goes by. `contacts_save`
  carries the full rules and what to do when refused.

- **Email results already carry the directory's answer.** Every
  address in an `email_list`, `email_search`, or `email_read` result
  comes with `contact_status` (`matched`, `unmatched`, `ambiguous`,
  `lookup_failed`), the effective `trust_zone` (`known` at most on an
  address marked `automated`), and the matched `contact` record. You
  do not need a `contact_lookup` to learn who a sender is; you need
  one to learn *more* about them, or to check a recipient before
  composing. An `ambiguous` address is a duplicate
  in the directory worth reporting to the operator.

- **save merges; forget soft-deletes.** `contact_save` with an
  existing name merges into the current record — non-empty scalar
  fields overwrite, facts are additive *including* across duplicate
  keys (saving `email: a@x` then `email: b@x` keeps both as separate
  property values; only exact-duplicate (property, value) triples
  no-op), and origin arrays replace. There's no "update" tool
  separate from save; the save IS the update. Outside the operator's
  own message, addresses, numbers, and notification routing facts are
  additive only on a `known` contact that is not the operator's. By contrast, `contact_forget`
  removes one `known` contact, by name or by `contact_id`, from every
  query and gate and names the record it removed; it refuses contacts
  above `known`, the operator's own, and ones bound to a Home Assistant
  person. Lookup before forgetting; the cost of removing the wrong
  record is real.

- **A real save queues synthesis once.** A committed model-authored change
  records turn provenance on the property rows it adds or replaces and
  coalesces one `contact:<uuid>` archivist item. Rejected, rolled-back, and
  identical no-op calls do neither. The later dossier pass is for synthesis,
  not a reason to copy directory fields into prose during the save turn.

- **The operator is a contact too.** The host's primary operator lives in
  the same table as everyone else, marked by the stable
  `identity.operator_contact_id` config, the legacy name selector, or
  (fallback) by being the sole `admin`-zone contact. Treat
  `contact_owner` as authoritative for "who operates this host"
  rather than guessing from message senders or workspace metadata.

- **Dossier prose is synthesis, never structured authority.** Read the
  canonical dossier with `contact_dossier_read`; create or replace it with
  `contact_dossier_write`. Do not construct `contacts:<uuid>.md` for `doc_read`
  or use generic document mutations for ordinary dossier authoring. The ref and
  frontmatter already establish which contact the document describes, so pass
  the UUID only as the contact ID argument: never repeat the subject's UUID,
  derived ref, or private tag in the projections, and do not add a `Subject`
  section that merely copies directory fields. Omit the contact's canonical
  name from the status line and teaser because the dossier title already
  supplies it; digest and full may use the name where standalone prose needs
  it. Do not encode trust, Home Assistant bindings, or companion attribution as
  if prose changed those sources of truth. A write that fails validation
  stores nothing and lists every violation at once, each over-budget
  projection with its overage and whether rewording closes it or whole items
  must go; fix them all in the next call. Replacing an existing dossier with
  no read of it on record, or after it changed since that read, is also an
  error that stores nothing: read it again with `contact_dossier_read`, fold
  in the intervening change the error carries, and write again. The digest is
  bounded current state
  about the person, rewritten whole each time: drop what is resolved or
  superseded rather than appending to it. A contact's first dossier is
  refused while a contact that shares its name and looks like the same
  person already has one. The refusal names the evidence and which
  record keeps the dossier; if the two are different people, write
  nothing and report both to the operator (see Duplicates in
  `contacts_save`).

## Cross-references

- For sending mail after looking up a recipient, bounce to `email` —
  the send decision reads the trust zone assigned here against the
  sending account's policy. The Email Accounts block lists, per
  account and for this turn, which zones it `sends_directly_to`,
  `drafts_for`, and `refuses`, and a refusal's `decision.recipients`
  names each recipient at issue with its recovery. The gate is
  all-or-nothing: any refused recipient refuses the whole message. An
  account whose entry shows `draft_gate: "relaxed"` drafts, rather
  than refuses, a recipient refused only for its zone, `known`
  included, because the operator sends every draft there by hand.
- For Signal messages, the same contact directory backs sender
  recognition; activate `signal` for the messaging side.
- For "what did this person and I last discuss" beyond what's in the
  contact note, bounce to `archive_text` scoped to a conversation.
- For a durable synthesis of how the relationship is evolving, resolve the
  person here, call `contact_dossier_read`, and then call
  `contact_dossier_write` when the synthesis changes; use the archive as
  evidence, not as the final home of the synthesis.
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

`contact_lookup` with `name` is the fastest path. It matches a
contact's formatted name or nickname, case-insensitive:

```json
{
  "name": "Frank"
}
```

When several contacts hold the name as their formatted name or
nickname, you get one of them: the operator's own contact first, then
a contact above `known`, then a formatted-name match before a nickname
match, then the lowest ID. So a `known` contact whose formatted name
or nickname a contact above `known` also goes by is never what that
name returns.

Only when no contact holds the name that way does the lookup try each
contact's given name and the first word of its formatted name, and
then exactly one contact must fit: `Alice` returns Alice Jones when no
other contact's given name or first word is Alice. When two or more
fit, such as a household Dave Rivera and a `known` Dave Smith, the
lookup returns neither, whatever their zones, because a first name two
people share does not say which one is meant. The error lists up to
five of them, each with its formatted name, trust zone, `contact_id`,
and the field it matched, and counts the rest; `query` set to that
name lists every contact that fits ahead of any other match, each
with its `contact_id`, so it reaches the ones the error left out while
no more than 50 share the name. Retry with the full formatted name of the one you mean, or pass
its `contact_id` to a tool that takes one; if the conversation does
not settle which, ask rather than guess.

A whole name still beats a first name: `Bob` returns a `known` contact
named just Bob, not a household Bob Smith. When a first name returns a
`known` contact, check it against the `contact_directory` row of
`system_health` before acting on it (see Duplicates in
`contacts_save`).

A name is matched against those name fields and nothing else. A note,
AI summary, or organization that mentions someone never makes that
contact the person: "Dave's partner" in Carol's note does not make
Carol the answer to `Dave`. To find contacts by what their text says,
use `query`, below.

Returns the contact record if found, including all facts, trust
zone, origin policy, and metadata, and, when contact dossiers are
configured, its `Contact ID` and dossier trailhead. Missing returns a simple
not-found message — there are no structured search hints in the
result, so on a miss the next move is yours: re-query with `query`
or a `key`/`value` filter, or decide to `contact_save` deliberately.

## You have partial information

`contact_lookup` with `query` runs a full-text/LIKE search across
the contact's text fields — `formatted_name`, `nickname`,
`given_name`, `note`, `ai_summary`, `org`. **It does not search
properties/facts** —
the property store is keyed and queried separately. To match on a
specific property value (e.g., "find the contact with this email"),
use the `key` + `value` filter below instead of `query`:

```json
{
  "query": "Anthropic"
}
```

Returns up to 50 matching contacts, those whose formatted name,
nickname, given name, or first word the query is listed first, and
says so when more match than it lists. Each row carries the contact's
`contact_id` and trust zone, and a contact that answers to the query
as a name also names the field it answers by. Useful when the name in the
input is the person's company, their title, or a partial spelling. It is the
only lookup that reads those text fields, and it returns a list to
choose from, never an answer to who a name is.

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

## You need the host's operator

`contact_owner` returns the primary operator's record with rich
detail plus a structured summary of currently active operator-scoped
channels:

```json
{}
```

No arguments needed. Uses `identity.operator_contact_id` from config
when set, accepts the legacy name selector for existing installs, and
otherwise falls back to the sole `admin`-zone contact if exactly one
exists. Right tool when the model needs to assert "this is the operator
I'm talking to" or "what channels does the operator have active right
now."

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
teaser: "Create, update, or remove ordinary contact data; zones, keys, and elevated contacts' addresses, notification routing, and names stay operator-custodied."
---

# Save

You're mutating ordinary directory data. Two tools cover this surface:
one writes and one deletes. Trust zones, keys, identity bindings, the
addresses and numbers that decide who a message is from, the facts
that route a person's notifications, and the names people are found
by are not ordinary contact data. Go refuses to change them here, and
a refusal saves or removes nothing and says what to do instead.

## Custody

Each rule below keeps authority with the operator, and they share one
recovery: tell the operator exactly what should change on which
contact, and the operator makes the change through CardDAV or the
contacts API. Where a rule is lifted in the operator's own message,
the operator can instead ask you for the change there.

### Trust zones

`contact_save` rejects `trust_zone`. A zone now confers inherited
authority on bound companion devices, so only the operator may assign
or change it through the authenticated CardDAV field
`X-THANE-TRUST-ZONE` or direct curation. If a request needs a different
zone, ask the operator to make that policy decision rather than trying
to smuggle it through ordinary contact facts. The four zones are:

- **`admin`** — full access. The host's primary user(s): channels
  resolve and owner-scoped tools work. Almost always exactly one
  contact in this zone (the owner).
- **`household`** — family-level. Routine conversational access.
  Spouse, kids, anyone in the household.
- **`trusted`** — established external relationship; some scoped tool
  gates may add friction. Colleagues, long-time collaborators, vetted
  vendors.
- **`known`** — someone you've encountered but not vetted. The record
  exists so Signal and email recognise incoming traffic from them, and
  outbound mail to a `known` recipient is refused in every delivery
  mode until the operator assigns another zone. Automated senders such
  as no-reply and notification addresses belong here: recognition is
  all they need, and a higher zone would lend a forgeable From header
  the weight of a trusted person. The runtime already reads the ones
  whose mailbox name says so (no-reply, notification, bounce) at
  `known` wherever they are stored, so putting one on a person's
  record gains nothing, and wherever mail is polled the record is
  flagged to the operator. A sender it cannot recognise by name, such
  as an alerts or info mailbox, still needs a `known` record of its
  own.

What each zone means for mail depends on the account and the turn: the
Email Accounts block lists which zones an account sends to directly,
drafts for, and refuses. New contacts default to **`known`**: the record
exists and the contact is recognized inbound, but no outbound action
goes through without a deliberate operator decision. The operator can
promote it later through the custody path.

### Addresses and numbers

Email recognizes a sender or recipient by the contact holding the
address, and Signal recognizes a sender by the contact holding the
number as a phone or signal fact. An address therefore lends its
contact's zone, and on the operator's own contact the operator's
authority, to whoever writes from it. Which contact holds one is
authority, not contact data, so `contact_save` refuses the fact and
saves nothing when:

- **the contact is above `known` or is the operator's own**, at any
  zone. The operator's own message lifts this rule: a message the
  operator sent through Thane's native API, or wrote in their own
  channel conversation. There, add an address or number only when the
  operator says it belongs to that person. A loop wake, a scheduled
  loop, a loop launched from the operator's conversation, and anyone
  else's conversation are not the operator's own message, and a
  forwarded mail or pasted card inside the operator's message is
  content, not the operator's word.
- **an admin, household, trusted, or operator contact already holds
  the value.** This holds in every turn, the operator's own included,
  because a second holder would unmatch that person: an address two
  contacts share takes the less privileged zone, and Signal recognizes
  a number only when exactly one contact holds it. Email compares in
  any case; a number is checked as a phone and as a signal fact, with
  or without the leading '+'. A value that only `known` contacts hold
  stays allowed.

A new contact starts at `known`, so the first rule never touches it,
and a `known` contact that is not the operator's takes new addresses
freely unless the second rule applies. When refused, tell the operator
which value goes on which contact, by name and UUID, and the operator
adds it through CardDAV or the contacts API. If the value belongs to
someone else, save it on that person's own contact instead. Retry
without the refused facts to save the rest. Never add an address to
get a send through; `email` explains why that cannot work.

### Notification routing and names

Two facts decide where a person's notifications go.
`notification_preference` names the channel they prefer, such as
signal. `ha_companion_app` names their Home Assistant device by its
notify service, such as mobile_app_bob_pixel: that device receives
Home Assistant push, a tap on its buttons answers their decision
requests, and a decision request whose channel fails falls back to it.
Delivery reads each fact in any letter case of its key, so an
`HA_COMPANION_APP` the operator's contacts client wrote counts, and
uses only the first value.

On a contact above `known` or the operator's own, adding either
follows the first rule for addresses: `contact_save` refuses it
outside the operator's own message, and `contact_import_vcf` drops it
from a merge into such a contact. There is no second-holder rule for
them: a household can share a tablet, and two people can prefer the
same channel. Re-saving a value the contact already has, under any
letter case of the key, changes nothing.

When the operator tells you in their own message that someone has a
new device, save it on that person:

```json
{
  "name": "Bob Smith",
  "facts": {"ha_companion_app": "mobile_app_bob_pixel"}
}
```

Facts are additive and delivery uses the first value, so if Bob
already had a device, the result says the new one was recorded but
Home Assistant push still goes to the old one. Tell the operator that,
and which value to remove; they remove it through CardDAV or the
contacts API. Only a result without that note means Home Assistant
push, including `ha_notify` and a decision request whose channel
fails, now reaches the new device; `send_notification` still tries
Bob's `notification_preference`, then a channel he is active on,
first. Outside the operator's own message, on a contact above `known`
or the operator's own (a wake, a loop, or Bob telling you about his
own new phone), save nothing, and tell the operator what should change
on whom.

A name or nickname is how a person is found: notifications, decision
requests, and lookups find the contact whose formatted name or
nickname it is, the operator's own contact first, then one above
`known`, then a formatted-name match before a nickname match. Only
when no contact holds the name that way do they take the one contact
whose given name or first word it is, and a first name two contacts
share reaches neither. Conversation context does the same when a
channel has not bound the sender to a contact. A second contact answering to a person's name
still splits them: between two `known` contacts it can take their
notifications and decision requests, and a first name that is another
contact's whole name ("Bob" beside "Bob Smith") reaches that contact,
not them.

- **Changing the nickname** of a contact above `known` or of the
  operator's own follows the first rule for addresses: only in the
  operator's own message. A change only in the case of ASCII letters
  is not a change.
- **A name or nickname someone with authority goes by.** In every
  turn, the operator's own included, `contact_save` refuses a new
  contact's name, or any contact's nickname, that an admin,
  household, trusted, or operator contact already goes by as its name
  or nickname, compared as lookups compare them. `contact_import_vcf`
  leaves such a card out and does not fill in such a nickname. Save
  the new person under a fuller name ("Bob Jones", not "Bob") or
  without the nickname; if you meant the existing person, save to
  their contact by its exact name. On a contact that already exists,
  leave the nickname off or pick one nobody with authority goes by,
  since a fuller name there would create a second contact. Two people
  sharing a name or nickname on purpose is a card edit the operator
  makes.
- **Descriptions are not names.** A contact's note, org, and AI
  summary never resolve a name, so a notification or decision request
  addressed by a description ("the plumber") reaches no one, not
  whoever's note mentions it. Address one by the person's exact name
  or nickname. A given name or first word works only while no other
  contact has it, and nothing stops another contact from taking it
  later, so a handle someone must always be reachable by belongs in
  their formatted name or nickname.

`kind` (individual, group, org, location) describes a contact and
decides nothing: no gate, notification route, or operator check reads
it.

On an older configuration the operator is found by name rather than by
UUID. There, `contact_save` also refuses, in every turn, to create a
contact or set a nickname that carries the name Thane recognizes the
operator by on anyone but the operator, and to replace that nickname
on the operator's own contact when the contact answers to the name
only through it (a change of case is fine); `contact_import_vcf`
leaves such a card out, or leaves the nickname off a card it merges.
A second contact with that name could take the operator's identity,
and an operator contact without it would not be found as the operator
at the next start. If the refused contact is the operator, save to
their existing contact, which `contact_owner` returns, and leave that
nickname alone; if it is someone else, use a fuller name.

### Fact keys

A fact key becomes a vCard property name that the operator's contacts
client reads back, so a key is a plain name: letters, digits,
'-' and '_', starting with a letter, at most 64 characters. '.', ';',
':', spaces and line breaks are vCard syntax that would decode as a
different property, such as a live email address or a trust zone, so
a key like "home phone" or "item1.email" is refused; the error
suggests an underscore spelling. `KEY`, `X-THANE-KEY-*`, and every
other `X-THANE-*` key are refused on every contact: keys authenticate
a contact's messages, and the X-THANE headers carry the trust zone,
Home Assistant binding, AI summary, and origin policy, which have
their own arguments or belong to the operator. A key naming a field
the contact record itself owns is refused as well, because a fact
under that name never reaches the operator's contacts client and is
lost on their next edit: note, title, role, org, nickname and kind
have their own arguments, and the vCard names FN, N, BDAY,
ANNIVERSARY, GENDER, PHOTO, UID, REV and VERSION belong to the
operator. A fact value containing a line break or other control
character is refused too, and so is a carriage return in any other
argument; note and the AI summary may hold plain line breaks.

## Create or update a person

`contact_save` takes `name` plus ordinary person attributes; the merge
semantics let you add details incrementally:

```json
{
  "name": "Frank Smith",
  "kind": "individual",
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
overwrite**, **facts are additive even across duplicate keys** (a
new value for an existing key is added as another property value
rather than replacing the prior one — the contact ends up with
multiple `email` / `phone` / etc. entries; only exact-duplicate
(property, value) triples no-op, and a routing fact's value no-ops
under any letter case of its key), and **origin arrays are
replaced** when provided (pass `[]` to clear). To leave a field
alone, omit it. Addresses, numbers, routing facts, and nicknames
follow the Custody rules above. Notifications use only the first value
of each routing fact, so a second `ha_companion_app` is recorded
without moving delivery, and the result says so. No model-facing tool
removes or replaces a single property value: to replace or remove an
address, number, device, or other multi-valued property, read the
record, decide what should remain, and ask the operator to edit the
card through CardDAV or the contacts API.

## Standard keys map to vCard properties automatically

In `facts`, `email` → `EMAIL`, `phone` → `TEL`, and `signal` and
`matrix` → `IMPP` with a `signal:` or `matrix:` prefix; `EMAIL`,
`TEL`, and `IMPP` written in any case land on the same property.
Those are the addresses and numbers the Custody rules govern. Custom
keys are stored as written and must be plain names (see Fact keys).
The QR-card and vCard exports use the mapped property
names; the model-facing lookup syntax accepts either form (`key:
"email"` and `key: "EMAIL"` both work).

## What does NOT belong in a contact

`contact_save` is for compact structured person data, not document-shaped
understanding. Evolving person-specific relationship or collaboration
synthesis belongs in `contact_dossier_write`. Project knowledge, technical
decisions, and other claims that are not specifically about this person belong
in `memory` (`remember_fact`) or ordinary documents. The structured contact
record is *who*; the dossier is *how this relationship currently makes sense*.

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

`contact_forget` removes one `known` contact from the directory. Pass
exactly one of `name` or `contact_id`:

```json
{
  "name": "Frank Smith"
}
```

A name resolves once, the way `contact_lookup` resolves it; a first
name two contacts share resolves to neither and removes nothing, and
the error lists up to five of their `contact_id` values. A
`contact_id` is the canonical
UUID of one active contact, lowercase with hyphens, and removes
exactly that contact:

```json
{
  "contact_id": "0d1f8a6e-4c2b-4b7e-9f00-3a7d0e2c9b41"
}
```

The result names what was removed, as
`Forgot contact: Frank Smith (known, <uuid>)`; check it, because a
nickname or first-name match can land on a record you did not mean.

It refuses, in every turn including the operator's own message, and
whether you pass a name or a `contact_id`, a contact above `known`,
the operator's own contact, and a contact bound to a Home Assistant
person. Forgetting one turns that person's email and Signal traffic
into a stranger's. Nothing is removed; tell the operator, who can
delete the contact through CardDAV or `DELETE /v1/contacts/{id}`. The
refusal names the other remedy when one exists: demoting a contact
above `known`, or removing a Home Assistant person binding. The
operator's own contact is only ever deleted by the operator. When you
passed a name, the refusal also names up to three `known` contacts
that the name also fits and that it could remove, with their UUIDs; if
one of them is the record you meant, its `contact_id` reaches it.

The store implements this as a **soft delete** (sets `deleted_at`),
and **there is no undo path through the model-facing tools** — once
forgotten, the contact is gone from every query and gate the model
can reach. The operator can bring it back by putting a card to its
UUID through CardDAV or the contacts API, so report the UUID from the
result when a forget was a mistake. Past references in archive
transcripts and email threads still mention the person by name, but
any tool that resolves against the directory (email send policy,
signal sender recognition) will treat that person as unknown on the
next encounter.

**Lookup before forgetting.** Confirm you have the right record. The
cost of removing the wrong contact is real, and only the operator can
reverse it.

## Duplicates

One person saved as two contacts is split between them: a lookup, a
notification, Home Assistant presence, or a dossier can land on the
record you did not mean. Go looks for the splits that can misroute.
The `contact_directory` row of `system_health` stays degraded while
any exists, naming up to five, and each one is logged when Thane
starts. Every finding involves a contact above `known` or the
operator's own; two ordinary `known` contacts never raise one. A
finding is one of:

- **`name`**: contacts that share a name and look like one person.
  They share a name when one answers to the other's formatted name or
  nickname, or one's whole formatted name is the other's given name or
  the first word of its formatted name ("Bob" beside "Bob Smith").
  They look like one person when they share an address or number, when
  the one with no more authority holds no real address or number of
  its own, or when one is a `known` contact bound to a Home Assistant
  person. A finding lists only the contacts that evidence ties
  together. A contact that looks like a copy of two different people,
  such as a bare "Dave" beside "Dave Rivera" and "Dave Smith", is listed
  in a finding with each, because nothing in the records says which one
  it copies; which one is the operator's to say.
- **`shared_name`**: different people who each answer to one formatted
  name or nickname with no sign between them of being one person, such
  as two people nicknamed "Mom". It names one contact per person; a
  person saved twice is also its own `name` finding. A name lookup
  reaches only one of them.
- **`email`**: contacts that hold one address, in any case, when a
  `known` holder makes email read it at `known` for all of them, or a
  holder has no other address of its own. A mailbox that contacts with
  authority share while each holds its own addresses is not a finding;
  email reads it as `ambiguous`.
- **`phone`**: contacts that hold one number as a signal fact or as a
  mobile or untyped phone, with or without the leading '+'. A home,
  work, or main line listed on several cards is not a finding.
- **`reserved_domain`**: a contact above `known`, or the operator's
  own, holds an email address at a domain reserved for examples and
  tests: `example.com`, `example.net`, or `example.org`, a name under
  one of them, or a name in the `example`, `test`, `invalid`, or
  `localhost` top-level domain. No mailbox exists there, so the address
  is a placeholder that still carries the contact's zone.

Each finding names the contacts with zone and UUID, and marks the
operator's own and any bound to a Home Assistant person. A
`contact_dossier_write` refusal to start a second dossier and an
`ambiguous` email address are the same problem seen from a tool.

**A finding is not proof that two contacts are one person.** Before
you save, merge, or forget anything, you need one of these: the two
share an address or number; the contact that would go holds no
address or number of its own; or the operator says, in their own
message, that they are one person. A `shared_name` finding, or a
`name` finding between contacts that each hold their own addresses, is
the operator's to judge: report it with each contact's name, zone, and
UUID, and change nothing. So is a contact listed in more than one
`name` finding: it could be a copy of any of those people, so report
every finding it is in, and do not forget it or fold its dossier or
addresses into any of them.

With that evidence, who fixes it depends on the duplicate, the contact
that should go:

- **A `known` duplicate that is not the operator's and not bound to a
  Home Assistant person is yours to remove.** Save anything worth
  keeping onto the contact that stays, with `contact_save` under that
  contact's exact formatted name: save matches only a formatted name,
  so the shared short name would update the duplicate. Custody still
  applies there, so outside the operator's own message, report
  addresses and numbers meant for a contact above `known` instead of
  saving them. If the duplicate has a dossier, read it with
  `contact_dossier_read` first, because no dossier tool reaches a
  forgotten contact. Then forget the duplicate, by `contact_id` when
  its name resolves to the contact that stays. When the shared name
  resolves to the duplicate itself, the duplicate is the only contact
  holding that name exactly, as a bare "Dave" beside a household
  "Dave Rivera" is, and the contact that stays answers to it only by
  its given name or first word. Forgetting the duplicate leaves the
  name to the first-name rule: "Dave" then reaches Dave Rivera only
  while no other active contact has Dave as its given name or first
  word, and no one once another does. Say so when you report the
  forget, naming the contact that stays, so the operator can give it
  the name as a nickname through CardDAV. A nickname on a contact
  above `known` lands on the card in the operator's address book, so
  set it yourself only when the operator asks in their own message.
  Last, fold the
  duplicate's dossier claims and citations into the surviving
  contact's dossier with `contact_dossier_write`: an existing dossier
  updates as usual, and a first one for the survivor is refused only
  while a duplicate holding a dossier is still active.
- **Any other duplicate is the operator's.** Report the set with each
  contact's name, zone, and UUID. The operator merges the duplicate
  into the contact that should keep the name, renames one, or moves an
  address to the contact that owns it, through CardDAV or
  `/v1/contacts`. No model-facing tool renames a contact or removes an
  address, so a placeholder address is the operator's to replace or
  remove, in any turn. The operator's own message lifts only the
  rule on changing a contact above `known` or the operator's own, so
  there you can add the duplicate's addresses to the contact that
  stays; the forget refusal holds in every turn, and a Home Assistant
  person binding moves only in the operator's config.

## Cross-references

- Before saving, almost always do a `contacts_lookup` first — the
  merge semantics mean partial duplicates are easy to create through
  typos in the name.
- After the operator changes a trust zone through its custody path,
  the `email` send gate reads that zone immediately — no extra step
  or contact rewrite is needed.
- For "I want to remember a project decision but the natural place
  is a person record," that's a smell that you actually want
  `memory` (`remember_fact`) instead.
- For relationship patterns or evolving context that are genuinely about the
  person, resolve the canonical UUID and use `contact_dossier_read` followed by
  `contact_dossier_write` rather than expanding `note`, `ai_summary`, or
  `facts` into a document.

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
incoming vCard didn't carry a zone. Promoting or demoting a contact's
trust is an operator-custodied action, not a side effect of import or
an ordinary `contact_save`.

Addresses and numbers follow the same custody as `contact_save`, with
no exception for the operator's own message, because a vCard is
content, not the operator's word. An `EMAIL`, `TEL`, or `IMPP` value
is dropped when the merge target is above `known` or is the
operator's own contact, and on any target, new or merged, when an
admin, household, trusted, or operator contact already holds it.
Properties whose decoded names are not plain names (a nested group
such as a.b.EMAIL, spaces, or more than 64 characters) are dropped
too, as are `KEY` and `X-THANE-KEY-*` and values carrying a carriage
return or other control character; a single group such as
item1.EMAIL imports as EMAIL under the address rules above. The rest
of the card still imports. The result counts each kind of drop, not
the values themselves, and a dry run reports the same counts, so a
preview shows how many values the operator will have to add by hand.
Each card's addresses and numbers, and its merge target's zone, are
checked again inside the write that stores it, so a value the operator
gives such a contact while the import runs is dropped too, and a card
whose merge target the operator re-zones (promotes or demotes) or
deletes meanwhile is skipped, not merged. A card is also skipped when
its write fails, as it can when an operator change to the contacts
collides with it, and when it is left with no usable name; a dry run
skips a nameless card too. The result names each skipped card with its
cause: import a re-zoned or failed card again with merge on (the
default), and give a nameless card a plain FN first.

## Export one contact as a vCard

`contact_export_vcf` produces a vCard for one contact:

```json
{
  "name": "Frank Smith",
  "format": "file"
}
```

`format: "file"` writes to a temp file (default); `format: "text"`
returns the vCard inline for direct inclusion in a message body.

**`name: "self"` is the special case for field filtering.** When
exporting the agent's own card via `name: "self"`,
`recipient_trust_zone` controls which fields are included so you
don't leak sensitive attributes to a lower-trust recipient:

```json
{
  "name": "self",
  "recipient_trust_zone": "known",
  "format": "text"
}
```

Lower zones get fewer fields (e.g., a `known` recipient won't see
your home address). **For exporting any other contact**,
`recipient_trust_zone` is currently ignored — the export carries
the contact's full data regardless. If you need a redacted vCard
for a third party, the workaround is to copy the contact into your
own self-card representation first, or hand-redact before sending.

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
  collapse), follow Duplicates in `contacts_save`: `contact_lookup` →
  confirm each set is one person → `contact_save` on the canonical one to absorb
  facts → `contact_forget` on each `known`, unbound duplicate that is
  not the operator's, by `contact_id` when its name resolves to
  another record (when it resolves to the duplicate itself, Duplicates
  says what forgetting it does to that name). Custody refuses saving shared addresses onto a
  canonical record above `known` or the operator's outside the
  operator's own message, and refuses forgetting a duplicate that is
  above `known`, the operator's, or bound to a Home Assistant person;
  report those to the operator with each record's name, zone, and UUID
  instead. Multi-step; consider whether a service loop is the better
  shape (`loops_examples_curate`).
- For *sending* the exported card, bounce to `email` or `signal`
  depending on the channel.
- For "merge two contacts" — there's no native merge tool. The
  workflow above (save absorbs, forget removes) is the supported
  pattern for removing a `known`, unbound duplicate, even one that
  shares its name with an elevated contact; moving addresses onto an
  elevated or operator contact outside the operator's own message, and
  removing one, are the operator's.

- Asked where a contact is, `contact_whereabouts` fuses their
  presence and device locations into one ranked, provenance-carrying
  answer — start there rather than assembling it yourself.
