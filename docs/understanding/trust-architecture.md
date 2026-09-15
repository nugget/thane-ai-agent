# Trust Architecture

> Any system whose safety depends on an actor's intent will fail.
> The only systems that hold are the ones where safety is structural.

This document maps Thane's design to the principle that **safety must be a
property of the system, not a hope about the actors inside it**. Every
enforcement point should work in Go code, not prompt instructions. The model
sees the result; Go makes the decision.

## The Principle

Prompt instructions are behavioral controls. They reduce harmful behavior
but don't eliminate it. Anthropic's own research demonstrated that explicit
"do not blackmail" instructions reduced the behavior from 96% to 37% —
meaning more than a third of the time, models acknowledged the constraint
and proceeded anyway.

Thane's design philosophy: **enforce in Go, not prompts.** Where we rely on
prompt compliance, we acknowledge the gap and plan structural replacements.

## Current Structural Enforcement

### Core Identity Bootstrap

**Status: Implemented**

`thane init` creates `core/` as the instance trust root. It generates an
Ed25519 SSH signing key, an internal Ed25519 X.509 channel CA, and
`core/config.yaml` with the initial identity declaration and default trust
policy. Private keys remain local under `core/` with `0600` permissions and
are ignored by git. Public identity material and policy are committed
together as one SSH-signed birth commit, giving the instance a verifiable
cryptographic birthday.

This is a foundation, not the full peer-trust system. Peer CA exchange,
delegation certificates, inherited-trust policy enforcement, and transport
mTLS still need dedicated runtime paths.

### Document Root Admission

**Status: Implemented**

A signed document root is not trusted merely because its commits carry
signatures. Before any of its history counts, the root must prove its
birth: it has exactly one parentless commit, that commit is signed by a
key declared in config as one of the root's `seed_signers`, and every
commit that has ever changed the in-tree `.allowed_signers` was signed by
such a key too.

The structural point is where the answer comes from. Verifying against a
root's own `.allowed_signers` lets the repository vouch for itself, since
whoever wrote that file also chose what it says. Seed signers live in
config, outside the repository they govern, so admission is the one
question a root cannot answer in its own favor. Nothing but the seed set
counts here: the in-tree file is excluded from admission entirely, or a
commit that added a key could be validated by the entry it introduced.

This makes hardening a config expression rather than a code path. A root
that omits the agent principal from its seed signers is one the agent may
not establish or amend; that is the intended shape for `core`, which
holds the config deciding what the instance trusts. Where the agent has
shell access it can still write that config, but it cannot sign the
change, so the boot gate refuses and names `admission`. Detection, not
prevention — which is the right property, because the realistic failure
is an agent steered by a poisoned document rather than a deliberate
adversary, and that drift is otherwise silent.

Admission runs for every git-backed root under `verify_signatures: warn`
or `required`, including roots Thane only reads and never writes to —
those carry entirely foreign history, so they are where it matters most.
See [Document Roots](document-roots.md) for the operator-facing detail.

### Trust Zones

**Status: Implemented**

Every contact has a trust zone: `admin`, `household`, `trusted`, `known`,
`unknown`. Trust zones are the universal router for cost, priority, and
permissions. The zone is stored in the contacts database and validated by
`ValidTrustZones` in Go — the model cannot invent new zones or escalate a
contact's trust level through conversation.

Trust zones determine:
- Model quality allocation (admin/household gets frontier, unknown gets local)
- Email send permission (admin/household: send freely, known: gate, unknown: block)
- Notification priority and rate limits
- Response depth and effort

### Owner Assertion

**Status: Implemented**

The `owner` tag is protected: it carries the operator's own tools, and the
model cannot activate it. It is pinned by the runtime for a conversation
whose channel binding says the caller is the operator — and that flag is
set in exactly one place, where a contact lookup resolves the caller to the
operator's contact record.

A surface does not confer it. Reaching a port is not an identity, so a
listener that cannot identify its callers produces a binding that names the
channel and claims nothing else. The Ollama-compatible surface is the case
that made this explicit: it set the owner flag for every conversation until
#1503, which meant Home Assistant, Open WebUI, and any host on the network
segment all spoke as the operator. It now runs at the trust its caller has
established, which for an unauthenticated caller is none.

