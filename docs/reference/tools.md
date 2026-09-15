# Tools Reference

Thane's native tools are organized by tag. A tool is available
to a given turn only when one of its default tags is active — see
[The Agent Loop](../understanding/agent-loop.md) for how tags flip on
and off, and [Prompt Caching](../prompt-caching.md) for how tag choices
interact with prompt caching.

Tools under the **Core tools** heading below are not tag-gated and
load on every turn. Everything else loads only when a relevant tag is
activated, either by the model via `tag_activate` or via configured
core tags.

The authoritative source for which tool is tagged how is
[`internal/model/toolcatalog/catalog.go`](../../internal/model/toolcatalog/catalog.go);
this doc is a human-readable reflection of that catalog. If you add or
re-tag a tool there, update this file in the same PR.

## Coarse menu tags

`development`, `home`, `interactive`, `knowledge`, `media`,
`operations`, `people` are coarse navigation tags rather than tool
containers. They carry short teasers that point the model at the
fine-grained tags that actually own the tools. Activating any coarse
tag surfaces its teaser; the model then activates the fine tag it
needs.

### Trailheads

A **trailhead** is a talent or KB document that marks the root of a
decision tree for a capability tag — the first navigation or triage
document a model meets when the tag activates. Trailheads are
declared with `kind: trailhead` in frontmatter (the legacy
`kind: entry_point` value still loads with a deprecation warning for
one migration cycle). A trailhead exists, names itself honestly, stays
small, takes a clear opinion about where to look next, and locates
itself in the surface a reader is currently on — it does not try to
encyclopedia the whole domain. By convention, trailhead filenames use
the `-trailhead.md` suffix and the heading is `# <Domain> Trailhead`.
When both a trailhead and ordinary doctrine articles share an active
tag, trailheads sort first so navigation scaffolding precedes deeper
guidance.

## Core tools

These tools load on every turn regardless of active tags.

| Tool | Description |
|------|-------------|
| `tag_activate` | Activate a tag for the current conversation. |
| `tag_deactivate` | Deactivate a tag for the current conversation. |
| `tag_reset` | Return the current conversation to its baseline tag state. |
| `tag_inspect` | Inspect one tag's active and excluded tool surface. |
| `lens_activate` | Activate a persistent global behavioral lens. |
| `lens_deactivate` | Deactivate a global behavioral lens. |
| `lens_list` | List currently active behavioral lenses. |
| `thane_now` | Synchronously delegate a bounded task and return the result inline. |
| `thane_assign` | Assign a task to a sub-agent that runs in the background and reports back when complete. |
| `request_core_attention` | From a loop, force a supervisor/core attention turn for a decision-worthy concern. |
| `logs_query` | Query the structured log index with attribute filters. |

## `archive` — conversation archive retrieval

| Tool | Description |
|------|-------------|
| `archive_search` | Full-text search across conversation archives. |
| `archive_sessions` | Browse session archive metadata. |
| `archive_session_transcript` | Retrieve a full session transcript. |
| `archive_range` | Retrieve archived messages by time range or message-count floor. |

## `session` — conversation lifecycle

| Tool | Description |
|------|-------------|
| `conversation_reset` | Reset the current conversation's message history. |
| `conversation_model_pin` | Hold the current conversation to one model deployment, or clear the hold. Outranks channel and client model selection; memory-only, cleared by restart. |
| `session_checkpoint` | Bookmark the current messages and active context without closing. |
| `session_close` | Close the current session with carry-forward context. |
| `session_split` | Archive early messages and retain recent context in a successor session. |

## `awareness` — live-context entity management

| Tool | Description |
|------|-------------|
| `add_entity_subscription` | Subscribe to an HA entity. Ownership is a parameter: no `owner` (or `core`) = always-visible, owned by the root container; `owner: <loop name>` lands on that loop's spec. |
| `list_entity_subscriptions` | List the whole subscription registry: core-owned (always-visible), loop-owned, and system-seeded rows, each with its owner. |
| `remove_entity_subscription` | Remove a subscription; `owner` addresses a loop's own entry, system rows are config-owned and refuse removal. |

Subscription expiry is reported as `expires_delta`, not a raw timestamp,
so the model does not need to do clock arithmetic.

For `weather.*` subscriptions, `add_entity_subscription` accepts
`forecast: daily`, `forecast: hourly`, or `forecast: twice_daily` to
fetch Home Assistant forecast response data each turn and include a
compact forecast in the injected entity context. Use `forecast: none` to
clear forecast fetching for that subscription.

An entity subscription's `entity_id` may be a glob (e.g.
`binary_sensor.*door*`, `*_temperature`) instead of a concrete id. A glob
subscription is re-expanded against live entities every turn — newly
matching entities join automatically — and is capped per turn, emitting a
truncation marker when it matches more than the cap. This works across
`add_entity_subscription`, `watch_entity`, and
`thane_loop_create.entities`.

A subscription may declare a transition log: `transitions: n` renders
the entity's last n observed state changes inside its block
(`{from, to, ago}`, class-aware, delta timestamps), and
`transitions_window_seconds` bounds the log to a trailing window
(usable together or alone; capped per subscription with truncation
advertised). Declaring a log derives capture — the entity joins the
state-change ingestion filter automatically, with per-entity bounded
retention behind the shared window — so `mode: ingest` is never needed
for it. Entity ids and globs only; complementary to `history` (numeric
trend summaries) rather than a replacement.

