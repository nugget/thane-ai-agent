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

A loop can be degraded with no error on record. When a wake ends
normally but every call it made to a durable write — its declared
output's tool, or `contact_dossier_write` for one contact — was
rejected, every counter reads the wake as a success. So the health
rollup in `loop_status` and the `system_health` loop census name that
loop with the tool, the contact for a dossier, and the rejection count,
and its `loop_status` row carries `unpublished_writes` with the target,
the last rejection text, and the conversation id to pull logs by. Each
contact's dossier is judged on its own: a wake that lands Alice's
dossier and never lands Bob's leaves Bob's flagged. A dossier entry
with no target is calls that wrote no dossier of their own, such as a
`contact_id` that is no active contact, and any landed dossier clears
it. Any other entry clears when a later wake lands the same write. It is held in
memory, so a restart clears it too; `loop_activity`'s
`iteration_complete` events keep the `write_rejections` and
`unpublished_writes` counts across restarts.

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

`cost_summary` answers what recorded model work cost over time. Start with
`{"period":"all","group_by":"loop"}` to compare loop lifetimes, then
use a returned group `key` as `loop_id` to inspect one. From inside a loop,
`{"loop_id":"self","since":"-3600s"}` measures its recorded work over
the last hour. Exact recorded IDs remain queryable after a loop stops;
`{"loop_name":"archivist","since":"-172800s","until":"-86400s"}`
selects that captured name over the previous day's window. A name can cover
multiple lifetime IDs, which the default loop grouping keeps separate.
The `groups[].loop_name` field is the latest captured label in the selected
window; query the ID to include records captured under other names.
An explicit `group_by` such as `model` or `role` instead breaks down the
selected spend. Loop filters cover direct recorded calls, not descendants.

Read `summary` and its pricing coverage before interpreting the bounded
`groups`: `matched_groups` counts all groups, `returned_groups` counts those
shown, and `summary` covers the full selection even when `truncated` is
true. Global loop discovery reports usage without loop attribution in `unattributed`;
do not assign it to a named loop. Missing prices mean incomplete cost coverage;
unknown pricing means uncertain coverage, with stored estimates retained.
No recorded usage does not establish no cost: calls without reported tokens
are absent, and older records may lack loop identity.

`logs_query` supplies failure evidence scoped by loop, subsystem, or request.
The `system_health` snapshot already surfaces the newest warnings and errors
with their hourly rates, so reach for `logs_query` when a sample there needs
its full story — not as the first sweep.

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