The general form of this rule — one resolver that reads attestation state,
sender trust zone, and loop tier at the execution chokepoint — is #1268.

### Companion Credential Scope

**Status: Implemented**

A companion account token authenticates a device offering data, not an
operator driving the API. The native gate enforces that: a request whose
principal is `companion` may reach the companion surface — the realtime
WebSocket, its legacy aliases, and observation ingestion — and is refused
with 403 on every *gated* route outside it. Public routes are unchanged:
`/health`, `/v1/identity` and `/v1/auth/session` serve without a
credential for anyone, so presenting a companion token there is neither
better nor worse than presenting none. The allowlist is deny-by-default, so a route
added to the server is closed to companions until it is named on purpose,
and a test derives the companion surface from the route table at runtime
so the two cannot drift.

A companion credential also cannot become a console session. `/v1/auth/login`
refuses it, and the session store refuses it again beneath the handler, so a
future caller cannot reopen the exchange.

This matters because of where the credential lives. It sits in a phone's
Keychain and on a laptop, travels further than an operator token, and is
held by software the operator does not read. Before this, it authorized
every gated native route — contact deletion, checkpoint restore, session
reset — and could be traded for a browser session.

Note the precondition: the gate exists only when operator API tokens are
configured. With none configured it is nil and every route serves without a
credential, so this scope is a restriction on an authenticated deployment,
not a floor under an unauthenticated one.

### Orchestrator Tool Gating

**Status: Implemented**

The orchestrator model receives only its declared tools (currently ~14). It
literally cannot send email, control HA devices, or access forge write
operations directly. All capabilities beyond planning/coordination require
delegation or explicit capability tag activation.

This is structural: the tools are not present in the API call. The model
cannot choose to use a tool it doesn't have.

### Capability Tag System

**Status: Implemented**

Tools are grouped into semantic tags (`ha`, `email`, `forge`, etc.) that must
be activated before use. Tags marked `core` are available
unconditionally; others are loaded on demand. The tag registry is
config-driven and validated at startup.

### Egress Gate

**Status: Implemented for email; planned for Signal and other channels**

Every outbound email passes through one Go path, `Service.Send`, and ends
in exactly one disposition: sent, held in the account's drafts folder for
the operator to send, or refused with a decision record. The path checks
the account's access level, assesses every recipient against the contact
directory and the account's domain lists, routes on the most restrictive
recipient's trust zone under the account's delivery policy, applies an
unattended floor so a turn the operator is not present for drafts rather
than sends unless the operator chose direct delivery, adds the operator's
audit copy outside the gate to mail it will send (a draft carries none),
offers the composed message to a refuse-only
inspector, signs, and only then delivers. The model's tool call cannot
skip a stage, a refusal names each recipient at issue with its recovery,
and one log line per decision records which rule settled it. Rate
limiting and Message-ID dedup remain planned, as does extending the gate
to Signal.

An account whose delivery is `drafts` never hands mail to SMTP: the
operator reads and sends every draft from their own client, so their
send is a gate the path cannot skip. With `draft_gate: relaxed`, the
default there, the path relaxes the trust gate to match. A recipient
refused only for its trust zone (no contact record, a `known` contact,
or a shared address whose least privileged record is blocked) is
drafted with gating `draft_only` rather than refused. The relaxation
runs after the contact assessment and before the account's
recipient-domain rules, and that order is the point: the domain rules
skip recipients already refused, so relaxing after them would draft to
a stranger at a denied domain, while relaxing first leaves the domain
rules to refuse it again. Automated mailboxes, failed directory
lookups, unparseable addresses, and the recipient limit are never
relaxed: nobody reads a reply at an automated mailbox, and a failed
lookup is no judgement to relax. `draft_only` ranks above
`confirmation` and below `blocked`, so a mixed recipient list decides
as its most restrictive recipient, and routing drafts any `draft_only`
recipient on every delivery mode as a backstop. `relaxed` beside any
other delivery mode is refused at startup, and the check that enables
it reads the delivery mode again rather than trusting validation, so a
relaxed gate can never admit a recipient to mail Thane sends. Because
the operator's send is the gate, the draft names every `draft_only`
recipient by bare address: a display name is whatever the model or the
original message supplied, and on a recipient the directory does not
vouch for, `"Alice" <mallory@example.net>` would read as Alice to the
operator about to press send. Recipients the gate allowed keep their
names.

