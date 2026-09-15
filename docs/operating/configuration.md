# Configuration

Thane is configured via a YAML file. See
[Getting Started](getting-started.md) for where the config file lives and
`examples/config.example.yaml` for the full field reference with inline
documentation.

This guide covers the major config sections organized by concern.

## Models & Routing

```yaml
models:
  ollama_url: http://localhost:11434
  default: qwen2.5:20b
  available:
    - name: qwen2.5:20b
      provider: ollama
      quality: 6
      speed: 7
      cost_tier: 1
    - name: claude-sonnet-4-20250514
      provider: anthropic
      quality: 9
      speed: 5
      cost_tier: 4
```

**`ollama_url`** — Where your Ollama instance lives. Required.

**`default`** — The model used when no virtual model specifies otherwise.

**`available`** — List of models Thane can choose from. Each model has a
quality score (1-10), speed score (1-10), and cost tier (1-5). The router
uses these scores plus routing hints to select the best model for each
request. You don't hardcode which model handles which task — you describe
the models, and Thane's router does the matching.

See [Virtual Models](routing-profiles.md) for how virtual models map to
model selection.

## Anthropic (Cloud Models)

```yaml
anthropic:
  api_key: sk-ant-...
```

Optional. Enables cloud models for complex reasoning. Without this, Thane
runs entirely on local models.

## Home Assistant

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: your_long_lived_access_token
  ingest_rate_limit_per_minute: 12  # optional: cap on state-change events ingested per entity per minute
  # registry_cache_ttl and floor_alias are also optional — see homeassistant.md
```

`url` and `token` are required; the token needs access to the entities and
services you want Thane to interact with.

As of v0.10.2 the former `homeassistant.subscribe` block is retired — a stale
`subscribe:` key will fail the boot. Its `rate_limit_per_minute` moved to the
top-level `ingest_rate_limit_per_minute`, and entity globs are no longer a
config concept: they are runtime ingest-mode subscriptions, declared with
`add_entity_subscription` (or the model's `watch_entity`) after boot. See
[Home Assistant](homeassistant.md) for setup details.

## MQTT

```yaml
mqtt:
  broker: tcp://homeassistant.local:1883
  username: thane
  password: your_password
  wake_subscriptions:
    - topic: frigate/events
```

Required. See [MQTT](mqtt.md) for broker setup, telemetry entities, and
wake subscriptions.

## Email

```yaml
email:
  bcc_owner: "Operator <operator@example.com>"
  poll_interval: 300
  accounts:
    - name: primary
      description: "Thane's own mailbox. Correspondence with the household and trusted contacts."
      imap:
        host: imap.example.com
        port: 993
        username: thane@example.com
        password: ${IMAP_PASSWORD}
        tls: true
      smtp:
        host: smtp.example.com
        port: 587
        username: thane@example.com
        password: ${SMTP_PASSWORD}
        starttls: true
      default_from: "Thane <thane@example.com>"
      sent_folder: Sent
      drafts_folder: Drafts
      policy:
        access: send
        delivery: by_trust_zone
        denied_recipient_domains: [example.org]
    - name: packages
      description: "Parcel and delivery notifications. Read and file only; never sends."
      imap:
        host: imap.example.com
        port: 993
        username: packages@example.com
        password: ${PACKAGES_PASSWORD}
      policy:
        access: organize
