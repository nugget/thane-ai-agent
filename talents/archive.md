---
name: archive
tags: [archive]
kind: trailhead
teaser: "Open for past conversations — what was said, by whom, in which session."
next_tags: [archive_text, archive_time, archive_session]
---

# Archive

Archive is your long-term memory of conversations — the words spoken
by you, the user, and the tool I/O captured alongside. Four tools,
three ways the model usually asks the question.

## The single most important disambiguation

**Archive holds the *words said*. Logs hold the *events produced*.
Memory holds the *truths distilled*.** Three stores, three altitudes,
easily confused with each other:

| You want... | Surface |
|---|---|
| The literal words of a past conversation | Activate `archive`, then pick a tool below |
| A system event (loop iteration, tool call, error, model response) | Call `logs_query` directly — it's a core tool |
| A stable host-level fact you wrote down (preferences, layout, routines) | Activate `memory` — the search axis there is `recall_fact`, not text search |
| The texture/tone/arc of the *current* conversation | `session_working_memory` (see [`working-memory.md`](working-memory.md)) — not archive |

All four span time. Three have free-text search. They are not the
same surface. A question like "what did I tell the user about VLAN
30 last week?" goes to archive — the conversation is the source. A
question like "what loop iterations ran during that crash last
Tuesday?" goes to logs_query. A question like "what do I know about
the VLAN 30 routine?" goes to memory — the *fact* outlives any one
conversation that produced it. A question like "what happened around
3pm on Thursday?" needs both archive and logs.

When you're not sure, ask: am I looking for *words spoken*, *events
produced*, or *truths distilled*? The split is clean once named.

## Choose by the shape of your question

- **You have a topic or phrase in mind** — activate `archive_text`.
  Semantic search across every past session; returns matches with
  the surrounding context window so you see a moment, not a line.

- **You have a time window in mind** — activate `archive_time`.
  Verbatim messages by time range, crosses session boundaries.
  Right for "what was said between X and Y" or "the last 50
  messages" regardless of session.

- **You have a specific past session in mind** — activate
  `archive_session`. Browse the session catalog by title/tag/recency,
  then read one in full. Two tools that work as a pair.

## Result envelopes and byte caps

All archive tools return JSON envelopes with delta-second timestamps
(`-3600s` = an hour ago). All four enforce per-tool byte caps that
truncate gracefully when results exceed the budget:

- `truncated: true` in the envelope means the result was clipped —
  narrow the query (tighter time range, more specific phrase,
  shorter limit) and retry if you need completeness.
- The fitter drops lowest-relevance hits first for search, oldest
  sessions first for browse, oldest messages first for range.

Don't infer absence from a truncated result. A truncated archive
search with no matches in your slice doesn't mean the topic wasn't
discussed — it means the byte budget was spent on other hits before
yours.

---
name: archive_text
tags: [archive_text]
kind: trailhead
teaser: "Semantic search across past sessions — phrasing matters less than concept."
---

# Search by text

You have a topic, phrase, or concept in mind. Semantic search
across every past session you've had with anyone:

```json
{
  "query": "VLAN renumbering decision",
  "limit": 5
}
```

Each result is the matching message plus surrounding context bounded
by natural silence gaps — so you see a moment in conversation, not
an isolated line. The default 10-minute silence threshold catches
conversational breaks; tighten with `silence_minutes` for tighter
clips, loosen for fuller context.

## Scoping to one conversation

When you remember which conversation but not the exact wording:

```json
{
  "query": "rollback plan",
  "conversation_id": "deepslate:default",
  "limit": 10
}
```

Scoping reduces cross-conversation noise dramatically when the topic
is one you've discussed across multiple contexts.

## Following the trail

A search hit is usually the start of a thread, not the whole thing.
Once a hit looks worth reading, pull the full session with
`archive_session_transcript` (in the `archive_session` branch).
Search-then-transcript is the canonical pairing.

## Cross-references

- For "what was said in this *time window*" (regardless of topic),
  bounce to `archive_time`. Search is about content; range is about
  time.
