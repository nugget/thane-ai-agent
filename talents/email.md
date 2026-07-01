---
name: email
tags: [email]
kind: trailhead
teaser: "Open for inbox work — triaging what arrived, drafting a response, or organizing what's there."
next_tags: [email_triage, email_respond, email_organize]
---

# Email

Email is the channel where remembered shapes are most likely to lead
you wrong — a "reply to that thread from last week" that was actually
two months ago, a recipient whose address you half-remember, a folder
name that's a guess. Fetch the concrete state before composing anything
that goes out.

## The single most important disambiguation

**Inbound work is `email`. Outbound notifications are `notifications`.**
A turn that's "respond to this email I got" is email; a turn that's
"tell the user about something happening in the system" is
notifications. Both can produce a message-shaped artifact, but the
audiences and trust models are different.

| You want... | Surface |
|---|---|
| Find, read, respond to mail that arrived in your inbox | Activate `email`, then pick a leaf below |
| Send a non-correspondence notification to the user | Activate `notifications` (which routes via push, etc.) |
| Look up a sender's history across past conversations | `archive_text`, scoped to the conversation if relevant |
| Resolve a name to an email address | `contacts` — recipient validation depends on it (see below) |

## Choose by the shape of your question

- **You want to see what's in the inbox** — activate `email_triage`.
  Folders, list, search, read. Read-only; safe to cast a wide net.

- **You want to send or reply to mail** — activate `email_respond`.
  Compose, reply, the trust-zone gating that protects against
  accidental sends to strangers.

- **You want to move mail around the folder structure** — activate
  `email_organize`. Mark as read/flagged, move between folders. UIDs
  are folder-scoped; this is where that bites.

## Constants across all branches

- **Recipients must be in the contact directory AND at a
  send-eligible trust zone.** Both `email_send` and `email_reply`
  route through a trust-zone gate that aborts the send on *any*
  issue. Addresses resolved to `admin` / `household` / `trusted`
  zones go through; `known` zone is **rejected** with a
  "promote-or-authorize" message; addresses with no contact record
  are **rejected** with a "no contact record" message. The two
  failure modes are distinguishable in the result so you know
  whether to promote an existing contact or save a new one, but
  both leave the message unsent. Confirm contacts exist *and are at
  a send-eligible zone* via `contact_lookup` before composing — the
  rejection after you've drafted the body is annoying and avoidable.
- **Sent mail is irreversible.** There is no "unsend." A draft sent to
  the wrong audience is permanent. When uncertain about the recipient
  list or the body's tone, draft into the conversation first and ask;
  don't reach for `email_send` as an optimistic move.
- **UIDs are folder-scoped.** A UID from `email_list(folder: "INBOX")`
  identifies a message *within INBOX*. After `email_move` to Archive,
  the message has a new UID in Archive; the old INBOX UID stops
  resolving. Re-list after moving if you need to operate on the
  moved messages.
- **Accounts are configured per-host.** Most calls accept an `account`
  parameter; omitting it uses the primary account. Multi-account hosts
  should pass `account` explicitly to avoid silent misrouting.

## Cross-references

- For grounding sender and recipient names in real records, bounce to
  `contacts` (`contact_lookup`). Required reading before `email_send`
  unless you're certain every recipient is already in the directory.
- For high-volume triage loops (digest every morning, watch for
  specific senders), the right shape is usually `thane_loop_create`
  with `operation=service` rather than a synchronous email turn. The
  managed output document is optional, so a triage loop can run without
  maintaining one. See `loops_examples_curate` for the pattern.
- For escalation when an email needs human attention (sensitive thread,
  legal/financial content), bounce to `notifications` —
  `request_human_decision` with the email summary in the body.

---
name: email_triage
tags: [email_triage]
kind: trailhead
teaser: "Read-only inbox work — folders, list, search, read by UID."
---

# Triage

Reading the inbox. Four tools, picked by how specifically you can name
what you're looking for.

## Survey the folder structure

`email_folders` enumerates mailboxes with message and unseen counts:

```json
{
  "account": "primary"
}
```

Useful when you don't know whether the host's archive lives in
`Archive`, `[Gmail]/All Mail`, `Saved`, or somewhere else. Pick the
folder name from the result; don't guess.

## List recent messages

`email_list` returns recent messages newest-first with sender, subject,
date, and flags:

```json
{
  "folder": "INBOX",
  "limit": 20,
  "unseen": true
}
```

`unseen: true` is the right move when triaging — read what you haven't
read, skip what you have. The UIDs in the result are what you'll feed
to `email_read`, `email_mark`, or `email_move` next.

## Search across content

`email_search` runs text + header + date-range queries:

```json
{
  "query": "VLAN renumber",
  "from": "nugget",
  "since": "2026-04-01",
  "folder": "INBOX",
  "limit": 30
}
```

All filters are optional; combine the ones you have. `since`/`before`
take `YYYY-MM-DD`. Searches return newest-first like list does.

## Read one in full

Once a UID looks worth reading, pull the body with `email_read`:

```json
{
  "uid": 4827,
  "folder": "INBOX"
}
```

Returns full headers and body. The UID **must** match the folder it
was listed from — `email_list(folder: "INBOX")` UIDs only resolve via
`email_read(folder: "INBOX")`. Cross-folder UID confusion is a common
silent failure.

## Cross-references

- For "what was said about this topic across conversations *and*
  emails" (where the email is a hint, not the answer), bounce to
  `archive_text` for the conversation side after reading the email
  here.
- For drafting a response after reading, bounce to `email_respond`.
  Don't reach for `email_reply` from this branch's tools — re-activate
  email_respond first so its safety doctrine loads.
- For moving a message after deciding what to do with it, bounce to
  `email_organize`.