```

Optional. Each entry under `accounts` is one mailbox; the first is the
primary, which tools use when no `account` is named and no loop binding
selects one. `description` is shown to the model beside the account so it
learns what a mailbox is for before acting rather than by being refused.
An account with an `smtp` block can send and needs `default_from`; one
without is read-and-organize only. `sent_folder` names the IMAP folder that
receives a copy of every sent message. `tls` defaults to true on every port
except 143 and `starttls` to true on every port except 587's alternative,
465; both accept an explicit `false`.

Each account's `policy` says what the model may do there and where the
mail it writes goes. `access` is `read` (list, search, and read without
marking seen), `organize` (also flag and move), or `send` (also compose,
reply, and draft); it defaults to `send` when `smtp` is configured and
`organize` otherwise, and an account with `smtp` can still be held at
`organize` to keep its credentials for the operator's own use. `delivery`
decides what happens once every recipient has passed the trust gate:
`by_trust_zone` (the default) sends directly to `admin` and `household`
recipients when the operator is present for the turn, holds mail for `trusted`
recipients in the drafts folder for the operator to send from their own
client, refuses `known` and unknown recipients, and holds everything an
unattended loop writes, so a poller-woken handler never sends on its own;
`drafts` holds every message, and by default drafts for more recipients
than the gate allows elsewhere (see `draft_gate` below); `direct` sends
everything the gate allows,
including from unattended turns, and should be chosen deliberately. The
operator is present only for their own message: one sent through Thane's
native API, or one they wrote in a conversation bound to their own contact.
A poller wake, a scheduled loop, a loop launched from the operator's
conversation, and a call through the Ollama-compatible shim that Home
Assistant automations and voice satellites use are all unattended. A
drafted message carries no `bcc_owner` audit copy, on any account: the
operator sends it from their own client, under the account's
`default_from`, and nothing Thane adds rides along.

`draft_gate` says how the trust gate treats recipients on an account
whose `delivery` is `drafts`, where the operator reads and sends every
message by hand. `relaxed`, the default with `delivery: drafts`, drafts
for anyone a person could answer: a recipient the gate would refuse
only for its trust zone (an address with no contact record, a `known`
contact, or an address several records share whose least privileged
record is at a blocked zone) is drafted instead, and the decision
records it with gating `draft_only`. An automated mailbox, an address
the directory could not be consulted for, the recipient-domain rules
below, and the 50-recipient limit still refuse there, and one refused
recipient still refuses the whole message. A relaxed account also
drafts one kind of reply that would otherwise be refused as an
automatic response (see below). `strict`, the default with every other
delivery mode, applies the full gate. `relaxed` beside any `delivery`
other than `drafts` is refused at startup, because a recipient the gate
relaxed could then be sent to, and so is any value other than those
two. The model sees `draft_gate: relaxed` in the account's Email
Accounts entry, whose `drafts_for` then lists every zone and whose
`refuses` lists none. **Upgrading:** an account already configured with
`delivery: drafts` becomes relaxed without any change to its file; set
`draft_gate: strict` to keep the gate it had.

`drafts_folder` names, exactly, the folder drafts go to, for a server
that marks none with the `\Drafts` special-use attribute; leave it
empty to use the folder the server marks, found in the cached folder
listing or, when the cache names none, by one fresh listing. Thane
never guesses a drafts folder by name. When neither the key nor the
server names one, a message that would have been drafted, for whatever
reason, is refused with route `no_drafts_folder`, and nothing is sent
in its place, on every delivery mode; the model is told to ask the
operator to configure `drafts_folder`, and, when it asked for the draft
itself, not to resend the message without `draft: true`. A listing that fails is reported
as an error rather than as that refusal. `email_mark` and `email_move`
refuse to act in the drafts folder only once one is known, so on a
server without special-use attributes set `drafts_folder` to keep those
guards in force.

Thane records each draft it writes in a draft ledger in the operational
state store; nothing configures it. An entry holds the draft's
UIDVALIDITY, UID, and Message-ID, the message it answers, and a short
revision history. The `email_drafts` tools use it to revise or withdraw
a draft while it is still Thane's, meaning the drafts folder still
holds it at that UID with that Message-ID, not marked `\Deleted`. An edit in the operator's
own client stores the draft anew under another UID, and from then on it
is the operator's: no tool touches it again. Revising needs a server
that advertises UIDPLUS or IMAP4rev2; without either, revision is
refused. Thane keeps one draft per message: a second draft answering a
message while Thane's first is still open is refused, and on an
operator mailbox so is a draft answering a message when the drafts
folder already holds a reply to it that is not one of Thane's open
drafts, most likely the operator's own. Each account keeps at most 200
entries, forgetting closed ones first and never an open one, so a new
draft is refused while 200 are open; an entry that has closed is
forgotten 14 days later.

`junk_folder` and `trash_folder` name, exactly, the folders that hold
spam and deleted mail, for a server that marks no folder with the
`\Junk` or `\Trash` special-use attribute; leave them empty to use the
folder the server marks. They are what `email_move`'s
`destination_role: junk` and `destination_role: trash` resolve to
first, and a role that neither a key nor the server answers is refused
by name rather than guessed. The trash folder is also where
`email_draft_withdraw` moves a draft Thane withdraws, whatever
`move_into` lists; with no trash folder known, the withdrawal is
refused and the draft stays where it is.
`denied_recipient_domains` refuses recipients at those domains and their
subdomains regardless of trust zone, and `allowed_recipient_domains`,
when set, refuses every domain outside it. An automated-looking
recipient (a no-reply, notification, or bounce mailbox; see
[Contacts & CardDAV](#contacts--carddav)) is refused whatever its
record's zone. A reply to a message whose own headers mark it
automatic or bulk (an `Auto-Submitted` value other than `no`, a
`List-Id` or any RFC 2369 `List-*` field, or a `Precedence` of
`bulk`, `list`, or `junk`) is refused with route `automatic_response`
in every turn the operator is not present for, whatever the
`delivery` mode and even when a draft was requested, and the
operator's own turn replies as usual. The one exception is on an
account with a relaxed `draft_gate`: an unattended reply to list mail, marked by a
`List-Id` or a `Precedence` of `bulk` or `list` and not `Auto-Submitted`, whose own `To` or `Cc` holds the
account's address (its `default_from`, else its IMAP username when that
is an address, compared without regard to case), is drafted with route
`personally_addressed_list_reply`, because a person on a list answers
mail addressed to them. List mail that reached the account only through
the list's address stays refused, and so does anything
`Auto-Submitted` (automatic replies, bounces, notifications), mail
marked `Precedence: junk`, which classic autoresponders set without
`Auto-Submitted`, and mail whose only list marks are `List-*` fields
such as `List-Unsubscribe`; an alias
or plus address of the account does not count as its address. Every send ends in one of
three dispositions, `sent`, `drafted`, or `refused`, and the tool result
or refusal carries the decision that produced it.

`bcc_owner` receives a blind copy of every message the agent sends, that
is, every `sent` disposition; a draft carries none. It is an audit copy
for the operator, not a recipient the agent chose, and it is exempt from
the trust gate.

### Whose mailbox

```yaml
    - name: personal
      description: "The operator's own inbox."
      imap:
        host: imap.example.com
        port: 993
        username: alice@example.com
        password: ${PERSONAL_PASSWORD}
      default_from: "Alice Example <alice@example.com>"
      policy:
        access: send
        delivery: drafts
      mailbox:
        owner: operator
        voice: "First person as Alice; brief; sign with her first name only."
        move_into: [role:junk]   # the default for an operator mailbox
        filing_note: "Server rules file lists and receipts; INBOX is what is left for Alice."
        review_loop: email-draft-review   # optional second pass; unset means none
```

Each account's `mailbox` block says whose mailbox it is, how mail
written from it should sound, and where `email_move` may file its
mail. Whether the model may compose or move at all, and where its mail
goes, is `policy`. `owner` is
`assistant` (the default), a mailbox Thane keeps for itself or for a
purpose the operator gave it, or `operator`, the operator's own inbox,
which Thane helps with but does not own. Any other value is refused at
startup. On an operator mailbox `email_read` leaves mail unread unless
the call asks otherwise, and a turn the operator is not present for
cannot mark mail seen, whether by reading it or with `email_mark`; the
operator's own turn can. New mail on an operator mailbox wakes the
built-in `email-owner-triage` unless `wake_loop` names another loop (see
"Passes" below). `voice` is a
note of at most 500 bytes on how mail from the account should sound; a
longer one is refused.

`move_into` lists the only folders `email_move` may file this
account's mail into. An entry is `role:<role>`, the folder holding that
special-use role (`inbox`, `sent`, `trash`, `junk`, `archive`, `all`,
`flagged`, or `important`, resolved through `junk_folder` and
`trash_folder` first, then the server's own marks), or an exact folder
name; `"*"` on its own allows every folder and cannot be combined with
other entries. The default is `[role:junk]` on an operator mailbox,
where the server keeps its own filing tree and only spam leaves INBOX,
and `["*"]` on every other account, which is how accounts behaved
before the key existed. Moving mail back to INBOX out of a listed
folder is always allowed, so a move can be undone and mail rescued from
junk. The drafts folder can never be listed, by role or by name, and
`email_move` refuses it as a source too, as `email_mark` does. The
limit applies in every turn, the operator's own included: it is
configuration, not a judgment about who asked. An unknown role, an
empty entry, or `"*"` beside other entries is refused at startup.
`filing_note` is an optional sentence of at most 300 bytes on how the
mailbox is filed, shown to the model as written.

In a turn the operator is not present for, a move into the junk folder,
by role or by name, is also checked message by message: a message whose
sender is the operator's own contact record, is held by a contact at
`admin`, `household`, or `trusted`, shares its address with such a
contact (or with more contacts than the directory could name, any of
whom might be one), or could not be looked up in the directory stays
where it is,
and the rest of the batch moves. The result lists each refused message
with its sender, zone, reason, and recovery (flag it, and bring it to
the operator if it cannot wait), and each refusal is logged with the
loop and conversation that asked. The operator's own turn is not
checked. No key changes this guard.

The model sees these in the account's Email Accounts entry: `owner`
(only when it is `operator`), `writes_as` (the full `default_from`,
display name included), `voice`, and on an operator mailbox
`reads_mark_seen: false`. On any account whose `move_into` is not
`["*"]` it also sees `move_into` resolved to folder names, with
`role:<role>` standing in for a role no folder is known to hold yet.
Those accounts and every operator mailbox also show `junk_folder`
(once configuration or the server's folder listing names one);
`filing_note`
appears whenever it is set. An account left at the defaults renders as it
did before the block existed. The example above is one shape for an
operator's inbox: with `delivery: drafts` and no `smtp`, nothing the
model writes there is sent. Its draft gate is relaxed by default, so a
draft may go to anyone a person could answer, a `known` or unmatched
address included, and waits in the operator's drafts folder, under
their `default_from` and in their voice, for them to review and send;
an automated mailbox, a failed lookup, and a denied domain are still
refused. Add `draft_gate: strict` under `policy` to keep the full trust
gate there.

### Passes: wake_loop and review_loop

```yaml
      mailbox:
        owner: operator
        wake_loop: email-owner-triage     # the default for an operator mailbox
        review_loop: email-draft-review   # unset (the default) means no review pass
        review_delay: 15m                 # the default
        review_max_wait: 2h               # the default
