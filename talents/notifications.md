---
name: notifications
tags: [notifications]
kind: trailhead
teaser: "Open for outbound alerts — fire-and-forget, async actionable, sync escalation, or resolving an incoming response."
next_tags: [notifications_send, notifications_ask, notifications_resolve]
---

# Notifications

An outbound alert is you borrowing someone else's attention from the
future. An interruption spends trust as well as tokens. Spend both on
purpose.

## The single most important disambiguation

**Notifications is for *outbound non-correspondence* — push, alert,
escalation, anything where the system is interrupting the user to
say something or ask something.** It's *not* for drafting outbound
mail and it's *not* for in-loop supervisor attention:

| You want to... | Surface |
|---|---|
| Push an alert, ask a decision via buttons, or escalate to a human | `notifications` — this leaf |
| Compose and send an email (correspondence — threaded, addressed, archived) | `email` (`email_send` for new threads, `email_reply` for replies) |
| Get the agent's own supervisor to take a turn (loop-side core attention) | `request_core_attention` (core tool; no activation needed) — that's *the agent* attending, not *the user* |
| Send a Signal message that's conversational, not alert-shaped | `signal` (`signal_send_message`) |

The cleanest test: *who is this message addressed to as a person, and
what response shape am I expecting?* User, alert/decision shape →
notifications. User, conversational/threaded shape → email or signal.
Supervisor/self, attention shape → `request_core_attention`.

## Speak up for

- security concerns and unusual patterns
- events someone explicitly asked to hear about
- critical failures affecting comfort or safety
- time-sensitive situations that actually require action

## Stay silent for

- routine arrivals and departures
- state changes you are merely observing
- status updates that can wait
- anything you would not want to receive at 2am

## Trust these instincts

- treat explicit user notification requests as durable preference
- raise the threshold at night or when the household is likely asleep
- if the event is merely interesting, prefer silence or a less urgent
  channel
- if someone is already in bed, raise the bar again

## When you do reach out

Be brief. One to two sentences. Lead with what happened, then why it
matters, then any action they need to take.

## Choose by the shape of the interaction

The judgment above settles *whether* to notify. The branches below
settle *how*:

- **You're informing, not asking** — activate `notifications_send`.
  Fire-and-forget delivery. The recipient sees the message; you
  continue your turn with no callback.

- **You need a response** — activate `notifications_ask`. The
  critical fork is async (you continue, callback arrives later) vs
  sync (your turn blocks until they respond or timeout). Choosing
  wrong is the most common mis-route at this leaf.

- **A user just replied to an outstanding actionable** — activate
  `notifications_resolve`. This is a callback site, not a
  notification site; you're closing a loop the system already
  opened.

## Constants across all branches

- **Recipients resolve through the contact directory.** The
  *outbound* tools (`send_notification`, `ha_notify`,
  `request_human_decision`, `request_human_escalation`) take a
  `recipient` (contact name) and the system looks up the channel
  from contact facts. `resolve_actionable` is the exception — it
  closes an existing tracked notification by `request_id` /
  `action_id` and has no recipient. Get the contact lookup right
  on the outbound side and the delivery routing is automatic; pass
  a name that doesn't resolve and the send fails before reaching
  the channel.
- **Priority is `low` / `normal` / `urgent`** — but the parameter
  name differs across tools. `send_notification`, `ha_notify`, and
  `request_human_decision` use `priority`. `request_human_escalation`
  uses `urgency`. Same enum, different keys; the misroute the model
  walks into is passing `priority` to `request_human_escalation` and
  getting it silently dropped. Low is passive/FYI (won't break
  through Do Not Disturb on most channels). Normal is the default.
  Urgent bypasses quiet hours. Match the level to the judgment
  doctrine above, not to your enthusiasm about the event.
- **Conversational channels deliver responses asynchronously.** When
  a user replies "yes" in Signal to an outstanding actionable, the
  conversation history annotates the message with `[request_id:
  ...]`. That's the hook for `notifications_resolve`.

## Cross-references

- For recipient resolution, bounce to `contacts` (`contact_lookup`)
  when the contact name isn't certain. Sends to unknown recipients
  fail at the routing layer, not at compose time — the lookup is
  the catch.