- For system events that happened in the same window — tool calls,
  loop iterations, errors — use `logs_query` (always available, no
  activation needed).

---
name: archive_time
tags: [archive_time]
kind: trailhead
teaser: "Verbatim messages by time range — crosses session boundaries."
---

# Pull by time range

You want messages from a specific window, not a topic search. The
range tool — `archive_range` — crosses session boundaries:
sessions are an internal abstraction here; this tool just gives
you the messages.

## A specific window

`archive_range`'s `min_time` and `max_time` accept RFC3339
timestamps or signed deltas:

```json
{
  "min_time": "-3600s",
  "max_time": "-1800s",
  "max_messages": 100
}
```

The example pulls messages from 60 to 30 minutes ago. Deltas are
easier than RFC3339 for "in the last X" framings; absolute
timestamps are clearer for fixed past windows.

## "Last X minutes OR Y messages, whichever is more"

`min_messages` acts as a floor regardless of time:

```json
{
  "min_time": "-1800s",
  "min_messages": 50
}
```

Returns the last 30 minutes' worth, OR the most recent 50 messages
if the conversation was quieter than that. Useful when you want
context guaranteed without the time window starving on a slow
conversation.

## Scoping

Optionally limit to one conversation, or exclude the current
session (useful when you want archived history but not your own
current turn-by-turn rows):

```json
{
  "conversation_id": "deepslate:default",
  "exclude_session_id": "<current_session_id>"
}
```

## Cross-references

- For "what was said about *topic X*" (content-shaped, not
  time-shaped), bounce to `archive_text`. Range is the wrong tool
  for content questions — it returns everything in the window,
  topic-blind.
- For browsing the sessions in a window (catalog, not verbatim
  messages), bounce to `archive_session`.
- For system events in the same time window, `logs_query` (always
  available) is the parallel surface.

---
name: archive_session
tags: [archive_session]
kind: trailhead
teaser: "Browse the session catalog by title/tag/recency, then read one in full."
---

# Browse and read sessions

You have a specific past conversation in mind (or want to find one
by title or tag). Two tools that work as a pair: browse to identify,
transcript to read.

## Browse the catalog

`archive_sessions` returns past sessions newest-first with metadata
— title, tags, message count, duration, summary:

```json
{
  "limit": 20
}
```

Or scoped to one conversation:

```json
{
  "conversation_id": "deepslate:default",
  "limit": 30
}
```

Use this when you want to flip through history without a specific
search query, or when you remember a session by its title or tag
("the postmortem one" / "tagged release").

## Read one in full

Once you've identified the session worth examining, pull the
complete transcript with `archive_session_transcript`. Pass either
the full session ID, or an 8-character prefix — the handler resolves
short prefixes (up to 8 chars) by lookup; values longer than 8
chars are treated as full IDs and won't prefix-match:

```json
{
  "session_id": "019e6238"
}
```

Returns the full message-by-message transcript in chronological
order. The transcript cap is larger than the search/browse cap
(32KB vs 16KB) because reading one session in full is the explicit
intent.

## The canonical pairing

The search-then-transcript and browse-then-transcript pairings are
the dominant patterns in this branch:

1. `archive_search(query: "X")` → identifies a hit in session A
2. `archive_session_transcript(session_id: A)` → reads A in full

Or:

1. `archive_sessions(limit: 30)` → catalog
2. `archive_session_transcript(session_id: <chosen>)` → reads chosen

Reaching for transcript without first narrowing via search or browse
is wasteful — sessions can be long, and the byte cap means a random
guess often returns a clipped transcript when a targeted one would
return the whole thing.

## Cross-references

- For finding by content rather than by session metadata, bounce to
  `archive_text` — search-then-transcript is usually the better
  path when the question is "where did we discuss X."
- For verbatim messages across a time window regardless of session,
  bounce to `archive_time`.
- For forensic detail on what the system *did* during a session
  (tool calls, loop iterations, errors), `archive_sessions`'s
  projection covers conversation-level metadata only — start/end
  times, message count, title, tags, summary. The mechanical event
  side lives in `logs_query` (always available); scope it to the
  session's time window for "what happened during this conversation."