```

Each account's new mail wakes one loop, its `wake_loop`, with one event
per message, and an account may also name a `review_loop` that looks
at its mail afterwards. Both must name an event-driven loop definition:
a built-in, a core `loops/` document, or a `loops.definitions` entry.
Startup refuses a name with no definition or with a definition of
another operation, and the error names the account and the key;
`wake_loop` is checked only when polling is on, because nothing else
wakes it. `review_loop` must also differ from `wake_loop`,
which is checked when the config loads.
[Loop Definitions](../reference/loop-definitions.md#built-in-email-loops)
lists the built-ins' specs and how to override one.

`wake_loop` defaults to the built-in `email-owner-triage` on an
operator mailbox and to `email-default-handler` everywhere else.
`email-owner-triage` prefers local models (`local_only: "true"`, a
routing preference, so a cloud model can still take the turn when no
local one can) and does one thing per message: it files obvious spam
from an unmatched sender with `destination_role: junk` (an account
where neither `junk_folder` nor the server names a junk folder keeps
the spam where it is), flags what needs the operator, drafts a plain
answer as the operator where the account has `access: send` and a
drafts folder, always with `draft: true`, hands a message it cannot
judge to the review pass with `email_escalate`, or leaves the message
alone. It reads with `mark_seen: false`, never moves mail anywhere but
junk, and cannot use `email_send`.

Before `wake_loop` existed, operator mailboxes woke
`email-default-handler`. To keep that hands-off behaviour, which only
flags and files spam and neither replies nor drafts, set
`wake_loop: email-default-handler` on the account before upgrading.
Otherwise an operator mailbox with `access: send` and a drafts folder
starts receiving drafts from the triage pass.

`review_loop` is empty by default, which means no second pass, and
`email_escalate` is then refused with the flag to set instead. When it
is set, every draft written on the account in a turn the operator is
not present for, by any loop but the review loop itself, is queued for
the review loop as `draft:<account>:<draft_id>`, and `email_escalate` queues a
message as `message:<account>:<message_id>`. Queueing the same subject
again replaces the item already waiting. Nothing about review is
written to the mailbox or the draft ledger, and there is no review or
approval stage: the queue is the only record that work waits. The
review loop gets `queue_pull`, `queue_ack`, and `queue_defer` over its
own queue when it starts, whether it is the built-in or a loop of your
own. The built-in `email-draft-review` may use cloud models, asks for a
higher quality floor than the triage pass, revises or withdraws queued
drafts for accuracy and tone, and handles escalated messages the way
the triage pass would, with more care. Like the triage pass, it cannot
use `email_send` and drafts every reply.

The review loop is woken only while its queue holds work, never for an
empty one. `review_delay` (default `15m`) is how long work gathers after
it arrives, so a burst becomes one review. `review_max_wait` (default
`2h`) bounds how long a steady stream can put that wake off, and may
not be shorter than `review_delay`. Both are Go durations, and a
negative one is refused. The loop is also woken at startup for work
queued before a restart, and again when work its last wake announced,
such as a batch larger than one pull or an item it deferred, is still
queued `review_delay` after that wake, though never sooner than a
minute after it. That recheck runs on its own timer, whether or not
mail is polled. Work queued since the last wake is left to its own
`review_delay` and `review_max_wait`, so a recheck never cuts a burst
short. A wake that cannot read the queue within 30 seconds is logged
and tried again a minute later, whatever the `review_delay`. Accounts
sharing a review loop share one wake, shaped by the shortest delay and
wait among them.

After every poll the poller also reconciles the draft ledger of each
account with an open Thane draft whose poll succeeded, bounded to 10
seconds per account, so a draft the operator sent, edited, or
discarded closes within one poll, and it recounts the review queues.
The ledger is locked per account, so a slow drafts folder holds
drafting on its own account only.

The built-in passes are registered only when an account routes to
them, and a core `loops/` document or `loops.definitions` entry of the
same name replaces one. The model sees `wake_loop` in the account's
Email Accounts entry only when it differs from the owner's default.
Whenever a review loop is set, it sees `review_loop` with
`pending_review`, the count of the account's queued review work, and
`pending_review_as_of`, when that was counted at the last poll or
enqueue. Rendering the entry never counts the queue.

`poll_interval` is how often, in seconds, every account's INBOX is checked
for new mail; it defaults to 300 when email is configured and `0` disables
polling, which also removes the built-in `email-poller`,
`email-default-handler`, and `email-owner-triage` loops. With polling
off, no new mail is routed and `wake_loop` is not checked. An
account's `review_loop` still receives drafts and escalations and is
still woken for them, so it is still checked at startup, and the
built-in `email-draft-review` is still added when an account names
it. Its recheck does not depend on polling, so work a wake left queued
still wakes it again. See
[Event Sources](../reference/event-sources.md) for what a new-mail wake
carries. A loop that should only ever see one mailbox binds it with
`bindings: {email_account: <name>}`; see
[Loop Definitions](../reference/loop-definitions.md).

### Labels

```yaml
email:
  labels:
    contact:
      meaning: "The sender matches a contact record"
      keyword: thane-contact
      color: blue
      apply: contact_matched
