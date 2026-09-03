---
name: session
tags: [session]
teaser: "Open for conversation-lifecycle decisions — reset, close, checkpoint, split, or pin this conversation to one model."
---

# Session

Five tools, each genuinely distinct. Four shape the conversation's
lifecycle; the names sound similar enough that the model demonstrably
mis-routes between them, and the differences matter because some are
destructive and some aren't, some preserve continuity and some sever
it. The fifth, `conversation_model_pin`, shapes *which mind answers*
this conversation and touches no history at all.

## The single most important disambiguation

**`session` is about lifecycle ops on the *current* conversation.
`archive` is about reading *past* conversations.** The two share the
word "session" (one of archive's tools is `archive_session`), but
they operate at opposite ends of time:

| You want... | Surface |
|---|---|
| Reset, close, checkpoint, or split the conversation you're in | `session` — this leaf |
| Search or read a past session that's already closed | `archive` (`archive_session_transcript`, `archive_search`) |
| Carry-forward summary written into the next conversation | `session_close` with `carry_forward` (covered below) |
| The texture/tone/arc of the current conversation, not its structure | `session_working_memory` (see [`working-memory.md`](working-memory.md)) — that's a memory tool, not a session lifecycle one |

A model that lands here looking for "what did we say earlier in this
conversation" is on the wrong leaf entirely — the live message
history is already in your prompt, and older content (when it
exists) lives in archive.

## The destructive / non-destructive split

This is the cleanest mental model. Two of the four operations end the
current session; two do not.

| Operation | What it does | Current session ends? |
|---|---|---|
| `conversation_reset` | Wipes the conversation. Archives messages but starts blank. | **Yes** (and brutally — no carry-forward) |
| `session_close` | Closes current session, opens a fresh one with a carry-forward handoff injected. | **Yes** (but with continuity) |
| `session_checkpoint` | Snapshots state. Current session continues uninterrupted. | No |
| `session_split` | Archives early messages, keeps recent ones in the current session. | No (the current session keeps going) |
| `conversation_model_pin` | Holds this conversation to one model deployment, or clears the hold. Touches no messages. | No |

When in doubt about destructiveness, look at this table first.

## conversation_reset — the nuclear option

**Only on explicit user request. Never on your own initiative.**

```json
{
  "reason": "user asked to start over"
}
```

The user has to ask: "reset," "clear the conversation," "start over,"
"wipe history." A frustrated user venting is not a request. A
suggestion that things are getting confused is not a request. The
description on the tool itself is explicit: *NEVER call this tool on
your own initiative*. If the loop feels stale or the context feels
crowded, `session_close` or `session_split` are almost always the
right move instead — both preserve at least something, while
conversation_reset preserves nothing model-visible.

Messages are archived (so they remain searchable via `archive_text`
afterward), but the *active conversation* gets a blank slate. There
is no carry-forward.

## session_close — graceful transition with continuity

**The right move when the topic is shifting and you want to keep a
thread of context across the break.**

```json
{
  "reason": "topic change — moving from VLAN work to email triage",
  "carry_forward": "Just finished VLAN renumbering on hosts deepslate/glade. Rollback plan filed in kb:network/vlan-renumber.md. Owner approved leaving the lab on the old subnet for another week. Next session is unrelated email work."
}
```

`carry_forward` is **required** — without it, the new session starts
with no prior context and the lifecycle handler warns explicitly.
Write the carry-forward as notes to your future self: key decisions,
open threads, anything the next session would otherwise have to
re-derive. The handler injects it into the new session's system
prompt automatically.

Use this when:
- A long conversation is moving to a clearly different topic
- Context has grown stale (long file dumps, exploratory dead-ends)
  and you want a clean prompt without losing the through-line
- A natural milestone closed (the PR landed; the incident resolved)
  and the next phase is conceptually separate

`session_close` is the workhorse of conversation-lifecycle management.
Reach for it more often than for `conversation_reset`.

## session_checkpoint — non-destructive safety net

**Snapshot state, keep going.** Use *before* a risky operation, not
after the fact.

```json
{
  "label": "pre-cutover"
}
```

Cheap to call. The session continues uninterrupted; the archive
captures a recoverable point. The label is for your future self when
you're staring at a list of checkpoints in the archive.

Common moments to checkpoint:
- About to run a destructive shell command, migration, or schema change
- About to make a large mutation (mass-rename, bulk delete) that's
  recoverable in principle but tedious to undo
- The conversation just reached a known-good state and you want to be
  able to come back to *exactly* this point

Don't checkpoint reflexively every turn — that fills the archive with
noise. Checkpoint when there's a specific reason to want this
particular state recoverable.

## session_split — retroactive trim

**Drop the early messages, keep the recent ones in the current
session.** The early messages get archived as a completed session;
the current session continues from the split point onward.

```json
{
  "at_index": -20
}
```

Or, by content:

```json
{
  "at_message": "Now let's switch to the email triage"
}
```

Exactly one of `at_index` or `at_message`. `at_index` is **negative
offset from the end** (`-20` means "20 messages back"); passing a
positive value errors out. `at_message` matches the first message
whose content contains the substring; the split happens before that
message.

Use this when:
- The first half of a conversation explored a dead end and the second
  half is the actual work — you want the model's working context to
  reflect only the productive thread
- Long preamble or context-loading messages are still consuming token
  budget but are no longer needed; trim them out without losing the
  current state of work
- You realize mid-conversation that the productive turn started later
  than the conversation began

Distinguish from `session_close`: split keeps you *in* the current
session (no carry-forward needed, no handoff). It's "compact this
session" rather than "transition to a new session."

## conversation_model_pin — steer which mind answers

**Hold this conversation to one model deployment, from the next turn
on, until cleared or until Thane restarts.** The user asks for it in
model terms: "switch this chat to opus," "use the local model for
now," "go back to automatic."

```json
{
  "model": "claude-opus-4-8",
  "reason": "user wants to compare opus on this thread"
}
```

To clear:

```json
{
  "clear": true
}
```

`model` is a deployment id or a unique model name; the `models` tag's
`model_registry_list` is where those come from, and an unknown or
ambiguous name errors with the next move rather than guessing. The
pin outranks the channel's configured model and any model a client
picked in a dropdown, and it holds across turns, sessions, splits, and
checkpoints of this conversation. Nothing about it is durable: it is
process memory, so a restart drops it. That is the recovery path for a
bad choice, by design.

Three things to hold in mind while a pin stands:

- **It applies from the next turn.** The turn that sets the pin has
  already chosen its model. Say so when you confirm.
- **The Context line tells you the state.** `(pinned -Ns)` after the
  model name means the pin was honored this turn. `pinned X skipped
  this turn: <reason>` means the deployment could not serve this one
  turn (an image arrived and it lacks vision, the prompt outgrew its
  window) and the router answered instead. The pin still stands; do
  not re-pin, and do not apologize for a switch you did not make.
- **It is not policy.** To take a deployment away from *everyone*, or
  promote a discovered one, that is `model_deployment_set_policy`
  under `models`. To pin a persistent loop definition, that is
  `loop_definition_update` under `loops`. This tool is for this
  conversation and this conversation only.

Pin on the user's request or for a deliberate comparison. Do not pin
on your own initiative because a turn felt weak; the router's choice is
explainable (`model_route_explain`) and usually right for a reason.

## Choosing the right one

If you're tempted to reset the conversation, ask: did the user *ask*
for it? If not, the right move is one of the other three:

- The current session is fine but I want a snapshot → `session_checkpoint`
- The topic changed and I want a clean room with a handoff →
  `session_close`
- Old turns are dead weight; recent turns are the work → `session_split`

`conversation_reset` exists for one purpose: honoring an explicit
user request to start over. That's it.

## Cross-references

- For "what was said in the conversation I just closed/split/reset,"
  bounce to `archive` — the archive surfaces past sessions
  searchably regardless of which lifecycle operation produced them.
- For the system-event side of "what happened around the time of the
  session boundary" (loop iterations, tool calls, errors), use
  `logs_query` (always available, no activation needed). Lifecycle
  operations show up in the event stream.
- For session-level memory that should outlive the *current* session
  but doesn't fit in carry-forward prose, `memory` (`remember_fact`)
  is the right home — facts persist across sessions automatically.