The always-visible tier belongs to `core`: the root container's
subscriptions render in every context (conversations are de facto
core's context), stored as core-owned registry rows — core has no
persisted spec by design, so the rows themselves are the source of
truth and the orphan sweep treats `core` (like `system`) as reserved.

A loop-owned subscription may declare `wake: true`: the owning loop is
awakened when the entity changes, with the event delivered as
`{entity, from, to, ago}` in the class-aware vocabulary through the
shared loopqueue chassis — debounced (`wake_debounce_seconds`, default
a few seconds), coalesced per entity (latest change wins while a wake
is pending), and crash-durable. Capture derives automatically, wake
subscriptions count against the ingest entry cap, and the upstream
rate limiter still applies — a chattering sensor cannot wakestorm a
loop. Simple native change triggers only: HA-side derivation
(compound conditions, zone dwell, templates) remains the
automation→MQTT→wake pipeline, draining the same queue.

A subscription may carry `requires_tag`, a capability tag gating its
visibility: it renders only while that tag is active in the consuming
context. This is the macro-set lens — one tag activation surfaces a
subject's tagged documents and its related entities together. The gate
is render-only (rejected with `mode: ingest`/`both`, and the ingestion
filter ignores gated rows), optional, and deliberately not the default
path: ungated subscriptions remain the norm.

Entity subscriptions also accept `include`, a set of HA metadata flags:
`area`, `device`, `labels`, `description`, and `visibility`, or
`all: true`. Enabled metadata is resolved through the native HA registries and
injected beside the entity state. Area metadata includes floor registry
details; deployments can alias floors as buildings with
`homeassistant.floor_alias: building`. Visibility metadata exposes the HA
hidden/enabled status plus a model-facing `context_role`, so
hidden-but-enabled entities remain available for focused research without
becoming default context clutter.

## `ha` — Home Assistant state and control

| Tool | Description |
|------|-------------|
| `ha_control_device` | Natural-language device control with fuzzy entity matching. |
| `ha_find_entity` | Smart entity discovery across HA domains, optionally enriched with HA metadata. |
| `ha_get_state` | Current state of any entity, with optional area/device/label/description/visibility metadata. |
| `ha_list_entities` | Browse entities by domain and/or entity_id glob (e.g. `binary_sensor.*door*`), with optional HA metadata. |
| `ha_search_states` | Predicate search across live entity state (state value, numeric attribute, domain, area). |
| `get_area_activity` | Whole-area snapshot: floor context + entities grouped by salience + transition timeline + filtered counts, with optional per-entity metadata. |
| `ha_device` | Whole-device snapshot: the full device-info card (manufacturer/model/firmware/serial/MAC connections/area/labels/integration) + every child entity grouped the way HA's device page groups them (controls/sensors/configuration/diagnostic), with hidden entities shown marked, per-group truncation counts, and an availability rollup; resolved by id or name, with optional per-entity metadata. |
| `ha_history` | Recorder trend for one entity over a lookback window: numeric min/max/start/end/delta/trend or a discrete change summary, optionally trending a numeric attribute instead of the state. |
| `ha_home_snapshot` | Curated whole-home overview: anomalies, security/openings, presence, climate (energy optional), salience-first with an at-a-glance summary and a quiet status, plus optional per-entity metadata. |
| `ha_call_service` | Direct HA service invocation. |
| `ha_list_services` | List available HA services with per-field detail; feeds `ha_automation_create` action authoring. |
| `ha_registry_search` | Search the entity/device/area registry. |
| `ha_automation_list` | List automations with recent activation counts. |
| `ha_automation_get` | Retrieve one automation's configuration. |
| `ha_automation_create` | Create a new automation. |
| `ha_automation_update` | Modify an existing automation. |
| `ha_automation_delete` | Delete an automation. |
| `ha_automation_traces` | Fetch execution traces for an automation — step-by-step debugging of why it did or didn't fire. |
| `ha_automation_vocabulary` | Enumerate the 2026.7 trigger/condition identifiers usable in `ha_automation_create` config blocks. |

## `notifications` — delivery, escalation, and actionable responses

| Tool | Description |
|------|-------------|
| `ha_notify` | HA companion-app push notification. |
| `send_notification` | Provider-agnostic fire-and-forget notification. |
| `request_human_decision` | Actionable notification with a human-in-the-loop callback. |
| `request_human_escalation` | Escalate to a human with a synchronous response wait. |
| `request_ai_escalation` | Escalate to another agent/model with a synchronous response wait. |
| `resolve_actionable` | Mark an actionable notification as resolved. |

## `memory` — persistent facts and working memory

| Tool | Description |
|------|-------------|
| `remember_fact` | Store knowledge with optional embeddings. |
| `recall_fact` | Retrieve knowledge by category or semantic search. |
| `forget_fact` | Remove a stored fact. |
| `session_working_memory` | Read/write scratchpad for the active session. |

## `documents` — indexed document-root browsing

Managed document roots (`core:`, `kb:`, `generated:`, `scratchpad:`,
custom prefixes) are browsed through these tools rather than via raw
filesystem access. See
[Document Roots](../understanding/document-roots.md).
For source-code checkouts, configure a read-only root with
`indexing: false` and use the `file_*` tools against that prefix; the
document index is markdown-oriented.

`doc_roots` includes each root's policy summary: indexing status,
authoring mode, git/signature expectations, and current verification
health. Mutation tools obey that policy before writing; for example,
`read_only` roots reject managed writes and git-backed roots route
writes through signed commits. Read/search surfaces also respect
`verify_signatures: required` by blocking content that is not cleanly
covered by trusted signed git history.

Document tool results use model-facing time deltas such as
`modified_delta`, `created_delta`, and `checked_delta` instead of raw
absolute timestamps. Timestamp filter inputs still accept RFC3339 values
or signed deltas like `-604800s`.

On writable git-backed roots, Thane retains a hidden read receipt for each
loop/conversation and document ref. `doc_write`, `doc_body_write`, `doc_edit`,
and `doc_journal_update` use that receipt automatically;
revision hashes are not part of their model-facing contract. If the document
changed after the read, the mutation returns `applied: false` without replacing
newer content and includes a bounded `changed_since_read` patch. The receipt
then advances to the current document so a reconciled retry has the right
comparison base. When a patch is unavailable or truncated, the result includes
a bounded current excerpt instead. Replacing an existing whole body requires
reading it first, and the refusal names the document to read; structured edits
can safely derive a fresh base as part of the operation. `doc_delete` and
`doc_move` advance the caller's own receipt to absent, so recreating a document
at a ref the same caller just removed is an ordinary create.

A loop's generated `replace_output_*` and `publish_output_*` tools use the same
receipts, scoped to the wake. The Declared Durable Outputs context that opens a
wake records a receipt for every output it renders whole, so an output that fit
the context can be replaced directly; one marked `truncated` must be read whole
with `doc_read` first, and an own-output read that itself truncates records no
receipt. These tools report a refusal as a tool error rather than an
`applied: false` result: for the document's owner, a result that reads as
success while nothing was committed is indistinguishable from a healthy
publish. The error text carries the same message and reconciliation payload,
and a refused publish never writes the accompanying working notes. A
validation refusal lists every failing projection at once — an over-budget one
with its overage and whether rewording closes it or whole items must go — so
one corrected call recovers. The two receipt refusals recover differently: a
missing read is answered by reading and calling again, while a stale receipt
has already advanced to the current document and the error carries the
intervening change, so the caller reconciles before calling again rather than
replaying the same content over it. A wake whose every call to an output tool
is refused still ends as a completed turn; the loop records the write as
unpublished, and `loop_status` and `system_health` report it until a later
wake lands it.

| Tool | Description |
|------|-------------|
| `doc_roots` | List configured document roots with policy, health, and counts. |
| `doc_browse` | Walk a document root by folder; document rows include their exact `write_tool`. |
| `doc_outline` | Emit the heading/section outline for a document. |
| `doc_read` | Read a document by prefixed path, including faceting, available levels, and exact `write_tool`; on writable git-backed roots, also retain a hidden comparison base for this loop/conversation. |
| `doc_section` | Retrieve a named section from a document. |
| `doc_search` | Full-text and tagged search across roots; hits advertise facets and exact `write_tool`. |
| `doc_links` | List inbound/outbound links for a document. |
| `doc_values` | List frontmatter values (tags, statuses, etc.) across a root. |
| `doc_create` | Create a normal faceted document safely: corpus collision check + normalized placement + logical write in one call. New `dossiers:` documents must be direct children. |
| `doc_intake` | Analyze proposed knowledge against the existing corpus before writing it (the deliberate two-step form of `doc_create`); the `dossiers:` root rejects nested placement. |
| `doc_commit` | Commit an approved `doc_intake`/declined `doc_create` result through the normal logical document mutation. |
| `doc_write` | Create, self-migrate, or update a normal managed document from status-line, optional teaser/digest, and full projections; Go validates and renders its private storage codec atomically. New `dossiers:` targets must be direct children. |
| `doc_body_write` | Write one undifferentiated Markdown body for the unusual document that intentionally has no projection ladder. It cannot create a nested `dossiers:` target. |
| `doc_edit` | Targeted edit within an ordinary document, with automatic stale-write protection; faceted and contract-owned documents use their returned `write_tool`. |
| `doc_copy` | Copy a document to another location. |
| `doc_move` | Move or rename a document. |
| `doc_delete` | Delete a document. |
| `doc_copy_section` | Copy one named section into an ordinary destination; faceted destinations reject partial mutation. |
| `doc_move_section` | Move one named section between ordinary documents; faceted sources or destinations reject partial mutation. |
| `doc_journal_update` | Append or update an ordinary journal-style entry, with automatic stale-write protection. |

`doc_write` is the normal logical writer for documents without a narrower
domain owner. Loop-declared document output tools remain request-scoped and do
not appear in the global catalog above. When a loop declares an output, Thane
generates a tool such as `replace_output_metacognitive_state` or — for a
maintained document that declares `facets` — `publish_output_office_status`,
only for that loop run. Those generated names are stamped as `managed_by`, and
document reads return them as `write_tool`; `doc_write` will not bypass that
narrower owner. Every structured publisher routes through managed document
roots, so root policy, indexing, validation, and provenance remain centralized
instead of being reimplemented in each loop prompt.

## `email` — inbox traffic

| Tool | Description |
|------|-------------|
| `email_list` | List messages in one folder of one account, newest first, as JSON naming the account and folder beside every UID. In the drafts folder, a row that is one of Thane's open drafts carries `thane_draft {draft_id}`, matched on its UID and Message-ID together; a row without it is not Thane's. On an account whose `mailbox.labels` names labels, each row adds `labels`, the account's labels whose keyword the message carries, and `flag_label`, the label whose flag the message shows, present only when Thane's label record says Thane wrote that flag and the message still carries it as written; the raw `flags` stay beside them. |
| `email_read` | Read a message: a JSON header object, a `---` line, then the readable body. Whether it marks the message seen follows `mark_seen`, which defaults to false on an account with `mailbox.owner: operator` and true elsewhere; on an operator mailbox, `mark_seen: true` is refused in a turn the operator is not present for. `auto_submitted` and `bulk` report what the message's own headers claim about how it was sent; nothing authenticates those headers and any sender can set or omit them, so they never change the sender's trust zone. When an HTML body's own markup hides text from a person reading it (the `hidden` attribute, `aria-hidden="true"`, or an inline `display:none`, `visibility:hidden`, zero font-size, or zero opacity), that text is withheld from the rendered body and `hidden_content` `{present, chars}` reports it beside `body_source`. A hidden link's target is withheld with it, so `chars` is 0 when the markup hid only a link with no text. Only those inline idioms are recognised: text a stylesheet, a colour, or positioning hides stays in the body with no `hidden_content`. `labels` and `flag_label` are as in `email_list`, and a message read from the drafts folder carries `thane_draft` as in `email_list`. |
| `email_search` | Server-side IMAP search by text, headers, flags, dates (or deltas), Message-ID, and label. `flagged` and `unflagged` test `\Flagged` whoever set it, so a label's flag counts; `label`, offered only when a declared label has a keyword, searches for that keyword, and is refused on an account whose `mailbox.labels` does not name it. An argument the schema does not declare is refused, naming it and listing the accepted ones, and nothing is searched. Rows carry `labels`, `flag_label`, and, in the drafts folder, `thane_draft` as in `email_list`. |
| `email_folders` | List an account's mailboxes with their special-use role (`inbox`, `drafts`, `sent`, `trash`, `junk`, `archive`, `all`, `flagged`, `important`, or none), raw attributes, and counts. Names are used verbatim as an `email_move` `destination`; `destination_role` finds a folder by its role instead. |
| `email_mark` | Add or remove a flag, or a label the account carries; reports the UIDs affected and the UIDs not found. On an operator mailbox, adding `seen` is refused in a turn the operator is not present for. On an account whose `policy.delivery` is `drafts`, `answered` is refused, added or removed, because a draft is not an answer and Thane cannot know when the operator sends one. Adding `flagged` to a message whose flag is still the one Thane wrote for a label removes that colour's keywords, so it reads as a plain flag; removing `flagged` from such a message removes the colour's keywords with the flag; either way the message is listed under `thane_color_cleared`, and if the colour cannot be removed first, the removal is refused and the flag stays. `label`, offered only when a label without `apply` is declared, takes the place of `flag`: it is refused on an account whose `mailbox.labels` does not name it; it writes the label's keyword, and its colour only on a message without `\Flagged`; it refuses a colour label on a message carrying any flag but that label's own, listing it under `refused`; removing it takes away only what Thane recorded setting; a label with `apply` is refused; and in a folder whose `PERMANENTFLAGS` lack `\*`, a label that needs keywords refuses the whole call. The account's drafts folder is refused as the folder to act in. |
| `email_send` | Compose a message (markdown → MIME); the account's policy and the recipients' trust zones decide whether it is sent, held in the account's drafts folder for the operator, or refused with a decision record. On an account with `policy.draft_gate: relaxed` (delivery `drafts`), a recipient refused only for its zone is drafted with gating `draft_only`, named in the draft by bare address; automated, unresolvable, and unparseable recipients, and those the recipient-domain rules refuse, are refused there too. A draft needs a folder with the drafts role (`drafts_folder`, else the server's `\Drafts` mark); without one it is refused with route `no_drafts_folder` and nothing is sent in its place. The `bcc_owner` audit copy rides only sent mail. An account whose access is not `send` is refused, and the refusal says the message must not be written from another account. A drafted result carries `draft_id`, the draft's key in the draft ledger that the `email_drafts` tools take. |
| `email_reply` | Reply with threading headers through the same gate and decision, including the access refusal. In a turn the operator is not present for, a reply to a message marked `auto_submitted` or `bulk` is refused with route `automatic_response`, draft or not, except on an account with a relaxed `draft_gate`, which drafts a reply to `bulk` mail that is not `auto_submitted` and names the account's own address in its To or Cc, with route `personally_addressed_list_reply`; a lone `Precedence: junk`, the classic autoresponder mark, does not qualify. A reply that would be drafted is refused with route `draft_open` while an open Thane draft already answers the same message (the refusal names its `draft_id`, for `email_draft_revise`), and, on an account with `mailbox.owner: operator`, with route `operator_reply_started` when one `UID SEARCH HEADER In-Reply-To` of the drafts folder finds a reply to the same message that the draft ledger does not hold. Any message that would be drafted is refused with route `draft_limit` while the account already has 200 open Thane drafts. A reply sent directly while an open Thane draft answers the same message carries a `note` naming that `draft_id`, so it can be withdrawn. |
| `email_escalate` | Hand one message to the account's review pass, the loop its `mailbox.review_loop` names: queues `message:<account>:<message_id>` in that loop's work queue (a repeat coalesces, replacing the reason) and returns `{queued, subject, pending_for_review}`, with `pending_for_review` null when the queue could not be counted. Changes nothing in the mailbox. Refused, with nothing queued and the `email_mark` call to flag the message instead, on an account without a `review_loop`, for a message without a Message-ID, and from the review loop itself. |
| `email_move` | Move messages within an account. `folder` is always the source, and the target is exactly one of `destination` (an exact folder name) or `destination_role` (a special-use role Go resolves, taking `junk_folder` or `trash_folder` first); neither or both is refused, and so is a role no folder holds. In a turn the operator is not present for, only folders in the account's `mailbox.move_into` are accepted, plus INBOX out of one of them; in the operator's own turn `move_into` does not apply and any folder the account has is accepted. In every turn the drafts folder is neither source nor destination, and a folder the account lacks is refused. In a turn the operator is not present for, a move into the junk folder refuses each message from the operator's own record or a contact at `admin`, `household`, or `trusted` (or an address several contacts share when one of them is or may be at such a zone, or one the directory could not look up) and moves the rest. The result lists `moved` `{uid, destination_uid, message_id, from, trust_zone}` and `refused` `{uid, from, trust_zone, reason, recovery}`, reports the new UIDs when the server returns them, and records both UID lists in the Email Accounts block's recent operations. |