```

`labels` is a small vocabulary of marks the operator reads in their
own mail client, declared once for the site and carried by the accounts
whose `mailbox.labels` names them (below). A label is a meaning first.
Its key is the name the model uses (lowercase letters, digits, `-`, and
`_`, starting with a letter, at most 32 bytes), and `meaning`, which is
required, is one sentence of at most 200 bytes that the model reads
beside it. How a label shows on a message is presentation, and each
label needs at least one of two:

- `keyword` is an IMAP keyword such as `thane-contact`: printable ASCII
  without spaces or any of `( ) { % * " \ ]`, at most 64 bytes, and
  unique among the labels regardless of case. System flags (anything
  beginning with `\`), anything beginning with `$`, which marks the
  keywords clients and servers already give meaning to (`$Junk`,
  `$Forwarded`, and the colour keywords `$MailFlagBit0` to
  `$MailFlagBit2` among them), and the junk-filter keywords `Junk`,
  `NonJunk`, and `NotJunk` are refused, so a label never tells a client
  something it did not mean. Any client can search for a keyword;
  whether it displays one depends on the client.
- `color` is a flag colour, one of `red`, `orange`, `yellow`, `green`,
  `blue`, `purple`, or `grey`, and each colour may belong to only one
  label, because a message shows one. It is written as `\Flagged` plus
  the colour keywords in the table below. A client that colours flags
  shows the colour; every other client shows a plain flag.

`apply` names a rule under which Thane applies the label itself, so the
model never applies or removes it. The only rule is `contact_matched`:
when the poller finds new mail in an account's INBOX whose sender
matches exactly one contact record, at any trust zone, it applies the
label before dispatching the message's wake. The contact directory
decides, not the model. A label without `apply` is one the model may
apply and remove with `email_mark`'s `label` argument. At most 8 labels
may be declared, because every one is shown in every turn that carries
the `email` tag. A missing meaning, a label with neither keyword nor
colour, an invalid keyword, an unknown colour or rule, and a colour or
keyword two labels share are refused at startup. Without `labels`, the
default, Thane writes no label anywhere.

An account carries only the labels its `mailbox.labels` names, whatever
its `owner`:

```yaml
email:
  accounts:
    - name: personal
      mailbox:
        owner: operator
        labels: [contact]
```

An account without `mailbox.labels`, the default, carries none: Thane
writes no label there, its entry lists none, and `email_mark` and
`email_search` refuse a label on it. Every name must be declared under
`email.labels`, and an account whose `access` is `read`, which Thane
never writes, may name none. Both are refused at startup.

Every write adds or removes named flags (`+FLAGS` or `-FLAGS`) and
never replaces a message's flags, and nothing marks mail seen. A colour
is written only on a message without `\Flagged`: Thane adds `\Flagged`
with the colour's keywords and removes any other colour keyword the
message carries. A message already flagged, by the operator or for
another label, keeps its flag and colour: a derived label still adds
its keyword there, and `email_mark` refuses a label with a colour on
it. The poller reads each message's flags just before writing it, and
applies a derived label to a message at most once, so a mark the
operator takes off stays off if the message is listed again. A failed
write is logged and costs that message its label, never the poll or its
wakes.

Thane records what it set on each message (the keywords, the colour and
its keywords, whether it set `\Flagged`, the derived labels the poller
has applied, and the copy it marked: the folder, the folder's
UIDVALIDITY, and the UID) in the operational state store, namespace
`email_labels`, one record per account and Message-ID under a SHA-256
key of the two, for 90 days after the last change. The record repeats
the account and Message-ID, and one naming another message is never
read as this one's. That record is the only thing that tells Thane's
marks from the operator's. Removing a label touches only what it
records, and a flag counts as Thane's only while it still carries
exactly the colour Thane wrote, so a flag the operator recoloured or
took off is theirs from then on; whenever Thane reads a message beside
its record, it drops what the operator has taken back.

What the record claims holds only on the copy Thane marked. Every other
copy is the operator's: a second copy under the same Message-ID, such
as a mailing list's copy beside a direct one or a byte-identical
duplicate, and the same message once someone else moves it, since a
move gives it a new UID. Thane writes no label on such a copy, claims
nothing there, and never removes its marks, so a message the operator
moves keeps Thane's marks as the operator's own. When `email_move`
moves a message Thane marked and the server reports the new UIDs
(COPYUID), the record follows the message to its new copy; without
COPYUID the claim lapses the same way, and so it does when the same
call moves another copy under the same Message-ID, because the IMAP
library sorts the COPYUID sets and loses which new UID belongs to which
copy. The wake's `flags` leave out
what the record claims whether or not the account still carries the
label, so taking a label off an account does not make the marks Thane
already set read as someone's flag.

When the model flags a message for the operator
(`email_mark` flag `flagged`) and its flag is still the one Thane wrote,
Thane removes that colour's keywords, so the flag loses the label's
colour and reads as the operator's attention flag; the keyword stays.
When the model removes `flagged` from such a message, the colour's
keywords go with the flag. Once a record expires, Thane treats the
marks as the operator's and leaves them in place. A message without a
Message-ID gets no label.

Thane knows only what it reads. A flag the operator takes off and later
sets again in the same colour, with no read by Thane in between, still
counts as Thane's, and a flag the operator sets in the moment between
Thane reading a message and writing its colour takes Thane's colour.
Closing both gaps would need the server's change sequence numbers
(CONDSTORE), which Thane does not use.

`email_search` finds a label by `label` when the label has a keyword
and the account carries it, and its `flagged` and `unflagged` see a
label's flag like any other. On an account whose labels include a
colour, `flagged: true` therefore returns label flags beside attention
flags. The model tells them apart by each row's `flag_label`, which
Thane fills from its record: it names the label only when Thane wrote
that flag, so an operator's flag in a label's colour still reads as
theirs.

Whether a label sticks is the server's call. Every read-write SELECT
reports the folder's `PERMANENTFLAGS`, which Thane reads as `permanent`
(the list includes `\*`, so new keywords stay), `session_only` (a list
without `\*`), or `unsupported` (an empty or absent list, which Thane
takes as keeping nothing rather than as keeping everything). Anywhere
but `permanent`, Thane writes no keywords, colour keywords included,
and logs that once per account and folder, and `email_mark` refuses a
label that needs them; a label that is only a red colour needs none.

The model sees the labels an account carries in its entry, as `labels`
`[{label, meaning, shows_as, apply}]`, where `shows_as` reads like "blue
flag and keyword thane-contact", and INBOX's verdict as `keywords` once
a read-write SELECT of INBOX has reported it. List, search, and read
results name the labels a message carries and, as `flag_label`, the
label whose flag Thane wrote on it, beside the raw flags. A new-mail
wake's `flags` never include a mark Thane set. An account that carries
no label renders none of this. The built-in passes and the default
handler are told how to read label flags only when some account
carries a label with `apply`.

#### One client's presentation: flag colours

The colour keywords follow the encoding Apple Mail on macOS and iOS
uses, and Apple Mail shows only `\Flagged` and its colour, not
arbitrary keywords. For an operator who triages there, the colour is
the label and the keyword is only for searching. Other clients show the
same messages as flagged, and a client that displays keywords shows the
keyword.

| `color` | Written as `\Flagged` plus |
|---|---|
| `red` | no colour keyword |
| `orange` | `$MailFlagBit0` |
| `yellow` | `$MailFlagBit1` |
| `green` | `$MailFlagBit0` and `$MailFlagBit1` |
| `blue` | `$MailFlagBit2` |
| `purple` | `$MailFlagBit0` and `$MailFlagBit2` |
| `grey` | `$MailFlagBit1` and `$MailFlagBit2` |

Avoid `red` for a label on such a client: a red flag is also how the
operator's own plain flag reads, and it is what a label's flag becomes
when the model flags the message for the operator.

## Signal Messaging

```yaml
signal:
  enabled: true
  socket: /var/run/signal-cli/socket
```

Optional. Requires [signal-cli](https://github.com/AsamK/signal-cli)
running as a daemon with JSON-RPC over Unix socket.

## Contacts & CardDAV

```yaml
carddav:
  enabled: true
  listen:
    - 127.0.0.1:8843
  username: thane
  password: your_password
```

The contact directory is always active. The CardDAV server is optional —
enable it to sync contacts with macOS/iOS/Thunderbird.

Directory hygiene: email reads an automated-looking address at `known` at
most, whatever zone its record holds, and the email trust gate refuses
mail to it. An address is automated-looking when its local part,
lower-cased, cut at any `+` tag, and split at `.`, `-`, and `_`, spells
`noreply` or `donotreply` across whole segments (`no-reply`,
`aws-noreply`, `do-not-reply`), has a `notification`, `notifications`,
`bounce`, or `bounces` segment (`calendar-notification`), or joins to
exactly `mailerdaemon` (`mailer-daemon`, `MAILER_DAEMON`,
`mailer.daemon`, and `mailer-daemon+tag`, but not
`mailer-daemon-reports`). The display name and the domain are
never read, `postmaster` is not automated, and there is no per-record
override. When email polling is on (email is configured and
`poll_interval` is not `0`), every active `admin`, `household`, or
`trusted` record holding such an address is reported: one Warn per
record and address at startup, at most 20 followed by one summary Warn
with the total, and a `contact_directory` row in `system_health` that stays
degraded until the directory is fixed, counting every such record and naming
those that fit in the row's five named findings after any fork findings
(described below), which can be none. The row re-reads the directory on
every render, so a fix clears it without a restart. The fix stays in the operator's custody: demote a record that
only sends notifications to `known`, or move the address to its own
`known` record, through CardDAV (`X-THANE-TRUST-ZONE`) or a full-record
`PUT /v1/contacts/{id}`. While the address stays on the higher record,
the row stays degraded.

The same `contact_directory` row and startup Warns also carry the fork
audit, which is not gated on email polling: it runs whenever the contact
store exists. Every finding involves a record above `known` or the
operator's own. Records that share a name (a formatted name or nickname,
or one record's whole formatted name that is another's given name or the
first word of its formatted name) are a `name` finding when they look
like one person: they share an address or number, the one with no more
authority holds no real address or number of its own, or one is a
`known` record bound to a Home Assistant person. A `name` finding lists
only the records that evidence ties together, and a copy (a `known`
record, or one with no real address or number of its own) that could be
either of two people is listed with each. Different
people who each answer to one formatted name or nickname, with no such
sign between them, are one `shared_name` finding that names one record
per person, since a lookup reaches only one of them. An email
address held by several records is a finding when a `known` record holds
it or a holder has no other address of its own; a mailbox that records
with authority share while each holds its own addresses is not. A phone
number is a finding when several records hold it as `IMPP` `signal:` or
as a `TEL` whose `TYPE` is empty or names a mobile phone (`cell`,
`mobile`, `iphone`), with or without `+`; a line typed `home`, `work`,
`main`, or the like is not. An email address at a reserved domain
(`example.com`, `example.net`, `example.org`, or a name under one of them,
or the `example`, `test`, `invalid`, and `localhost` top-level domains) on
a record above `known` or the operator's own is a finding too, since no
mailbox exists there. Each finding names its records with zone and UUID
and marks the operator's own and any bound to a Home Assistant person.
The row names up to five findings in all, fork findings first and
automated-address findings in what remains, and startup logs one Warn per
finding, at most 20 followed by one summary Warn.

The fixes are the operator's, through CardDAV or `/v1/contacts`: merge a
duplicate into the record that should keep the name or address, rename
one of two different people who answer to one name, move a shared address
to the record that owns it, and replace or remove a placeholder. Thane can
itself forget a `known` duplicate that is neither the operator's nor bound
to a Home Assistant person, and copy a duplicate's addresses onto a record
above `known` only in the operator's own message; no model-facing tool
renames a contact, removes an address, or moves a person binding. Until
the fix, a name lookup prefers the record with authority when both answer
to the name as a formatted name or nickname, though a first name still
reaches a `known` record whose whole name it is, and
`contact_dossier_write` will not start a second dossier for a name sibling
that looks like the same person and has one (see
[Contact Identity Custody](../understanding/trust-architecture.md#contact-identity-custody)).

## Companion Apps

```yaml
companion:
  enabled: true
  providers:
    alice:
      tokens:
        - your-shared-token
      contact: 0d1f8a6e-4c2b-4b7e-9f00-3a7d0e2c9b41
```

Optional. Companion apps connect inward to Thane and expose local host
capabilities, such as macOS Calendar access from
[thane-agent-macos](https://github.com/nugget/thane-agent-macos).

`contact` (optional) binds every device that authenticates through the
account to one contact record, by contact UUID — the counterparty
layer's person attribution. Devices then present and report as that
contact's devices, and inherit authority from the contact's trust zone
at read time, so a zone change reaches every bound device immediately.
Configuration is deliberately the only place this binding can be made:
bindings confer inherited trust, and neither they nor trust zones are
writable through model-facing contact tools. The addresses and numbers,
the notification routing facts (`notification_preference` and
`ha_companion_app`), and the nickname of a contact above `known` or of the
operator's contact are custody too: the operator sets them through CardDAV
or `/v1/contacts`, and `contact_save` adds or changes one only in the
operator's own message (see
[Contact Identity Custody](../understanding/trust-architecture.md#contact-identity-custody)).
Delivery reads each routing fact in any letter case, so a card line
`HA_COMPANION_APP:mobile_app_bob_pixel` routes, and uses only its first
value, so replacing a device means removing the old line. No model-facing
tool gives another contact a name or nickname such a contact already goes
by; sharing one on purpose is a card edit.
A malformed (non-UUID)
value is rejected at config load; a UUID that matches no contact fails
closed at render time — the devices degrade to account-only
attribution and a warning names the unresolved binding.

The second counterparty edge binds a contact to its Home Assistant
person entity, through which zone state and separately resolved,
provider-attributed room presence flow into that contact's channel
context. Signed config is the preferred source of truth:

```yaml
identity:
  operator_contact_id: 019c76e4-2ff1-7918-8d6f-6c2488f5098d

person:
  contact_bindings:
    019c76e4-2ff1-7918-8d6f-6c2488f5098d: person.nugget
```

`identity.operator_contact_id` says which stable contact is the human
operator; it does not infer that authority from presence. Because that
contact's addresses carry the operator's authority, model-facing contact
tools never forget it, `contact_import_vcf` never adds an address, number,
or notification routing fact to it or fills in its nickname, and
`contact_save` adds one, or changes its nickname, only in the operator's own
message, at any zone. The legacy
`identity.owner_contact_name` selector remains accepted for existing
installations, but it is mutually exclusive with the UUID selector.
When neither is configured, Thane falls back to the sole `admin`
contact only when exactly one exists. New `thane init` workspaces create
a UUID in the signed config first, seed an `admin`-zone `Operator` contact
stub with that exact ID, and start with an explicit empty
`person.contact_bindings` map.

`person.contact_bindings` is an exact contact-UUID-to-person-entity map, and
it is what decides the presence roster: a contact is tracked because it
carries an `ha_person_entity` binding. Every referenced contact must exist at
startup, and a person may belong to only one active contact. When
`person.contact_bindings` is present — even as `{}` — startup
atomically reconciles the stored set to config. CardDAV still emits
`X-THANE-HA-PERSON`, but treats it as a read-only projection and preserves
it when clients omit unknown vCard fields. An explicitly present value must
match the projection; attempting to change or clear it is rejected. Removing
a map entry clears that binding on the next restart. A contact with a
configured binding cannot be deleted through CardDAV until its map entry is
removed and Thane restarts; unbound contacts remain deletable.

`person.track` is a startup assertion rather than the roster. Every entity it
lists must be claimed by some contact, or Thane refuses to start and names
the unclaimed ones — which catches a config that has drifted from the contact
graph instead of quietly shrinking the roster and taking the ingest floor,
channel enrichment, UniFi room updates, and the MQTT AP sensors with it. It
may be omitted entirely; the bindings alone are sufficient.

Configs loaded through `-insecure-config` cannot declare these identity
relationships. Thane ignores `person.contact_bindings`,
`identity.operator_contact_id`, and the legacy
`identity.owner_contact_name`: it does not reconcile stored bindings or grant
operator authority from unsigned input, and CardDAV retains its legacy
authenticated custody mode for bindings during recovery.

For backward compatibility, omitting `person.contact_bindings` entirely
keeps the earlier CardDAV-managed behavior: an authenticated PUT may set
or clear `X-THANE-HA-PERSON`. Model-facing vCard import never honors that
field in either mode.

The person tracker follows each person's Home Assistant `device_trackers`
attribute and adds those linked entities to the system ingestion floor. A
linked tracker contributes room presence only when its entity-registry
platform is `bermuda`, its state is `home`, and its `area` attribute is a
non-empty string. The tracker entity ID remains the stable observation identity;
a non-empty human-readable `scanner` attribute is surfaced as source evidence,
while missing scanner evidence stays empty rather than exposing the tracker ID.
The configured UniFi poller remains a second room provider and uses the AP name
as its evidence. Observations that agree resolve to one room, while disagreement
reports `room_conflict` and withholds a guessed room.
Use `companion:`. A top-level `platform:` section is rejected at config
load with an actionable error and must be renamed to `companion:` (the
field shape is unchanged).

## Capability Tags

```yaml
capability_tags:
  project_ops:
    description: "Project-specific research and archive tools"
    include: [archive_search, web_search]
```

Thane ships with a compiled-in capability-tag catalog for native and
provider-discovered tools. Most operators leave built-in tags out of
config. Use `capability_tags` only for deliberate overrides or custom
tags. Tags with `core: true` are always loaded. Others activate
on demand. See [The Agent Loop](../understanding/agent-loop.md).

## Channel Tags

```yaml
channel_tags: {}
```

`channel_tags` pins broad optional capability families for requests from
a source. Use it for coarse source defaults only. Runtime facts such as
current message-channel affordances and owner identity are asserted by
the integrations that know them, not configured here. Do not put
`message_channel` or `owner` in `channel_tags`; Thane skips those
runtime-only tags from source defaults.

## Memory & Storage

```yaml
data_dir: ./db
```

Where SQLite databases live. Defaults to `./db`. For the normal signed config
at `{workspace}/core/config.yaml`, relative paths resolve from the workspace,
so the default is `{workspace}/db` regardless of the directory that launched
Thane. Absolute paths remain unchanged; a config outside a workspace retains
working-directory-relative behavior.

## Document Roots

```yaml
roots:
  kb:
    path: ~/Thane/knowledge
    authoring: managed
    git:
      enabled: true
      sign_commits: true
      verify_signatures: warn
      signing_key: ~/.ssh/id_ed25519
  dossiers:
    authoring: managed
    seed_signers:
      - principal: thane@provenance.local
        key: "ssh-ed25519 AAAA..." # public half of git.signing_key
        label: agent
    git:
      enabled: true
      sign_commits: true
      verify_signatures: required
      signing_key: core:identity/signing_ed25519
  scratchpad:
    path: ~/Thane/scratchpad
    indexing: false
    authoring: managed
```

Each entry under `roots:` names one local collection Thane keeps track
of, combining its `path` with optional per-root policy: whether Thane
indexes it, whether managed document tools may write to it, and whether
writes should go through a signed git history. An entry may be a bare
string when it needs no policy beyond its path:

```yaml
roots:
  kb: ~/Thane/knowledge
```

`core`, `self`, `contacts`, and `dossiers` are reserved roots whose paths
are derived from `workspace.path`. Declare `contacts` or `dossiers` with policy
to enable it, but do not give either a path. They resolve to
`{workspace}/contacts` and `{workspace}/dossiers` respectively. An explicit
`dossiers` path is rejected with a migration recipe instead of being silently
discarded.

> **Deprecated:** the older `paths:` / `doc_roots:` split — one block to
> name the path, a second to attach policy — is still parsed but emits a
> deprecation warning and cannot appear in the same config as `roots:`.
> Migrate each `paths:` entry and its matching `doc_roots:` policy into a
> single `roots:` entry.

`authoring` accepts `managed`, `read_only`, or `restricted`. `managed`
is the default and allows document tools and loop-declared output tools
to write the root. `read_only` blocks managed writes. `restricted`
reserves the root for narrower future flows.

Forge-maintained source checkouts use the same named-root resolver but do
not need a `roots:` entry. Pass a handle such as `repo_root: thanecode` to
`forge_repo_follow`; Thane derives a checkout location beneath
`workspace.path`, clones it before returning, and registers `thanecode:` as
a read-only repository root. The host path remains internal. File tools can
then read `thanecode:internal/app/new.go`, while the scoped `repo_git_*`
tools expose commit history without shell access. Repository roots are not
document corpora: they are neither indexed nor subjected to document
signature policy.

A repository root only stays current while repository polling runs. Set
`forge.subscription_check_interval` to a positive number of seconds. When
polling is disabled the initial clone still succeeds, but the root remains
at that snapshot and the subscription wakes no loop; the tool response
calls this out.

`git.sign_commits` turns each managed document write/delete into a
signed git commit. By default the root itself is the repository; set
`git.repo_path` when several roots live under one larger repo. Thane
uses the repository-local `.allowed_signers` file for SSH signature
verification. Thane creates that file once, when the root is first
established, from the agent key plus the root's declared `seed_signers`;
after that the file is the root's own trust surface and config never
rewrites it.

A root that signs commits must declare `seed_signers` — the keys
entitled to establish it. At boot, a git-backed root under
`verify_signatures: warn` or `required` must prove its birth is
attributable to one of them, and that every change to `.allowed_signers`
was signed by one of them. A root founded by the agent must therefore
declare `thane@provenance.local` with the public half of its
`git.signing_key`; omitting that principal is how a root declares that
the agent may not establish or amend it. Failures report through the
root's own `verify_signatures` policy and name the `admission` check.

`git.verify_signatures` controls read-side enforcement. `none` disables
checks, `warn` logs and reports verification failures without blocking,
and `required` blocks managed document reads, indexed browse/search
results, and tagged context injection unless the content is cleanly
covered by trusted signed git history.

Good uses for custom roots:

- knowledge bases
- scratch work you want to revisit
- generated reports
- custom private note collections
- imported research notes

See [Document Roots](../understanding/document-roots.md) for the fuller
operator guide.

## Persona & Talents

Talents have no config key. They load from `{workspace}/core/talents/`,
a location derived from the workspace rather than authored, so the
prose that steers model behavior every turn is covered by the same
signed history and cleanliness rule as the config beside it. Editing a
talent means committing it: an uncommitted talent file fails the boot
gate exactly as an uncommitted `config.yaml` does, and every talent
must declare `tags:` (`always`, `persona`, or the capability tags that
select it).

A config that still declares the retired `talents_dir` key is rejected
at load with the migration recipe: move the directory into core
(`mv <talents_dir> {workspace}/core/talents`), commit it, and remove
the `talents_dir:` line. An instance that skips the move boots with an
empty talent set and a startup warning naming the missing directory.

See [Context Layers](../understanding/context-layers.md) for how these
fit into the system prompt. The workspace derives the protected
`core` and `self` roots; `core/axioms.md`, `core/persona.md`, and
`core/mission.md` are picked up by the runtime without a separate
inject-file list, as is `self/ego.md` — what Thane has made of them,
read beside them but written under the agent's own signer policy.

## Scheduler

Cron-style task scheduling is managed at runtime with the `task_schedule`
tool rather than in this file. Each task can override the model and routing
hints. See [Event Sources](../reference/event-sources.md) for how scheduled
tasks integrate with the agent loop. Email polling is not a scheduled task;
it is the `email-poller` service loop driven by `email.poll_interval`.

Self-reflection (`ego.md` maintenance) runs as the `ego` service loop,
not as a scheduled task. See the `ego:` block in the example config for
sleep bounds, supervisor randomization, and routing.

## Shell Execution

```yaml
shell_exec:
  enabled: true
  denied_patterns:
    - "rm -rf"
    - "sudo"
  allowed_prefixes:
    - "ls"
    - "cat"
    - "grep"
```

Optional. Controls the `exec` tool. Denied patterns are checked first
(block), then allowed prefixes (permit). If neither matches, the command
is blocked by default.

## Logging

```yaml
logging:
  root: ~/Thane/archive
  level: info
  stdout:
    enabled: true
    level: info
  datasets:
    events:
      enabled: true
    requests:
      enabled: true
    http_access:
      enabled: false
    loops:
      enabled: true
    delegates:
      enabled: true
    envelopes:
      enabled: true
    conversations:
      enabled: false
```

Thane writes append-only JSONL datasets under `logging.root`, partitioned by
dataset/date/hour. `stdout` is a separate operator-facing surface, so high-volume
request and access chatter can be retained on disk without polluting live logs.
`logs.db` remains the query/index layer, but the dataset files are the primary
filesystem record.

For live production monitoring, `just alerts ~/Thane WARN 2` follows new
warnings and errors from `logs.db` across all datasets. Use `ERROR` instead of
`WARN` to suppress warnings. The output is newline-delimited compact JSON and
can be piped through `grep` or `jq`.


## MCP Servers

```yaml
mcp:
  servers:
    - name: github
      transport: stdio
      command: docker
      args: ["run", "-i", "--rm", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN", "ghcr.io/github/github-mcp-server"]
      env:
        - "GITHUB_PERSONAL_ACCESS_TOKEN=your_token"
      include_tools:
        - search_code
        - issue_read
```

Optional. Extends Thane's capabilities via the Model Context Protocol.
See [Delegation & MCP](../understanding/delegation.md).

## Delegation

```yaml
delegate:
  profiles:
    general:
      tool_timeout: 8m
      max_duration: 15m
    ha:
      tool_timeout: 3m
      max_duration: 5m
```

Controls how delegated tasks are routed.
See [Delegation](../understanding/delegation.md).

## Listen Addresses

```yaml
listen:
  address: "0.0.0.0"
  port: 8080
  auth:
    tokens:
      - label: operator
        token: your-api-token
    session_ttl: 168h

ollama_api:
  enabled: true
  address: ""
  port: 11434
  api_key: ""        # optional bearer token; HA's Ollama integration cannot send one
  allowed_sources: []   # e.g. [192.168.1.0/24, 192.168.1.44]

openai_api:
  enabled: true
  address: ""
  port: 8081
  api_key: ""        # optional bearer token; OpenAI clients send it natively
  allowed_sources: []
```

Network binding for the API servers. `listen:` binds the native Thane
/v1 API and web dashboard (default port 8080). The optional
Ollama-compatible (port 11434) and OpenAI-compatible (port 8081) shims
bind separately under their own blocks. An empty or omitted `address`
binds **all interfaces**, so every host that can reach the machine can
reach the listener; set `address` to `127.0.0.1` to keep a listener
local to the host, for example behind a reverse proxy that terminates
TLS and enforces its own access policy.

`listen.auth` gates the native /v1 API and the console. With at least one
token under `tokens:`, every route requires a credential except the public
few: `/health`, `/v1/version`, `/v1/identity` (evidence published for
clients to pin), the console shell and its assets, the OpenAPI explorer,
the sign-in endpoints, and the companion endpoints that carry their own
credential in-band. API clients send `Authorization: Bearer <token>`.
Companion account tokens (`companion.providers.<account>.tokens`) are
accepted on the same routes and authenticate as the account, so companion
apps that already send them keep working. The console stores no token: it
posts one once to `POST /v1/auth/login` and receives an HttpOnly,
SameSite=Strict session cookie, valid for `session_ttl` (default 168h)
without use and extended on each request; sessions live in memory and end
on restart, costing one sign-in. `thane init` mints an operator token into
`core/config.yaml`, so a new workspace is closed from its first boot. With
no tokens configured the API is open, as it was before this block existed.
Each token is compared as a SHA-256 digest in constant time and the
plaintext is not retained after load; `label` is what logs and the session
endpoint report.

`allowed_sources` says which hosts may speak to a compat shim. With the
list empty or omitted, as it ships, the surface is open to anything that
can reach the port; with entries, a request from any other address is
refused with `403` and a JSON error body before it reaches the routes,
and the refusal is logged at WARN with the surface and the peer. Entries
are CIDR prefixes (`192.168.1.0/24`, `2001:db8::/32`); a bare address
(`192.168.1.44`) is shorthand for the single host. An entry that does not
parse fails `thane validate` and the boot gate, naming the block and the
offending entry, so a typo cannot quietly admit or refuse everyone.

A source is the address on the socket — the peer of the TCP connection,
or of the TLS connection under the front door — and never a forwarded
header, which any client can write and none can be trusted to write
honestly. That address only became visible to Thane when the front door
replaced the reverse proxy; before the cutover every request appeared to
come from the proxy. On a dual-stack listener an IPv4 client may arrive
as an IPv4-mapped IPv6 address, which is unmapped before matching, so
`192.168.1.0/24` covers it and there is no need to write the mapped form.

The list matters most on `ollama_api`, the one surface that cannot ask
for a credential: Home Assistant's Ollama integration sends no bearer
token, so `api_key` is unusable exactly where the surface drives the full
agent loop — tools, memory, delegation — for whoever reaches the port. On
`openai_api`, whose clients do send keys, the list is a second lock; it is
checked before `api_key`, so a key from an address outside the list is
never compared. The native API under `listen:` has no such field: it has
bearer authentication with a pinned public-route set, and `listen.address`
already confines it to a network when a deployment wants that.

Every response carries security headers, and which ones depends on what
the surface serves. All of them send `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, and a
`Permissions-Policy` denying geolocation, camera, and microphone; none of
this is configurable, because there is no deployment that wants less.
Beyond that there are three postures. Data surfaces — the native `/v1`
routes, both compat shims, CardDAV — send `default-src 'none'`, which
says an API response is not a page; with `nosniff` that is what keeps a
stored payload the server never parsed from being read back as script.
The web console sends a policy naming only `'self'` and `'none'`, with no
`'unsafe-inline'` and no `'unsafe-eval'` anywhere in it, which it can
afford because it loads no external origin, evaluates no strings, and
embeds no images. The `/docs` explorer sends the one loosened policy:
its vendored Scalar bundle applies styles at runtime and carries its
icons as `data:` URIs, so `style-src` and `img-src` are relaxed for that
surface alone. Scripts are not relaxed even there. `Strict-Transport-Security`
is not in this set; it is a claim about transport and only the HTTPS
front door sends it.

Every listener refuses state-changing requests (`POST`, `PUT`, `DELETE`,
`PATCH`) that a browser marks as cross-origin, using the `Sec-Fetch-Site`
and `Origin` headers browsers attach and non-browser clients do not. This
closes the blind cross-site request forgery path from a page the operator
happens to have open; it does not restrict `curl`, Home Assistant,
companion apps, or reverse proxies, none of which send those headers.

## HTTPS Front Door

```yaml
tls:
  enabled: true
  https:
    port: 443
    # public_port: 443  # when a packet filter maps 443 to a high port Thane binds
  http:
    port: 80            # redirect only; disabled: true turns it off
  hsts_max_age: 4320h
  hostnames:
    thane.example.net: native
    ollama.example.net: ollama
  client_auth:
    trusted_peer_cas: []   # PEM files, relative to core/
  certmagic:
    ca: https://acme-staging-v02.api.letsencrypt.org/directory   # drop for production
    email: acme@example.net
    agreed: true
    # storage defaults to {workspace}/tls; never inside core/
    dns:
      provider: linode
      propagation_delay: 10m
      propagation_timeout: 15m
      resolvers: [ns1.linode.com, ns2.linode.com]
      settings:
        api_token: your-linode-api-token
```

Optional. Thane terminates HTTPS itself: one TLS listener holds a
publicly trusted certificate for every hostname under `hostnames:` and
routes each to the surface it names (`native` for the /v1 API and
dashboard, `ollama`, or `openai`), a plain-HTTP listener answers with a
permanent redirect and nothing else, and every HTTPS response carries
`Strict-Transport-Security` (`hsts_max_age` sets its lifetime,
`hsts_disabled: true` omits it). A hostname that is not listed is refused
twice over: the handshake ends with the TLS `unrecognized_name` alert,
because the door holds no certificate for that name and says so rather
than presenting one it was never asked about, and a request that arrives
under a listed name but carries an unlisted `Host` header gets `421
Misdirected Request`. Both are logged under `subsystem=tls` with the name
asked for and the peer that asked; a burst of refusals coalesces into one
summary so a client stuck in a retry loop cannot write the log, and a
name too long to be a DNS name is shortened in the record so it cannot
set the record's size either. Ports 80
and 443 need privilege that Thane
should never hold: a supervisor that bound them can hand the sockets down under
the systemd `LISTEN_FDS` contract, named `https` and `http`, and the front
door serves on those instead of binding, logging each adoption at boot;
failing that, bind high ports, redirect the public ones with the OS packet
filter, and set `https.public_port` so the redirect names the port clients
use (see [Deployment](deployment.md#network-requirements)). Hostnames are explicit and lowercase;
wildcards and IP literals are refused, and a hostname routed to a shim
that is not enabled fails validation. The plaintext listeners under
`listen:`, `ollama_api:`, and `openai_api:` are unaffected; the front door
is a second way in, not a replacement, until you move them to loopback.

Certificates come from certmagic over the ACME DNS-01 challenge, so
hostnames that resolve only on a private network still get certificates
from a public CA. The `certmagic:` block is a pass-through: its fields
mirror certmagic's own settings in snake_case, so certmagic's and Caddy's
documentation apply directly. `agreed: true` is required. Leave `ca`
unset for Let's Encrypt production; point it at the staging directory
while bringing the front door up beside an existing proxy, since staging
issues untrusted certificates without counting against rate limits.
Account keys and certificates are runtime state stored under
`certmagic.storage`, which is created owner-only and tightened to
owner-only if it already exists wider. Config expands `${HOME}` but not
`~`, so spell the path out or leave it to the default. A path inside
`core/`, including one that reaches it through a symlink, is refused so
key material never enters signed history.

`dns.provider` selects a compiled-in libdns provider; `linode` is the
first. `dns.settings` is passed to the provider verbatim with the
provider's own field names, so for Linode it takes `api_token` and
optionally `api_url` and `api_version`; a misspelled key is rejected at
load. Propagation is the knob that matters in practice: Linode's
authoritative nameservers pick up API-created records on a schedule
rather than immediately, and the challenge fails if the CA looks before
they have. `propagation_delay` is how long to wait before checking at
all, `propagation_timeout` how long to keep checking after that, and
`resolvers` which servers to ask; pointing them at the zone's
authoritative nameservers avoids waiting on recursive caches. When both
durations are zero the provider's registry defaults apply, ten and
fifteen minutes for Linode; `propagation_timeout: -1` disables the checks
so issuance proceeds as soon as the delay elapses. `cert_obtain_timeout`,
if set, must exceed their sum or issuance is cut off mid-wait. Hostnames
must be fully qualified, lowercase DNS names; `thane validate` refuses
anything the CA would.

Issuance runs in the background after boot: the listeners come up
immediately and a handshake for a hostname whose certificate has not
arrived fails until it does, which with a ten-minute propagation wait is
the honest behaviour. That failure is deliberately not the one an
unlisted hostname gets: a listed name without a certificate yet is an
internal condition and ends the handshake as `internal_error`, so the two
are told apart from the client side rather than sharing one alert that
makes a misdirected client look like a broken server. Issuance, renewal, and failure are logged under
`subsystem=tls`; `thane validate` checks the provider, its settings, the
storage directory, and every client CA without touching the network.

`client_auth` is on by default: the listener requests a client
certificate, verifies any it is given against the instance's channel CA
(`core/ca/channel_root.crt`) plus any `trusted_peer_cas`, and attaches
the verified identity to the request as a principal. Nothing is refused
for lack of a certificate yet; the principal is the seam later
authentication layers read. `disabled: true` ignores client
certificates entirely.