---
name: email_respond
tags: [email_respond]
kind: trailhead
teaser: "Compose a new email or reply to an existing one — trust-gated by the contact directory."
---

# Respond

Sending mail. Two tools, one safety surface that dwarfs both: **every
recipient must be in the contact directory.**

## The trust-gated send

`email_send` composes a new thread:

```json
{
  "to": ["nugget@macnugget.org"],
  "cc": [],
  "subject": "VLAN renumber — rollback note",
  "body": "Hi,\n\nThe rollback worked cleanly. Logs attached in the next message.\n\n— Thane"
}
```

The body is markdown; the server converts to both `text/plain` and
`text/html`. Subject is required. The handler validates each `to` /
`cc` address against the contact directory before sending. **Any
trust-gate issue aborts the whole send** — there is no "send the
allowed ones and skip the others." Three result categories:

- **Allowed through** — every recipient resolves to `admin`,
  `household`, or `trusted`. The mail goes out.
- **Rejected, known-zone recipient** — at least one recipient is at
  the `known` trust zone. Result names the offender; nothing is
  sent. Recovery: promote the contact with `contact_save`
  (deliberately, with user authorization), or remove them from the
  recipient list.
- **Rejected, missing contact** — at least one recipient has no
  contact record. Result names the offender; nothing is sent.
  Recovery: `contact_save` to add the contact deliberately, or
  remove them from the recipient list.

When the result reports a rejection, the right move is usually
`contact_lookup` to confirm what's actually in the directory (maybe
the spelling differs, or an alias resolves elsewhere), then either
`contact_save` to add or promote, or revise the recipient list.
**Don't blanket-add contacts just to unblock a send** — the trust
gate exists precisely to make that decision conscious. A recipient
who's `known` rather than `trusted` is information about the
relationship; promoting them is a real trust-policy choice.

## The threaded reply

`email_reply` preserves `In-Reply-To` and `References` so the reply
threads properly in the recipient's client:

```json
{
  "uid": 4827,
  "folder": "INBOX",
  "body": "Confirmed — applying the change tonight.\n\n— Thane",
  "reply_all": false
}
```

`reply_all: false` (the default) replies only to the original sender.
`reply_all: true` includes the original `Cc` list. **Both paths still
go through the trust gate**, and the gate's all-or-nothing behavior
means a reply_all to a thread where any CC is at `known` zone or has
no contact record will be **rejected entirely** — the handler doesn't
selectively drop bad recipients and send to the rest. When reply_all
matters, do a `contact_lookup` sweep over the visible recipients
first.

## reply vs send — the right shape

Reply when the recipient is expecting your response in the existing
thread. Send when starting a fresh conversation, when the existing
thread is the wrong forum, or when the original audience is the wrong
audience for this message. Threading wrongly is a small annoyance;
audience-wrong is a real leak.

## Cross-references

- For looking up the right address before composing, bounce to
  `contacts` (`contact_lookup`). The trust gate's "Cannot send to X:
  no contact record" message is recoverable, but it's faster to know
  the directory state going in.
- For high-stakes outgoing mail (sensitive, legal, ambiguous tone),
  draft the body in a `scratchpad:` doc and ask for owner sign-off
  via `request_human_decision` before sending.
- For the loop shape that reads incoming mail and decides whether to
  reply, see `loops_examples_curate` — a `thane_loop_create` with
  `operation=service` is the right vehicle when "every morning"
  matters.

---
name: email_organize
tags: [email_organize]
kind: trailhead
teaser: "Mark messages read/flagged or move them between folders — UIDs are folder-scoped."
---

# Organize

Curating the inbox structure. Two tools, both UID-driven, both can
operate on arrays for bulk work.

## Mark messages

`email_mark` adds or removes a flag (`seen`, `flagged`, `answered`):

```json
{
  "uids": [4827, 4828, 4829],
  "flag": "seen",
  "add": true,
  "folder": "INBOX"
}
```

`add: true` adds the flag; `add: false` removes it. `add` defaults
to `true` when omitted — the common case is marking seen after a
triage pass, so the schema and handler now agree on that default.
Pass `add: false` to remove a flag. Single-message mode accepts
`uid` (integer) instead of `uids` (array) as a convenience.

The most common reason to reach for this: marking processed messages
as read after a triage pass, so the next pass's `email_list(unseen:
true)` only shows what's new.

## Move messages

`email_move` relocates messages between folders:

```json
{
  "uids": [4827, 4828],
  "folder": "INBOX",
  "destination": "Archive"
}
```

`folder` is the source; `destination` is the target. The handler
accepts a convenience shorthand: if you pass only `folder` and omit
`destination`, the `folder` value is treated as the destination and
INBOX is assumed as the source. Prefer the explicit form for clarity.

## UIDs are folder-scoped — the canonical gotcha

A UID identifies a message *within one folder*. After `email_move`,
the message has a fresh UID in the destination folder; the old UID
in the source folder stops resolving. If you want to operate further
on the moved messages, re-list after the move:

```json
{
  "folder": "Archive",
  "limit": 20
}
```

The bulk path that bites: moving 20 messages, then trying to
`email_mark` them with the old INBOX UIDs. Silent failure or wrong
messages flagged. Always re-list after a move when further operations
are coming.

## Cross-references

- For finding the messages to organize first, bounce to `email_triage`
  — list or search produces the UIDs you'll feed here.
- For automating the organize step (every morning archive read mail),
  this is service-loop territory — `thane_loop_create` with
  `operation=service`; see `loops_examples_curate`.
- For deleting rather than archiving, the move pattern still applies
  — `destination: "Trash"` is the conventional target. Trash retention
  policy is server-side, not Thane-managed.