Every email tool takes an `account`; in a loop bound with
`email_account` an omitted account resolves to the binding and other
accounts are refused. The `email` tag also injects an **Email Accounts**
context block listing each account with its policy (`access`,
`delivery`, `draft_gate` when it is relaxed, recipient-domain rules,
the drafts folder), whose mailbox it
is when the operator marked it (`owner`, `writes_as`, `voice`,
`reads_mark_seen`), where it may file mail in a turn the operator is
not present for when it limits that (`junk_folder`, `move_into`, and
any `filing_note`), whether it may
hand mail to SMTP itself, whether this turn is `attended`, which trust
zones it sends directly to, drafts for, and refuses this turn, and its
cached folder names with roles. An account whose new mail wakes a loop
other than its owner's default shows `wake_loop`, and one with a review
pass shows `review_loop`, `pending_review` (the account's queued review
work, counted by the poller and on each enqueue, never at render), and
`pending_review_as_of`. An account whose `mailbox.labels` names labels
shows them as `labels` `[{label, meaning, shows_as, apply}]`, and
INBOX's `PERMANENTFLAGS` verdict as
`keywords` (`permanent`, `session_only`, or `unsupported`) once a
read-write SELECT of INBOX has reported it; rendering never asks the
server. An account whose `access` is `read`
reads without marking messages seen and refuses flags and moves.

