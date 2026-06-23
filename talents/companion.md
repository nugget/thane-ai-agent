---
name: companion
tags: [companion]
kind: trailhead
teaser: "Open when a macOS companion (a paired Mac) holds the answer — its calendar, contacts, reminders, or other host-side data."
---

# Companion Trailhead

A macOS companion app is a paired Mac inside your trust boundary that
exposes some of its host data and tooling to you over a live connection.
Its calendar, contacts, and reminders are the usual reasons to come here.

A companion is a laptop. It connects and disconnects without warning, so
its tools are present only while it is online — they are not part of your
permanent surface. The connected-companion context block (shown on this
tag when any Mac is online) is your ground truth for what is reachable
right now: which accounts are connected and the exact tool names each one
currently offers. Read it before assuming a tool exists.

How to work here:

- The connected Mac authors its own tools, so their exact names come from
  it, not from a fixed list. Use the tool whose name and description match
  the data you need; `macos_calendar_events` lists calendar events, and a
  connected Mac may also offer contact-search and reminder-listing tools.
- If a tool you expect is absent, the Mac that provides it is probably
  offline rather than the request being wrong. Say so plainly instead of
  substituting a guess.
- When more than one account has a Mac connected, a call may come back
  asking you to disambiguate. Retry with `account` (and `client_id` if one
  account has several hosts) set to one of the choices it names.
- These are read surfaces — inspect before you expect to change anything.
  Writing host data (e.g. creating calendar events) is gated separately and
  is not available just because a read tool is.
