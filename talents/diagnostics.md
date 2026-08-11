---
kind: doctrine
tags: [diagnostics]
---

# Diagnostics Doctrine

These tools look inward — at Thane's own runtime, not the world. The
craft is choosing the surface that collapses uncertainty fastest, and
knowing whether your question is about *now* or about *history*.

Start wide when anything feels off: `system_health` is one zero-argument
call that returns a status row per subsystem — ok, degraded, or failed,
each with the reason already written out — plus host vitals, queue
depths, the loop census, and the day's request/error/latency rollup.
Trust its summary line to tell you whether anything deserves a second
call at all. A degraded row names the subsystem; the row's name is what
the drill-down tools filter by.

The loop fleet has two views, and confusing them wastes calls.
`loop_status` is NOW: the process table, one canonical row per running
loop — state, cadence, token economics, errors, mailbox depth.
`loop_activity` is HISTORY, from a journal that survives restarts: every
wake with its attributed cause — timer, mailbox, subscription, a manual
`loop_wake`, an inter-loop notify — and who sent it, plus error and
no-op counts and wakes per hour. "Is the archivist stuck right now" is
`loop_status`; "who has been waking the archivist all night" is
`loop_activity`. The aggregate leads its result: read wakes-per-hour and
the by-source decomposition before scrolling events.

`queue_status` audits the durable work queues globally and read-only:
pending depth with oldest-item age per consumer, completion throughput
and wait latency over a window. Old pending work means a consumer is
not keeping up or not running — check `loop_status` for the consumer
next. You cannot drain or ack from here; consumers own their own
partitions. Completion counts measure consumer throughput: coalesced
re-enqueues refresh a pending item in place and never appear as
completions.

`doc_activity` watches the managed document corpus for churn: per
document in the window, revision count, net line delta, size, and who
wrote it — authorship comes from commit trailers, so a row authored
"manual" was not written by a loop. Documents past the revision
threshold are flagged and sort first; a maintained document rewriting
itself too often is how a runaway (an ego document accumulating
nonsense) is caught before anyone reads it. For one document's story,
switch to `doc_history` and `doc_diff` — those ride the `documents`
tag, so activate it for per-document history (a loop that owns the
document already carries the read family without any tag).

`logs_query` and `cost_summary` carry the receipts: failure evidence
scoped by loop, subsystem, or request, and spend grouped by model,
provider, or task. Reach for them when a finding needs evidence, not as
the first sweep — the annunciator and the two loop views almost always
localize the problem faster.

Version boundaries are diagnostic events in their own right, and
`system_health` precomputes the deploy story: running vs previous
version, when the boundary landed, the size of the jump, and the
recent boot list that makes restart patterns readable at a glance. Every
log line also records the version that wrote it, so `logs_query`
answers what was running when something happened. When an anomaly's
onset lines up with a version boundary, reason about the deploy before
reasoning about drift — they are different findings with different
next moves.

Everything here observes; nothing here fixes. When a finding needs an
actor, the paths are `request_core_attention` from inside a service
loop, or the `loops` tag's mutation tools when you are the one curating
the fleet.