**Review queue.** An account with `mailbox.review_loop` feeds that loop
through the loopqueue store, in the partition named after the loop. A
draft written on the account in a turn the operator is not present
for, by any loop but the review loop, queues `draft:<account>:<draft_id>`, and
`email_escalate` queues `message:<account>:<message_id>`; queueing a
subject again coalesces with the waiting item. Each item's `source` is
`email_review` and its summary is compact JSON: `{account, draft_id,
message_subject, drafted_by}` for a draft, `{account, folder,
message_id, from, message_subject, reason}` for a message, where
`message_subject` is the message's Subject header and the item's own
`subject` is its queue key. The review loop is woken with one
`email_review` event of type `review_pending`, whose metadata counts
`drafts`, `messages`, and `pending`. It carries three loop-private
tools, attached at hydration to whatever definition an account names
as `review_loop`, scoped to that loop's own partition, and never
registered globally or listed in the tool catalog:

| Tool | Description |
|------|-------------|
| `queue_pull` | Pull one batch per iteration (default 5, at most 25), highest priority first, then oldest. Each item gives `subject`, `source`, `summary`, `priority`, and `age`; the result adds `remaining`, the depth behind the batch (null when it could not be measured), and `oldest_wait`. A second pull in the same iteration is refused. |
| `queue_ack` | Remove the exact item `queue_pull` returned, by subject, using a hidden one-shot receipt. Answers `ok`, `already_acknowledged`, or `retained_newer` when the subject was queued again while the item was worked; the newer item then stays queued. |
| `queue_defer` | Keep a pulled item pending, moved behind every item currently queued, for a later wake. |

