---
kind: entry_point
tags: [interactive]
teaser: "Activate when the next move is about how to behave in a live human conversation."
next_tags: [signal, owu]
---

# Interactive Decision Tree

Choose the next step deliberately:

- Activate `signal` when the channel is messaging-centric or the task is
  about sending or reacting on Signal.
- Rely on `owu` context when the conversation is happening in Open WebUI.
- Use `owner` only when it is already runtime-asserted; do not try to
  activate protected owner state manually.

Bias toward direct, user-visible progress. If a long-running delegate is
needed, prefer keeping the user informed rather than silently stalling.