- For the loop-side "I need supervisor attention right now" path
  inside a service loop, `request_core_attention` (always available,
  no tag activation needed) is the right tool. It's distinct from
  human escalation: it asks the *agent's* supervisor to take a turn,
  not a human.
- For raising the notification threshold during quiet hours, the
  lens system (`night_quiet`, `everyone_away`) shapes the
  judgment doctrine above without changing the tools. Lenses are
  global posture; notifications are per-event delivery.
- For sustained watching of HA entities that *might* warrant a
  notification later, `awareness` is the right subscription
  surface; this leaf is the egress point once the awareness layer
  decides something deserves attention.

---
name: notifications_send
tags: [notifications_send]
kind: trailhead
teaser: "Fire-and-forget delivery — message goes out, no response tracking."
---

# Send (fire-and-forget)

You're informing, not asking. The recipient sees the message; you
move on without waiting for or expecting a reply.

## Prefer the channel-agnostic tool

`send_notification` is the right default. The notification router
picks the delivery channel from the recipient's contact facts and
recent channel activity — HA push, Signal, and other registered
providers are all possible targets depending on configuration:

```json
{
  "recipient": "nugget",
  "title": "Sump pump cycled",
  "message": "Sump ran 4 times in the last hour. No alarm yet, but worth a glance after the rain stops.",
  "priority": "low"
}
```

`title` is optional but improves the channel rendering when present.
`priority` defaults to `normal`; use `low` for FYI material, `urgent`
only when quiet-hours bypass is actually warranted by the judgment
doctrine in the parent.

## The HA-specific path

`ha_notify` is the lower-level variant — it specifically targets the
Home Assistant companion app, bypassing the channel selector:

```json
{
  "recipient": "nugget",
  "message": "Garage door has been open for 30 minutes.",
  "priority": "normal"
}
```

Reach for it when:
- You explicitly want the HA push channel (e.g., a critical
  HA-originated alert where Signal would be the wrong feel)
- The recipient's contact facts don't yet route through
  `send_notification` (rare; usually a config gap worth fixing)

When the channel doesn't matter, prefer `send_notification` — it
keeps the routing decision in the system, not in your prose.

## Don't reach for actions here

`ha_notify` accepts an `actions` array; with it, the call crosses
the behavior line from fire-and-forget into an actionable
notification with callback routing. `send_notification` does *not*
accept `actions` at all — it is fire-and-forget by construction.
Either way, if you're adding actions you're asking, not informing;
bounce to `notifications_ask` so the doctrine for response
tracking and timeout policy loads.

## Cross-references

- For decision-shaped notifications (actions + callback), bounce to
  `notifications_ask`.
- For "where's the user reading this?", that's a contact-fact
  question — `contacts` (`contact_lookup`) shows the configured
  channels.

---
name: notifications_ask
tags: [notifications_ask]
kind: trailhead
teaser: "Get a decision back — async (callback later) or sync (blocks until they answer)."
---

# Ask (decision-shaped)

You need a response, not just to inform. **First check whether your
question is really shaped like a decision for a specific human at
all** — many "escalate this" instincts inside a service loop are
actually loop-side concerns better routed via
`request_core_attention` (in `loops`, always available; not part
of this leaf). That tool wakes the core/owner loop's next iteration
to review your concern; no actions, no recipient, no wait. It is
the canonical service-loop → operator attention path and the
default escalation shape for metacog, ego, and other internal
loops.

If you genuinely need a decision from a specific contact via
actionable buttons, the leaf has two real tools:

| You need... | Tool | Behavior |
|---|---|---|
| A decision eventually; you can keep working | `request_human_decision` | **Async.** Returns a `request_id`. Callback dispatched to your originating conversation when they answer. |
| A decision *now*; you cannot proceed without it | `request_human_escalation` | **Sync.** Blocks the current turn until they respond or timeout. |

Picking wrong between them is the most consequential mis-route at
this leaf. Async-when-you-should-have-been-sync leaves the turn
proceeding with no decision; sync-when-you-should-have-been-async
wastes a turn-long blocking wait on something you could have
continued past.

For an "ask a frontier AI model for judgment inline" pattern,
spawn a delegate with `thane_now` and a premium routing profile
— that returns the answer inline, same shape, with a real
handler behind it.

## The async path

`request_human_decision` posts an actionable notification and returns
immediately with a `request_id`:

```json
{
  "recipient": "nugget",
  "message": "The cron PR is ready to merge. Tests pass; one Copilot comment is unresolved (about edge-case wording). Merge anyway?",
  "actions": [
    {"id": "merge", "label": "Merge"},
    {"id": "wait", "label": "Wait — let me look"},
    {"id": "cancel", "label": "Don't merge"}
  ],
  "timeout": "2h",
  "context": "PR #934, last commit a83f12d"
}
```

The `context` field is stored with the record and surfaced to the
callback handler so the future-self picking up the response knows
what it was about. `timeout` defaults to 30m; `timeout_action` can
auto-execute an action ID on timeout, "escalate" to re-send urgent,
or "cancel" (the default).

When the user answers (in HA push or by replying conversationally in
Signal/another channel), the callback dispatches to your originating
conversation. You don't have to do anything to wait — the system
brings the response back to you.

## The sync path

`request_human_escalation` posts the same kind of question but
**blocks the current turn** until the human responds or the timeout
expires:

```json
{
  "recipient": "nugget",
  "question": "Production DB migration is staged. Confirm to run, or hold for review?",
  "context": "Affects 50M rows. Tested in staging; rollback plan in kb:db/2026-05-migration.md.",
  "actions": [
    {"id": "run", "label": "Run it"},
    {"id": "hold", "label": "Hold"}
  ],
  "timeout": "10m",
  "urgency": "urgent"
}
```

Default timeout is 10m (shorter than the 30m on async decisions
because *something* is waiting on the answer). On timeout, the tool
returns a "did not respond" indicator and your turn continues — but
having burned that 10m on hold.

The synchronous behavior is what makes this distinct from
`request_human_decision`. **Only use sync when the next step in your
turn genuinely depends on the answer** and there's no useful work to
do in parallel. If you could send the question async, keep working,
and pick up the response next turn, async is the right move.

## Cross-references

- For closing the loop after an async actionable gets answered in a
  conversational channel, bounce to `notifications_resolve`.
- For "the user already responded; how does that flow back to me" —
  the callback dispatches automatically into your originating
  conversation; no explicit fetch needed.
- For grounding the recipient in real contact data, bounce to
  `contacts` (`contact_lookup`) — sending to an unknown name fails
  at routing time.

---
name: notifications_resolve
tags: [notifications_resolve]
kind: trailhead
teaser: "Handle an incoming user reply to an outstanding actionable — callback site, not a notification site."
---

# Resolve (handle an incoming response)

This leaf is the shape-inverse of `notifications_ask` — instead of
sending a question and waiting, you're receiving a reply that
answers one. It's a callback site, and it only makes sense when
there's an outstanding actionable notification waiting to be
resolved.

## When this fires

A user replies in a conversational channel (Signal is the canonical
case) to an outstanding actionable. The conversation history shows
the prior outbound notification annotated with `[request_id: ...]`,
and the user's reply names the action they want.

The model's job is to extract the `request_id` from the prior
annotation and the `action_id` from the user's reply, then call
`resolve_actionable`:

```json
{
  "request_id": "01964ea7-7c2e-7d12-9a4b-1b2c3d4e5f6a",
  "action_id": "merge"
}
```

The `action_id` **must match** one of the actions originally
attached to the notification. Resolving with an invalid action ID
fails with a list of valid IDs in the error.

## Race-safe with timeout watchers

The notification record's response field is atomically updated. If
the timeout watcher beat you (the user took too long and the
configured `timeout_action` already fired), this returns
"Notification X already resolved" cleanly — you don't have to check
first. The atomicity is in the data layer.

## What this leaf is NOT

- Not for inbound notifications you authored. The action_id callback
  for an actionable *you* sent dispatches automatically to your
  originating conversation; you don't call `resolve_actionable`
  yourself in that case.
- Not for arbitrary "the user said yes" interpretation. This tool
  closes a specific tracked notification by request_id; without that
  ID, there's nothing to resolve.
- Not for synchronous escalations — `request_human_escalation`
  blocks until response, so by the time the reply arrives, the
  waiter has already dispatched. `resolve_actionable` is the
  async-actionable closer.

## Cross-references

- For the upstream side (sending the actionable that's now coming
  back), `notifications_ask` is where that originated.
- For "where was the request_id annotation in the conversation
  history" — that's part of the message metadata the conversation
  rendering surfaces automatically; no extra fetch needed.