The review loop has no `queue_enqueue`: its work comes only from drafts
and escalations, and `email_escalate` refuses a call from the review
loop itself.

**Labels.** A label declared in `email.labels` and named in an
account's `mailbox.labels` is written there as its IMAP keyword, its
flag colour (`\Flagged` plus `$MailFlagBit0` to `$MailFlagBit2`), or
both, always with `+FLAGS` and `-FLAGS`, never a replacing `STORE`, and
never marking mail seen. The poller applies each label with `apply:
contact_matched` to every new INBOX message whose sender matches exactly
one contact record, once per message, before the message's wake is
dispatched; the wake's `flags` metadata never includes a mark Thane
still claims, even when a failed dispatch lists the message again or
the account no longer carries the label, and a failed `STORE` is logged
and costs only that message's label. What Thane set on each message
(the keywords, the colour and its keywords, whether it set `\Flagged`,
the derived labels applied, and the copy marked, by folder,
UIDVALIDITY, and UID) is recorded in the operational state store,
namespace `email_labels`, key the hex SHA-256 of the account name's
byte length, `:`, the account name, and the bare Message-ID, for 90
days after the last change; a record whose own account and Message-ID
differ from the pair asked for is refused. Removing a label and handing
a flag to the operator touch only what that record claims, on the one
copy it describes, and a flag counts as Thane's only while it still
carries exactly the recorded colour; `flag_label` on a row is read from
the same record, under the message's whole Message-ID. A second copy
of the message, and the message once someone else moves it, is not
that copy, so its marks are the operator's; `email_move` carries the
record to the new copy when the server returns COPYUID and the call
moved no other copy under the same Message-ID, since go-imap keeps the
COPYUID sets sorted rather than paired. Keywords,
colour keywords included, are written only in a folder whose
`PERMANENTFLAGS` include `\*`. See
[Configuration](../operating/configuration.md#labels).

## `email_drafts` — Thane's drafts in flight

| Tool | Description |
|------|-------------|
| `email_drafts` | List the drafts Thane wrote, from the draft ledger, after checking each open one against its account's drafts folder: `draft_id`, account, `stage` (`open`, `gone`, or `withdrawn`), `closed_reason`, subject, recipients, the original it answers, the revision count, and `last_revised {by, at}`. Closed entries are listed only with `include_closed`. An account whose drafts folder could not be checked is listed under `errors`. |
| `email_draft_get` | Read one draft by `draft_id` beside the message it answers: a JSON header (recipients, version history, and the account's `owner`, `writes_as`, and `voice`), a `---` line, the draft's text/plain part, another `---` line, and the original's body, found by Message-ID in the folder it was read from. Both are read with PEEK. |
| `email_draft_revise` | Replace an open draft's body; recipients, subject, and threading headers never change. Holding the client lock throughout, it requires UIDPLUS or IMAP4rev2, proves the draft is still Thane's, writes a write-ahead record with a fresh Message-ID, appends the new version with `\Draft` and `\Seen`, records its UID in the write-ahead record, checks the old UID is still there and not already marked `\Deleted`, stores `\Deleted` on it (with `UNCHANGEDSINCE` when the server has CONDSTORE), reads the flag back, and expunges that UID alone. A draft the operator touched mid-revision stays theirs and the new copy is removed. The outbound inspector reviews the new body. Refusal reasons: `gone`, `held`, `withdrawn`, `no_uidplus`, `access`, `inspector`. |
| `email_draft_withdraw` | Move an open draft to the account's trash folder (`trash_folder`, else the server's `\Trash` mark; `mailbox.move_into` does not apply) and mark its entry `withdrawn`. With no trash folder known it refuses with `no_trash_folder` and leaves the draft in place. Refused on an account whose access is `read`. |

Every draft `email_send` or `email_reply` writes is recorded in a draft
ledger in the operational state store (namespace `email_drafts`, key
`<account>/<draft_id>`, where `draft_id` is a UUIDv7 the drafted result
returns). An entry holds the draft's UIDVALIDITY, UID, and Message-ID,
the original's Message-ID and folder when it is a reply, its recipients
and subject, its stage, a revision history of `{by, at, note}`, and the
previous body one level deep. A draft is Thane's only while the drafts
folder still holds it at the recorded UIDVALIDITY, UID, and Message-ID,
not marked `\Deleted`;
an edit in the operator's client stores a new copy under another UID,
and that draft becomes the operator's. The ledger records no review or
approval state: its stages are only `open`, `gone`, and `withdrawn`.
The draft tools reconcile the ledger when they are called, by examining
the drafts folder and fetching only the recorded UIDs. They settle any
revision a crash interrupted, adopting the new version only when the
operator did not touch the draft meanwhile, and any draft whose UID the
server never reported, which is found again by its Message-ID and the
digest of its bytes, or closed once nothing carries its Message-ID.
Each account keeps at most 200 entries: closed ones are forgotten
oldest first and an open one never, so a new draft is refused with
route `draft_limit` while 200 are open. Closed entries expire 14 days
after they close. Every draft tool
refuses a `draft_id` the ledger does not hold. The tag is separate from
`email` so that a loop that only triages or writes first drafts never
sees these tools; a loop that edits drafts carries both.

## `contacts` — directory and vCard administration

| Tool | Description |
|------|-------------|
| `contact_save` | Create or update a contact with vCard properties. Model-authored property rows retain turn provenance. Refuses `trust_zone`, `KEY` and `X-THANE-*` fact keys, fact keys that are not plain names or that name a field the record owns, and control characters in argument values. Outside the operator's own message, refuses to add an address, number, or notification routing fact (`notification_preference`, `ha_companion_app`, in any case) to a contact above `known` or to the operator's contact, or to change such a contact's nickname; in every turn, refuses a value an elevated or operator contact already holds, and a new contact's name or any nickname one already goes by. Routing facts are additive and delivery reads only the first value, so the result names the value still used when a new one lands behind it. When an archivist refresh consumer is enabled, a committed change coalesces one canonical contact refresh; identical no-ops never enqueue one. |
| `contact_lookup` | Search by name, query, kind, or property. A name matches a formatted name or nickname; when several contacts answer to it, the operator's contact wins, then one above `known`, then a formatted-name match before a nickname match, then the lowest ID, and only a name nothing answers to falls back to a search that must match one contact. When dossiers are configured, a name result includes the canonical UUID and the `contact_dossier_read` trailhead. |
| `contact_dossier_read` | Read or probe the canonical dossier for an active contact UUID. Go derives the ref and tracks revision state. Every success exposes `dossier.exists`, `dossier.ref`, and `dossier.document`; an absent dossier is a successful result with a null document and the exact create action. |
| `contact_dossier_write` | Create or replace a canonical contact dossier from four structured projections; Go owns its ref, private tag, frontmatter, and section layout, and requires full canonical UUIDs in archive-session citations. Validates every projection together: a rejected write stores nothing and lists each violation in one error, an over-budget field with its overage and whether rewording closes it or whole items must go. A replacement with no read on record, or against a dossier that changed since that read, is refused as an error too. Refuses a contact's first dossier while an active contact that shares its name and looks like the same person (a shared address or number, a duplicate with no address of its own, or a `known` duplicate bound to a Home Assistant person) already has one, naming both UUIDs, the evidence, and which record keeps the dossier; people who merely share a name each keep their own, and replacing an existing dossier is never refused. Available only for a managed-writable `contacts` root. |
| `contact_whereabouts` | Fuse a contact's room, HA zone, and bound-device location sources with provenance, freshness, and explicit room conflicts. |
| `contact_forget` | Soft-delete one `known` contact, selected by exactly one of a name (resolved as `contact_lookup` resolves it) or a canonical `contact_id`, and name the record removed. Refuses contacts above `known`, the operator's contact, and contacts bound to a Home Assistant person, by name or by ID; a refusal by name names up to three removable `known` contacts the name also fits, with their UUIDs. |
| `contact_list` | List and filter contacts. |
| `contact_export_vcf` | Export one contact as a vCard. |
| `contact_export_vcf_qr` | Export one contact as a vCard QR code. |
| `contact_export_all_vcf` | Bulk vCard export. |
| `contact_import_vcf` | Import one or more vCards. Drops `KEY`, `X-THANE-KEY-*`, malformed property names, and values carrying control characters, drops addresses, numbers, notification routing facts, and nickname fills merged into an elevated or operator contact, addresses or numbers already held by one, and a nickname fill one already goes by, and skips a card that would create a contact under a name or nickname one already goes by; rows carry turn provenance and the result counts every drop and names each skipped card. |

## `owner` — trusted operator context

| Tool | Description |
|------|-------------|
| `contact_owner` | Return the runtime operator contact, canonical UUID, configured dossier trailhead, and active operator channels. Protected tag; name retained for compatibility. |
| `contact_dossier_read` | Read or probe the operator's canonical dossier when the managed `contacts` root is available; also exposed through the `contacts` capability. |
| `contact_dossier_write` | Write a canonical dossier from an operator-origin turn when the managed `contacts` root is available; also exposed through the `contacts` capability. |

Operator channel activity recency is reported with delta fields such as
`last_active_delta`.

## `files` — workspace filesystem access

| Tool | Description |
|------|-------------|
| `file_read` | Read file contents. |
| `file_write` | Write file contents. |
| `file_edit` | Targeted edit with a diff preview. |
| `file_list` | List directory contents. |
| `file_search` | Search for files by name. |
| `file_grep` | Search file contents with regex, optionally filtering filenames with `file_pattern` glob syntax. |
| `file_stat` | Get file metadata. |
| `file_tree` | Render a directory tree. |
| `create_temp_file` | Create a temp file with a labelled path. |
| `repo_git_log` | Read bounded commit history from one named repository root. |
| `repo_git_diff` | Read a bounded patch or diffstat between commits in one named repository root. |
| `repo_git_show` | Show one commit and its bounded patch from one named repository root. |
| `repo_git_blame` | Read structured line attribution for one file in one named repository root. |

File tools resolve configured non-indexed roots and dynamically registered
repository roots such as `thanecode:` before applying workspace policy.
Indexed managed roots are deliberately unavailable through every `file_*`
surface; use `doc_browse`, `doc_search`, `doc_read`, and the returned
`write_tool` so the Markdown codec remains private. A
forge subscription registers a repository root from its `repo_root` handle;
the host checkout path is not model-facing. Repository roots are read-only.
When a loop carries a `repo_root` binding, an unprefixed relative file path
defaults to that root and every other named root—including document roots—is
refused. The `repo_git_*` tools have the same boundary: an omitted `root` uses
the binding, while unbound callers must name a repository root. This defaulting
does not apply to `forge_repo_follow`: omitting `repo_root` there requests
event-only tracking because the tool creates rather than reads a root.
`file_stat` reports modification recency as `modified_delta`.

## `shell` — host command execution

| Tool | Description |
|------|-------------|
| `exec` | Run a host shell command with configurable allow/deny guardrails. |

## `web` — web search and fetch

| Tool | Description |
|------|-------------|
| `web_search` | Search via the configured backend (SearXNG/Brave). |
| `web_fetch` | Extract readable content from a URL. |

## `media` — transcript and analysis

| Tool | Description |
|------|-------------|
| `media_transcript` | Fetch a video/podcast transcript via yt-dlp. Also tagged `web`. |
| `media_save_analysis` | Save a media analysis to the configured vault with generated-document provenance. |

## `feeds` — RSS/Atom and channel subscriptions

| Tool | Description |
|------|-------------|
| `media_follow` | Follow an RSS/Atom feed or YouTube channel. |
| `media_unfollow` | Stop following a feed. |
| `media_feeds` | List followed feeds and their status. |

`media_follow` accepts an optional `wake_loop` target. New feed entries are
always delivered as structured event-source wakes — when `wake_loop` is
omitted they route to the built-in `media-default-handler` event-driven
loop; when set, they route to that loop instead (e.g. a
`thane_loop_create` service loop's managed document). The retired pre-PR-T2c
"default media-analysis conversation" spawn path no longer exists.

## `attachments` — vision pipeline

| Tool | Description |
|------|-------------|
| `attachment_list` | List known attachments with metadata. |
| `attachment_search` | Semantic or tag search over attachment descriptions. |
| `attachment_describe` | Produce/refresh a vision description for an attachment. |

Attachment list and search results report arrival recency as
`received_delta`.

## `forge` — GitHub/code collaboration

| Tool | Description |
|------|-------------|
| `forge_issue_list` | List issues with filters. |
| `forge_issue_get` | Get an issue's details. |
| `forge_issue_create` | Create an issue. |
| `forge_issue_update` | Update issue fields. |
| `forge_issue_comment` | Comment on an issue. |
| `forge_pr_list` | List pull requests. |
| `forge_pr_get` | Get a PR's details. |
| `forge_pr_diff` | Retrieve a PR's diff. |
| `forge_pr_files` | List changed files in a PR. |
| `forge_pr_commits` | List commits in a PR. |
| `forge_pr_checks` | List current check-run/check-suite status for a pull request. |
| `forge_pr_reviews` | List reviews on a PR. |
| `forge_pr_review` | Submit a review. |
| `forge_pr_review_comment` | Comment on a specific line in a PR. |
| `forge_pr_merge` | Merge a PR. |
| `forge_pr_request_review` | Request reviewers on a PR. |
| `forge_react` | Add an emoji reaction to an issue/PR/comment. |
| `forge_search` | Search code and issues across the forge. |
| `forge_repo_follow` | Follow a repository for release/commit events, optionally expose a named read-only repository root, and wake an existing loop. |
| `forge_repo_unfollow` | Stop following a repository event subscription. |
| `forge_repo_subscriptions` | List repository event subscriptions and target loops. |

Repository subscriptions require `wake_loop` so event handling is owned by
an existing loop, usually one created with `thane_loop_create`
(`operation: service`) for a specific managed document. Pass a stable
`repo_root` handle such as `thanecode` when the loop should read a local
mirror. Thane chooses the physical path beneath `workspace.path`, performs
the initial clone, and registers the handle as read-only; a failed clone
fails the call rather than storing a root that does not exist. The poller
syncs the root before delivering repository events, and event metadata carries
`repo_root` plus `last_synced_sha`. Unfollowing leaves the checkout on disk.
Handles use lowercase ASCII letters, digits, `.`, `_`, and `-`; uppercase
input is canonicalized to lowercase so two spellings cannot select the same
checkout on a case-insensitive filesystem.
Omit `repo_root` for event-only tracking. A caller carrying a `repo_root`
binding may explicitly name only that root, but omission still remains
event-only rather than implicitly creating another checkout.
With `forge.subscription_check_interval` unset or zero the checkout is still
created and is accurate as of that moment, but nothing refreshes it and the
subscription wakes no loop; the response says so.

## `scheduler` — time-based tasks

| Tool | Description |
|------|-------------|
| `task_schedule` | Schedule a future task. |
| `task_list` | List scheduled tasks. |
| `task_cancel` | Cancel a scheduled task. |

Task next-run values include a model-facing delta.

## `thane_*` family — intent-shaped front door for "do work"

Core (`thane_now`, `thane_assign`, `thane_loop_create`). Pick by
lifecycle; `thane_loop_create` takes an explicit `operation`
(`service` / `event_driven` / `container`).
External wakes to live loops are
infrastructural rather than tool-shaped — producer subsystems dispatch
structured envelopes over the message bus directly, and
`request_core_attention` covers loop → core/owner attention escalation.

| Tool | Lifecycle | Description |
|------|-----------|-------------|
| `thane_now` | sync | Synchronously delegate a bounded task and return the result inline. |
| `thane_assign` | async one-shot | Assign a task to a sub-agent that runs in the background and reports back through the current conversation/channel when complete. |
| `thane_loop_create` (`operation: service`) | recurring | Scaffold a declared output document in the exact shape its generated `publish_output_*` tool writes (optional `output.initial` seeds the first publish; after the old loop is stopped, `output.migration` explicitly supplies newly required projections when a replacement changes the contract; working notes ride alongside) and launch a self-paced recurring loop that maintains it; `entities` surface HA subscriptions into the loop's context. |
| `thane_loop_create` (`operation: container`) | durable container | Create a non-executing loop container that groups descendant loops and provides inheritable tags. |

`thane_now` and `thane_assign` accept `context_mode`. The default,
`task`, gives the child run a compact
task-worker prompt with active capabilities, tagged context, and current
conditions, but without full Thane identity files, inject files,
always-on talents, or conversation-history dressing. Use
`context_mode=full` only when the delegated work genuinely needs that
continuity.

The delegate family (`thane_now`, `thane_assign`) uses capability tags as
its primary tool and context scope. Delegates inherit elective caller tags
by default so child work keeps the same task context; explicit `tags`
override profile default tags.
When the resulting scope includes `ha`, the executor uses HA-oriented
budget and routing hints automatically; otherwise it uses the general
delegate defaults.
Use root trailhead tags such as `development`, `home`, `operations`,
`knowledge`, `media`, `interactive`, or `people` when the delegate should
read the menu guidance and choose a narrower branch. Use leaf tags such
as `ha`, `files`, `forge`, `web`, `loops`, `documents`, or `diagnostics`
when the caller already knows the needed toolset.
Runtime and channel affordance tags such as `owner` and `message_channel`
are re-asserted only from trusted runtime context; they are not inherited
as model-requested tags. Use `inherit_caller_tags: false` when a delegate
needs a strict fresh tool scope.

## `loops` — lower-level loop control and inspection

Below the `thane_*` family. Use these for inspection, control, or
unusual launch shapes (event-driven, mqtt-wake-only,
supervisor-randomized metacog) where the canonical family doesn't fit.

| Tool | Description |
|------|-------------|
| `loop_status` | Snapshot of currently running loops, plus a parent→child `tree` projection over the whole registry and a whole-registry health rollup naming each degraded loop with its reason: consecutive errors, an error state, or a durable write a completed wake never landed (listed on the loop's row as `unpublished_writes`). |
| `loop_containers` | Placement directory of container loops (intent, child/descendant counts, conferred tags, sample children) — the loop-graph analog of `doc_roots`. |
| `set_next_sleep` | From inside a service loop, request the next sleep duration. |
| `spawn_loop` | Launch an ad-hoc loop from a definition and input. |
| `stop_loop` | Stop a running loop. |
| `loop_definition_list` | List registered loop definitions. |
| `loop_definition_get` | Retrieve a loop definition's spec. |
| `loop_definition_set` | Create or update a loop definition. |
| `loop_definition_delete` | Remove a loop definition. |
| `loop_definition_lint` | Validate a proposed loop-definition spec. |
| `loop_definition_launch` | Launch a persistent loop from a definition. |
| `loop_definition_set_policy` | Update a loop definition's lifecycle policy. |
| `loop_definition_summary` | Summary view across definitions. |

## `mqtt` — wake subscriptions

See [MQTT](../operating/mqtt.md) for the broker-side conventions.

| Tool | Description |
|------|-------------|
| `mqtt_wake_list` | List runtime and config-defined wake subscriptions. |
| `mqtt_wake_add` | Add a runtime wake subscription that delivers matching messages to a `wake_loop` target. Required args: `topic` and `wake_loop` (loop name/ID, optional tags + instructions). The legacy inline routing fields (`mission`, `quality_floor`, etc.) were retired in PR-T2b; point `wake_loop` at the built-in `mqtt-default-handler` for generic triage. |

MQTT wake delivery rides the shared loopqueue chassis: matching
messages enqueue durably per target and a short debounced wake drains
the coalesced batch, so a burst on one topic becomes one iteration
(latest payload wins per subscription+topic) and a message received
just before a crash is replayed at the next startup. A payload may
self-address by including `target_loop` (a loop definition name) — a
general-purpose HA automation publishing to one shared wake topic can
wake any named loop with no per-topic subscription; unresolvable
targets fall back to the subscription's `wake_loop`.
| `mqtt_wake_remove` | Remove a runtime wake subscription by ID. |

## `message_channel` — current message-app conversation

Normalized tools for the active message-app channel. Providers such as
Signal adapt these calls to their native APIs. Inbound message-app
bridges assert this capability as a runtime fact; it does not need to be
listed in `channel_tags` or contact `origin_tags`.

| Tool | Description |
|------|-------------|
| `send_reaction` | React inside the current message-app conversation. |

## `signal` — Signal messaging

Declared via a Provider with async binding; handlers return
[`tools.ErrUnavailable`](../../internal/tools/provider.go) when
signal-cli isn't connected. In inbound Signal conversations, final
response text is sent automatically by the bridge; prefer
`message_channel` for in-channel reactions and reserve these native
tools for Signal-specific workflows.

| Tool | Description |
|------|-------------|
| `signal_send_message` | Send a Signal message to a phone number. |
| `signal_send_reaction` | React to an inbound Signal message. |

## `companion` — native companion app integration

| Tool | Description |
|------|-------------|
| `contact_whereabouts` | Resolve a person first, then rank their presence and bound-device location sources without guessing through room conflicts. |
| `companion_last_known_location` | Read one device's durable last location with provenance and freshness. |
| `macos_calendar_events` | Query the local macOS Calendar (companion app required). |

## `models` — model registry and routing

| Tool | Description |
|------|-------------|
| `model_registry_list` | List available models with capability metadata. |
| `model_registry_get` | Retrieve one model deployment's metadata. |
| `model_registry_summary` | Summary of routing policy and cost tiers. |
| `model_route_explain` | Dry-run a routing decision with the router's rationale. |
| `model_deployment_set_policy` | Update deployment-level routing policy. |
| `model_resource_set_policy` | Update resource-level routing policy. |

Policy here is fleet-wide and survives restart. Holding one conversation
to one model is a `session` decision (`conversation_model_pin`, above),
and pinning a loop definition is `loop_definition_update` under `loops`.

## `diagnostics` — operational visibility

| Tool | Description |
|------|-------------|
| `get_version` | Agent version, build info, and commit SHA. |
| `cost_summary` | Aggregated token usage and cost (uses `usage.Summary`, including `cache_hit_rate`). |
| `logs_query` | Query the structured log index with attribute filters. |
| `system_health` | The annunciator panel: one ok/degraded/failed row per subsystem, plus host basics, per-partition queue depths, a 24h telemetry rollup, the deploy story (running vs previous version, recent boots), and the process's own WARN/ERROR rates. |
| `queue_status` | Read-only work-queue audit: live pending depth and oldest-item age per consumer, completion statistics over a window, and the most recent completions. |
| `doc_activity` | Revision-churn report over the managed document roots: revisions, net line delta, size, and authorship per document, with runaway-growth flagging. |
| `loop_activity` | Journal-backed loop history: every wake with its attributed cause and sender, iteration outcomes, errors, and state changes — survives restarts and covers stopped loops. |

## MCP tools

Thane hosts MCP servers as subprocesses and bridges their tools into
the registry as `mcp_{server}_{tool}`. MCP tools inherit their default
tags from the MCP config's `default_tags` or configuration-side tag
overrides; they do not have a compiled-in entry in
`internal/model/toolcatalog/catalog.go`.

Home Assistant needs no MCP bridge: the native `ha_*` tool set above is
the complete HA surface (the `ha-mcp` bridge was retired in v0.10.2 once
native parity landed). MCP remains the extension path for capabilities
Thane doesn't implement natively; `include_tools` filtering in the config
narrows the bridged surface.

See [Delegation & MCP](../understanding/delegation.md) for
configuration details.
