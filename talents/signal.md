---
tags: [signal]
teaser: "Open for proactive Signal sends — out-of-band messages or reactions, not the normal reply path."
---

# Signal

Signal is a real-time channel: messages land fast, recipients see them
without needing to check anything, and the social register is more
conversational than email or notifications. The two tools here are
narrow on purpose — the Signal bridge handles the *normal* reply
path automatically, and these tools cover the cases where the
bridge isn't the right shape.

## The single most important disambiguation

**When the model is inside an inbound Signal conversation, the
bridge takes the model's final response text and sends it as the
reply automatically — no tool call needed.** That's the default
path. The bridge also tracks tool calls: when `signal_send_message`
is invoked during the loop, the bridge sees that and *suppresses*
the automatic reply (no double-send). So the rule isn't "calling
the tool causes a duplicate" — it's "the tool replaces the bridge
reply, so be deliberate about what you actually want to send."

| Situation | Right move |
|---|---|
| Reply to the Signal message the user just sent | Just respond as your final text — the bridge sends it |
| Send a *proactive* message that isn't a reply (initiate outbound, follow up after the conversation ended) | `signal_send_message` |
| Send a *second* message in addition to your reply (e.g., a long reply split for readability) | First `signal_send_message`, then either let the bridge reply or call again |
| React to a specific message with an emoji | `signal_send_reaction` |
| Alert-shaped notification (button responses, escalation) | `notifications` — not signal |
| Threaded correspondence with attachments and history | `email` — not signal |

The cleanest test: *if I do nothing extra, will the bridge send the
right thing?* If yes, just answer and let the bridge handle it. If
no — the message goes to a different recipient, or you need to send
multiple things, or you want to send *before* the final reply text
— `signal_send_message` is the right path.

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
