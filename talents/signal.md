---
tags: [signal]
teaser: "Open for proactive Signal sends — out-of-band messages or reactions, not the normal reply path."
---

# Signal

Signal is a real-time channel: messages land fast, recipients see them
without needing to check anything, and the social register is more
conversational than email or notifications. The two tools here are
narrow on purpose — most "send a Signal message" impulses inside an
*inbound* Signal conversation route through the Signal bridge
automatically (the model's final response IS the outbound message;
no tool call needed). These tools cover the cases where the bridge
isn't the right shape.

## The single most important disambiguation

**Inside a Signal conversation the model is already having: don't
call `signal_send_message`.** The Signal bridge takes the model's
final response text and sends it as the reply automatically. Using
`signal_send_message` mid-conversation produces a *second* outbound
message in addition to the reply — duplicate sends, confused
recipient.

| You want to... | Surface |
|---|---|
| Reply to a Signal message the user just sent | Just respond — the Signal bridge sends your text as the reply |
| Send a *proactive* Signal message (initiate outbound to someone not currently in this conversation) | `signal_send_message` |
| React to a specific Signal message with an emoji | `signal_send_reaction` |
| Alert-shaped notification (button responses, escalation) | `notifications` — not signal |
| Threaded correspondence with attachments and history | `email` — not signal |

The cleanest test: *am I in a Signal conversation right now, and is
this my reply to what was just said?* If yes, just answer. If no
(proactive send, sending to someone other than the current
correspondent, follow-up after the conversation ended), the tool is
the right path.

## Constants

- **Recipients are phone numbers**, not contact names. Format is
  E.164: `+15551234567` (country code, no spaces or punctuation).
  The Signal bridge resolves the contact directory on inbound
  messages; outbound requires the canonical phone number. Use
  `contact_lookup` to look up the right number when working from
  a name.
- **Async unavailability is a real failure mode.** Signal tools
  return an "unavailable" error when the local signal-cli daemon
  isn't connected (still starting, restarted, network blip).
  This isn't a permission issue — retry once after a beat, or
  bounce to `email` / `notifications` if delivery is time-
  sensitive.
- **`signal_send_reaction` needs a target.** Reactions point at a
  specific message identified by its `target_author` (phone of the
  message's author) and `target_timestamp` (the numeric `[ts:...]`
  value from the inbound message context, or the literal string
  `"latest"` to react to the most recent inbound from the
  recipient).

## Sending a proactive message

```json
{
  "recipient": "+15551234567",
  "message": "Heads up — the deploy completed cleanly. No action needed, just FYI."
}
```

Use this when:
- A loop's work finished and you want to update the user out-of-band
  ("the curate loop wrote its document; here's the link").
- Following up on a thread that's gone quiet ("did you have a chance
  to look at the PR I sent over?") — but consider whether the
  follow-up is actually warranted before reaching for the tool.
- Bridging from a different surface — e.g., the model is operating
  inside an email-triggered loop and wants to ping the user on
  Signal because that's where they'll see it first.

## Reacting to a specific message

```json
{
  "recipient": "+15551234567",
  "emoji": "👍",
  "target_author": "+15551234567",
  "target_timestamp": "latest"
}
```

Use this when the right answer is acknowledgment, not text:
- The user said "thanks, that worked" — react with 👍 instead of
  composing a "you're welcome" reply.
- The user said something funny — react with 😂 if the social
  register supports it.
- A confirmation arrived ("the package was delivered") — react
  with ✅ rather than redundant prose.

`target_timestamp: "latest"` is the easy mode; pass an explicit
timestamp from a `[ts:...]` annotation when reacting to a specific
older message in the thread.

## Cross-references

- For recipient phone-number lookup, bounce to `contacts`
  (`contact_lookup`) — Signal needs E.164 numbers, not contact
  names. The directory has the resolution.
- For alert-shaped delivery (push notifications, escalation,
  decision buttons), bounce to `notifications` — that's where the
  trust-zone gating, async callback machinery, and priority
  vocabulary live.
- For correspondence that needs threading, attachments, or a
  durable history record beyond Signal's UI, bounce to `email`.