A draft goes only to the folder holding the drafts role: the account's
`drafts_folder`, else the folder the server marks `\Drafts`, from the
cached listing or one fresh one. The path never guesses a folder name.
When none resolves, it refuses the message with route
`no_drafts_folder` before composing it, on every delivery mode, so a
draft that has nowhere to go can never fall through to delivery.

### Router Quality Floors

**Status: Implemented**

Model selection uses quality floors, not model names. The model doesn't
choose its own quality level for metacognitive supervision.

### Email Polling State

**Status: Implemented**

IMAP high-water mark stored in opstate KV. The poller cannot re-process old
messages regardless of what the model requests. UID tracking is in Go, not
in prompt context.

### Email Sender Identity

**Status: Implemented (resolution); seams only (authentication)**

Every address the email tools render, and every sender the poller wakes a
handler for, is resolved against the contact directory in Go and tagged
with the result: `matched` with the contact and its trust zone,
`unmatched`, `ambiguous` when several records share the address (the least
privileged zone governs, and no candidate counts as the operator), or
`lookup_failed` when the store could not answer. The model never infers a
person from a display name. The same resolver serves Signal, so identity
is one answer across channels.

A From header is a claim, not a proof. A message from the operator's
address is tagged `is_owner: true` because the *record* is the operator's,
and nothing about that tag says the operator wrote it. The `authentication`
field on every read result is the only place a message can be called
verified, and today it always reads `absent`: no authenticator is
configured, so nothing was checked and no suspicion attaches. When S/MIME
and OpenPGP verification ship (#317), `verified` will be true only when
Thane itself validated a signature with a key the directory holds for the
matching contact. Domain-level results (DKIM, DMARC) and headers a mail
server wrote can never set it. The keys that would make such verification
possible are operator custody: `contact_save` refuses and `contact_import_vcf`
drops `KEY` and `X-THANE-KEY-*` properties, so a message cannot install the key that
verifies its own sender. The addresses and numbers the resolver matches on
are custody for the same reason; see
[Contact Identity Custody](#contact-identity-custody). The attended/unattended distinction the egress
gate reads comes from the same place: a turn counts as attended only when
it is the operator's own message, sent through Thane's native API or
written in a conversation bound to their own contact, never because a
message claimed to be from the operator. Loop wakes are never attended,
even in a loop the operator launched from their own conversation, and
neither is a call through the Ollama-compatible shim that Home Assistant
automations and voice satellites use.

One mailbox shape is capped in Go whatever the directory says. An address
whose local part names a mailbox nobody reads (no-reply, do-not-reply,
notification, bounce, mailer-daemon) is marked `automated`, and its
effective zone is capped at `known`: an `admin`, `household`, or `trusted`
record holding it is read as `known`, and `known` and `unknown` stay as
they are. Only the address's local part decides it; the display name and
the domain are never read, and postmaster is not automated. The cap sits
at the one point where email identity is normalised, so wake metadata,
list, search, and read results, and the send gate all see the same
answer, and it only ever lowers a zone. The matched record stays bound, so
the sender is still recognised and its inbound interactions are still
recorded. Outbound, the gate refuses an automated recipient whatever its
record's zone, with a reason that says to drop it. The mark is derived
from the address on every read and never stored, so no tool can set or
clear it. When email polling is on, each such address held by an active
record above `known` logs a Warn at startup and keeps the
`contact_directory` row of `system_health` degraded until the operator
demotes the record or moves the address to its own `known` record; see
[Configuration](../operating/configuration.md#contacts--carddav). The same
row also carries the fork audit, which runs whether or not mail is polled;
see [Contact Identity Custody](#contact-identity-custody).

A message can also mark itself, and that mark is kept apart from the
zone. When a message is read in full, by `email_read` or by the read
`email_reply` makes of the message it answers, Go reads the message's
own top-level headers: an `Auto-Submitted` value other than `no`
becomes `auto_submitted` (`auto-replied`, `auto-generated`,
`auto-notified`, or `other`, so no sender text is echoed), and a
`List-Id` (RFC 2919), any RFC 2369 `List-*` field, or a `Precedence`
of `bulk`, `list`, or `junk` sets `bulk`. RFC 3834 §2 tells an
automatic responder not to answer `Auto-Submitted` mail and lets it
decline list traffic; `Precedence` values `bulk` and `junk` are
convention (RFC 2076). The marks are per message, where `automated` is
per address, and they never change a zone: they are unauthenticated
claims that any sender can set or omit at will, so they carry no
credibility in either direction. Their one effect is outbound. A reply
to a marked message, written in a turn the operator is not present
for, would be an automatic response, so the send decision refuses it
with route `automatic_response` in every delivery mode, a requested
draft included, before any recipient is assessed; nothing is sent or
drafted. The one exception only drafts. On an account with a relaxed
draft gate, a reply to list mail, marked by a `List-Id` or a `Precedence` of
`bulk` or `list` and not `auto_submitted`, whose own To or Cc holds the account's address, is drafted with route
`personally_addressed_list_reply`: a person on a list answers mail
addressed to them, the operator sends the draft by hand, and its
recipients still pass the gate. Mail that reached the account only
through a list address stays refused, and so does anything
`auto_submitted` or marked `Precedence: junk`, which classic
autoresponders set without `Auto-Submitted`, because answering an
automatic reply, a bounce, or a notification is pointless, and so does
mail whose only list marks are `List-*` fields such as
`List-Unsubscribe`, which a sender adds to its own mailings. The operator's own turn replies as usual. Every reply to
marked mail that gets past the account's access check records the
marks as `original` in its decision, and the decision log line carries
them as `original_auto_submitted` and `original_bulk`; a reply from an
account that cannot write mail is refused with route `access` before
the original is fetched, so it records none. The poller, `email_list`,
and `email_search` do not read these headers, so their IMAP fetch is
unchanged and a wake event carries no marks; a handler learns of them
from `email_read` or from the refusal.

### Contact Identity Custody

**Status: Implemented**

The resolvers above trust the record an address belongs to. An email
address or Signal number lends its contact's trust zone to whoever writes
from it, and on the operator's own contact it lends the operator's
authority, because a channel conversation bound to that contact is
attended. Which contact holds an address is therefore authority, not
contact data, and Go keeps it with the operator the way it keeps trust
zones and keys. The facts that choose where a contact's notifications go,
and the names the resolvers find a contact by, are authority for the same
reason: whoever controls them receives that person's notifications and
answers their decision requests. The model-facing contact tools (`contact_save`,
`contact_import_vcf`, `contact_forget`, and `contact_dossier_write` for the
second-dossier rule) enforce the rules below. CardDAV, `/v1/contacts`, and
`thane init` write the store directly and keep full operator power. Name
resolution and the fork audit read every record however it was written,
so a duplicate the operator's own path created still resolves toward the
contact with authority and is still reported.

- **Custodied targets.** No model writer adds an `EMAIL`, `TEL`, or `IMPP`
  value to an existing contact above `known` (a malformed zone counts as
  above), or to the operator's own contact at any zone: the one
  `identity.operator_contact_id` names, or the one the legacy owner name
  resolves to. The operator's own message lifts this rule for
  `contact_save` only. Attendance is `tools.OperatorAttended`, the same
  predicate the egress gate reads, so the two cannot disagree about who is
  present. `contact_import_vcf` never lifts it, because a vCard is content.
- **Legacy operator name.** Under `identity.owner_contact_name` the
  operator is whichever contact the name resolves to, so the app resolves
  it once at startup through the channel resolver's cache and pins that
  contact for custody and `contact_owner`; custody, `contact_owner` and
  `IsOwner` then agree for the life of the process. In every turn,
  `contact_save` refuses to create a contact, or set a nickname, that
  carries the owner name on any contact but the operator's, and
  `contact_import_vcf` leaves such a card or nickname out, because the
  next resolution could pick the model's contact as the operator. For the
  same reason `contact_save` refuses, in every turn, to replace the
  nickname through which the operator's own contact answers to the owner
  name; a case change is allowed, and so is any nickname when the
  formatted name is itself the owner name.
- **Authority holders.** In every turn, no model writer adds a value that
  another active contact already holds when that contact is above `known`
  or is the operator's. Equivalence mirrors the resolver: email compares
  case-insensitively, and a phone number is checked as `TEL` and as `IMPP`
  `signal:`, with and without a leading `+`. A duplicate held only by
  `known` contacts stays allowed, because it moves recognition between low
  zones and never authority.
- **Notification routing.** `notification_preference` picks the provider
  that `send_notification`, `request_human_decision`, and
  `request_human_escalation` deliver through, and `ha_companion_app` is
  the Home Assistant notify service, used verbatim: the device that
  receives HA push, `ha_notify` included, and whose action taps answer
  decision requests. Both facts, in any letter case, follow the
  custodied-target rule and its lift. There is no holder rule, because a
  shared household device or a common channel moves no one's authority,
  and a value the contact already carries under any case of the key is a
  no-op. The router and the HA sender read each fact through
  `contacts.FactValues`: the exact lowercase key first, then other
  spellings in sorted order, and only the first value counts; a
  hyphenated or otherwise renamed key does not route. CardDAV and
  `/v1/contacts` upper-case property names, so their rows route too. No
  model-facing tool removes a routing value, so a save that records one
  behind an existing value says in its result which value delivery still
  uses.
- **Names.** `ResolveContact` finds, in one query, the active contacts
  whose formatted name or nickname is the name, both sides trimmed of
  edge space and compared with `LOWER` as the fork audit folds them,
  and takes the first in this order: the pinned operator's own record at
  any zone, then records above `known` (a malformed zone counts), then a
  formatted-name match before a nickname match, then ID. Only when no
  contact holds the name that way does it read the fork audit's short
  forms, through the same `recordNameKeys` keys, reading the short
  forms alone: a contact's given name and the first word of a formatted
  name of more than one word, trimmed and folded the same way. Exactly
  one active contact answering by a short form is the answer; two or
  more are an `AmbiguousNameError` whatever their zones, naming up to
  five with formatted name, zone, `contact_id` and the field matched,
  counting the rest and pointing at `contact_lookup`'s `query` to find
  them, because authority breaks ties among exact holders only.
  Notes, AI summaries, and organizations are never read: a word in one
  contact's note does not make that contact the person it names, and
  `Store.Search`, behind `contact_lookup`'s `query` and the contacts API,
  is the only path that reads them. Notifications, decision requests,
  `contact_lookup`, `contact_whereabouts`, vCard export, and
  `contact_forget` by name use it, and so does channel context for a
  sender no channel bound to a contact. A `known` record whose formatted
  name is the nickname of a contact with authority therefore never
  shadows that contact. An exact holder always beats a short form, so
  "Bob" reaches a record whose whole formatted name is Bob, not a
  household Bob Smith without that nickname; the fork audit below
  reports that shape. Forgetting such a record, the only exact holder
  of the name, leaves the name to the short-form step. A nickname change on a custodied target follows the
  target rule and its lift; "changed" folds ASCII letters only, as SQLite
  `LOWER` does, after trimming, so a case-only edit is not a change. In
  every turn, no model writer gives a new contact a formatted name, or any
  contact a nickname, that another active contact above `known` or the
  operator's own already uses as its formatted name or nickname, compared
  with `LOWER` as the resolver compares them. The check runs in the
  write's own transaction, so an operator promotion cannot land between
  the check and the write. `contact_import_vcf` skips such a card, leaves
  such a nickname off a merge, and never fills a nickname into a
  custodied target; an import card that carries only names and cannot
  resolve the operator counts every holder as one with authority.
  `FindByNickname` orders a shared nickname the same way, without the
  match-kind step. When `operator_contact_id` or the legacy owner name is
  configured, the contact store learns the operator from the same pinned
  record custody and `IsOwner` use, so notifications, lookups and context
  all resolve a shared name alike. Under the sole-admin fallback nothing
  is pinned, so a shared name orders by zone, then match kind, then ID.
  The legacy owner name is itself resolved at startup, before any pin
  exists, so it orders by zone and match kind alone: a `known` record
  cannot take it from a record above `known` that goes by it as a
  nickname. A
  short form is not protected the way a formatted name or nickname is:
  any contact given the same first name makes it ambiguous, so the handle
  the operator is notified by belongs in their formatted name or
  nickname.
- **Kind.** `kind` stays model-writable, so nothing may gate on it: no
  custody rule, notification route, or `IsOwner` decision reads it, and a
  test pins that changing it on the operator's contact moves neither.
- **Context by bound ID.** Channel context for a conversation bound to a
  contact resolves that contact by its ID, and looks the sender's name up
  only when nothing was bound. A session origin whose contact ID no longer
  resolves gets no contact context rather than a name match, so a name
  another contact now answers to cannot redirect a bound conversation's
  context.
- **Removal.** `contact_forget` refuses contacts above `known`, the
  operator's own contact, and contacts bound to a Home Assistant person, in
  every turn. It takes exactly one of a name, resolved once as above, or a
  canonical `contact_id` naming one active record, which is how a `known`
  duplicate whose name resolves to a contact with authority is removed;
  custody judges both alike. It deletes by ID only while the contact still
  qualifies, and names what it removed. A refusal by name also names up to
  three `known` records that share a name key with the refused contact,
  that the passed name also fits as a formatted name, nickname, given
  name, or first word, and that are neither the operator's nor bound to a
  Home Assistant person, with their UUIDs, so the next call can pass
  `contact_id`.
- **Fork audit.** `Store.ContactForks` reads every active record's names,
  email addresses, and phone numbers, writes nothing, and reports what can
  misroute. Every group it reports includes a record above `known` (a
  malformed zone counts) or the pinned operator's, so ordinary `known`
  contacts never raise it. Formatted names and nicknames are name keys in
  one key space, trimmed and folded as `LOWER` folds them; a record's short
  forms, its given name and the first word of a formatted name of more
  than one word, join a key only when that key is another record's whole
  formatted name. A shared name key alone does not make two records one
  person, since address books save many people by one first name, so a
  pair that shares one is judged by evidence: the two share an address
  or number; the one that is not the operator's and carries no more
  authority than the other holds no real address or number of its own (a
  placeholder does not count); or one is a `known` record, not the
  operator's, bound to a Home Assistant person. Records with authority
  that share an address or number are one person. Any other record that
  such evidence ties to a person is a copy of it, whether a `known` record
  other than the operator's or one above `known` with no real address or
  number of its own; a copy joins every person it is tied to but never
  ties two people into one, so a thin copy that could be either of two
  people is listed with each. Each person with two or more records is a
  `name` finding listing exactly those records. Records that answer to the key
  as a formatted name or nickname, one with authority, that no person
  holds together are different people: the key's one `shared_name`
  finding names one record per person, since a lookup reaches only one of
  them. Short forms alone between different people are not reported:
  resolution takes a short form only when exactly one record answers to
  it, so a first name two people share resolves to neither rather than
  to the wrong one. An email address held by
  several records, compared case-insensitively, is an `email` finding when
  a holder is a `known` record other than the operator's, so the send
  gate reads the address at `known` for every holder, or when a holder
  other than the operator's holds no other real address or number. A
  mailbox that records with authority share while each holds its own
  addresses is already read as ambiguous, so it is not reported. A phone
  number is a `phone` finding when several records hold it as `IMPP`
  `signal:` or as a `TEL` whose `TYPE` names no type but `pref`, or names
  `cell`, `mobile`, or `iphone`, compared with and without a leading `+`
  as the channel resolvers match it; once any holder has the number as
  `signal:`, every holder counts. A line typed `home`, `work`, `main`,
  `voice`, or the like is shared by design and not reported. Separately,
  an `EMAIL` on a record with authority whose domain RFC 2606 or RFC 6761
  reserves (`example.com`, `example.net`, `example.org`, or a name under
  one of them, and the `example`, `test`, `invalid`, and `localhost`
  top-level domains) is a `reserved_domain` finding: no mailbox exists
  there, so the address is a placeholder that still carries the record's
  zone. Each finding has a kind and a key, and names up to eight records
  with zone, ID, the operator flag, and any Home Assistant person binding,
  counting them all. At startup Thane logs one Warn per finding, at most
  20 followed by one summary Warn with the total, and the
  `contact_directory` row of `system_health` stays degraded, naming up to
  five findings with three records each, ahead of the automated-address
  findings that share those five, until the directory is fixed; the
  row re-reads the directory on every render. Unlike the automated-address
  findings, which follow email polling, these run whenever the contact
  store exists. The fixes are the operator's, through CardDAV or
  `/v1/contacts`: merge a duplicate into the record that should keep the
  name or address, rename one of two people who answer to one name, move
  a shared address to the record that owns it, and replace or remove a
  placeholder. Thane can itself forget a `known` duplicate that is neither
  the operator's nor bound to a Home Assistant person, by `contact_id`,
  and copy a duplicate's addresses onto a record above `known` only in the
  operator's own message. No model-facing tool renames a record, removes
  or replaces an address, or moves a person binding, which only the
  operator's config sets.
- **Second dossiers.** `contact_dossier_write` refuses the first write of a
  contact's dossier, when `contacts:<uuid>.md` does not exist yet, if an
  active record that shares a name key with the contact and shows the
  audit's evidence of being the same person already has a dossier. Name
  siblings with no such evidence, such as two people nicknamed "Mom" who
  each hold their own number, each keep their own dossier. It checks the
  dossiers of at most 16 such records, the operator's and those above
  `known` first. The refusal names both records with zone and UUID, the
  existing dossier's ref, and the evidence. Its advice follows authority.
  When the holder carries as much authority as the contact being written,
  it says to write into the existing dossier. When the contact being
  written carries more and the holder is a `known`, unbound duplicate, it
  says to read the duplicate's dossier, forget the duplicate by
  `contact_id`, then write the first dossier. When only the operator can
  remove the holder, it says to report it. In every case it says to write
  nothing and report both records if they are different people. Nothing
  is written. Replacing an existing dossier is never refused. The
  presence check reads through the document store rather than
  the document tools, so it records no read receipt; a failed check refuses
  the write. The check is not atomic with the write, so two concurrent
  first writes on name siblings can both land.
- **Fact keys.** A `contact_save` fact key must match
  `^[A-Za-z][A-Za-z0-9_-]{0,63}$`. `KEY` and the whole `X-THANE-` namespace
  are refused, and so is a value containing a control character. A key
  carrying `.`, `;`, `:` or a line break would be emitted verbatim as a
  vCard property name and decode as a different property, such as a live
  `EMAIL` or a trust zone, on the operator's next CardDAV PUT. A key naming
  a field the codec owns (`NOTE`, `TITLE`, `FN`, `BDAY` and the rest) is
  refused too, because `ContactToCard` withholds such a row and the next
  PUT deletes it. `contact_import_vcf` drops decoded properties whose names
  fail the same grammar; go-vcard strips a single group, so `item1.EMAIL`
  imports as `EMAIL` under the address rules.
- **Values.** The go-vcard encoder escapes LF but writes CR raw, and a
  client that reads a bare CR as a line ending would start a new property
  mid-value. `contact_save` refuses a CR or other control character in
  every argument (`note` and `ai_summary` keep plain line breaks), and
  `contact_import_vcf` drops values that carry one.

A `contact_save` refusal saves nothing and emits no contact mutation. It
logs one warning, `contact identity custody refused`, with the contact ID,
zone, rule (`zone`, `operator`, or `holder`), properties (`EMAIL`, `TEL`,
`IMPP`, a routing key, or `FN` or `NICKNAME` for a name), holder ID, and
the turn's request, conversation, and loop IDs. Import drops the refused
values, keeps the rest of the card, skips a card that would create a
contact under a name or nickname a contact with authority goes by, leaves
such a nickname off a merge, logs the same warning (marked `dry_run` for a
preview), and counts the drops in its result, naming each skipped card;
its rows carry the turn's provenance under the source
`contact_import_vcf`. A forget refusal logs the same warning with the
contact ID, zone, rule, and the turn's request, conversation, and loop
IDs. The save also re-reads the contact's trust zone, nickname, and
deleted state inside its transaction and aborts if any changed since the
tool read the record, so a model save can no longer revert an operator's
concurrent zone or nickname change or resurrect a deleted contact. Import
writes each card in one transaction that repeats the same re-read and the
holder and target checks, so an operator write that lands between the
import's check and its write cannot give a value a second holder: a newly
refused value is dropped and counted, and a card whose merge target
changed zone or nickname or was deleted, or whose name or nickname a
contact with authority took meanwhile, writes nothing and is counted as
skipped.

`ContactToCard` withholds any stored property whose name carries vCard
syntax, a control character or U+2028 included, or is a field the codec
owns (`FN`, `PHOTO`, `X-THANE-TRUST-ZONE`, `X-THANE-HA-PERSON`, and the
rest). Such a row can predate these rules, be a `PHOTO` row stored through
`/v1/contacts`, or arrive in the operator's own CardDAV PUT, because the
decoder keeps a nested group as `B.EMAIL` and a vertical tab inside a name.
CardDAV logs `contact properties withheld from CardDAV` with the contact ID
and property names on every read of such a card, and the operator's next
PUT of that card removes the rows. `ContactToCard` also rewrites every CR,
and every other line break the encoder would emit raw, to an escaped LF, so
a value stored before these rules cannot split a line either.

The rules are forward-only. Addresses added to elevated contacts before
they shipped keep matching, so reviewing them is the operator's job,
through CardDAV or `/v1/contacts`. The same holds for routing facts and
for a name or nickname two contacts already share, though name resolution
now prefers the one with authority and the fork audit reports the shared
names, addresses, and numbers involving one that can misroute. One change
reaches
existing rows at upgrade: delivery now reads every letter case of a
routing key, so an upper-case routing row, as CardDAV and `/v1/contacts`
write it, starts routing wherever no lowercase row comes first.

## Known Behavioral Gaps

These are areas where safety currently depends on prompt compliance. Each is
a candidate for structural enforcement.

### Delegation Guidance

**Risk: Medium-High**

Delegates receive task descriptions as natural language. The delegate model
chooses which tools to call and how. A delegate currently receives all tools
available to its profile.

**Structural fix:** Delegations should declare required capability tags.
Delegates receive only tools for those tags, not the full profile toolset.

### Inbound Email Credibility

**Risk: Medium**

A forged From of a person's address still carries that person's trust zone
into wake metadata and email tool results, because nothing verifies a
sender until S/MIME and OpenPGP verification ship (#317). The automated
cap covers only mailbox names that say nobody reads them. Until then the
handler Task and the email talents carry the rule that nothing
consequential a message asks for is done on its word alone. The Go floor
bounds what a forged From can win: a wake is never an attended turn, so
under the default delivery policy anything the handler writes is held in
the drafts folder for the operator (only an account configured with `delivery:
direct` sends from a wake), and the handler loop wears only the `email`
tag. A message's own `auto_submitted` and `bulk` marks move no zone at
all, because they are unauthenticated and any sender can set or omit
them; they only stop an unattended reply. `email_send` does not consult
them, so a wake refused on such a reply can still write fresh mail to
the same address: the handler Task and the refusal forbid that, and
under the default delivery policy it would be held in the drafts folder
like any other wake mail. On a drafts-only account with a relaxed draft
gate, a forged or list message can draw a draft addressed to a
stranger, including a list address or a Reply-To the sender chose.
Nothing leaves the mailbox until the operator reads that draft and
sends it: their review is that account's gate, and the email talents
tell the model to report who a draft is addressed to and why.

**Structural fix:** Signature verification against keys the directory
holds (#317), so a claimed sender is established rather than read from
the From header.

### Carry-Forward Content

**Risk: Medium**

Session close produces a carry-forward note written by the model. This is
the primary context that survives session rotation. A model pursuing a hidden
goal could selectively drop or inject context.

**Structural fix:** Harder — carry-forward is inherently a compression task
requiring judgment. Possible mitigations: structural extraction of recent
tool calls and their results, hash-based integrity checking, periodic human
review.

### Metacognitive State Writes

**Risk: Low-Medium**

The metacognitive state file is markdown written by the model. A model could
gradually shift its own operational parameters through state file
manipulation.

**Structural fix:** Separate machine-written operational state from
model-written reflective state. Clamp operational values in Go regardless of
what the file says.

## Design Guidelines

When adding new features, ask:

1. **What happens if the model ignores the instruction?** If the answer is
   "it could cause harm," the control must be structural.
2. **Where is the enforcement point?** If it's in a prompt, plan the Go
   enforcement.
3. **Can the model escalate its own permissions?** If yes, that's a
   structural gap.
4. **What's the blast radius?** Outbound actions (email, messages, web
   requests) need gates. Internal actions (file writes, state updates) need
   bounds.
