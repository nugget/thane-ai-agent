---
name: companion
tags: [companion]
kind: trailhead
teaser: "Open when a paired companion device holds the answer — a Mac's calendar, contacts, or reminders, or an iPhone's latest observations."
---

# Companion Trailhead

A companion is a paired device inside your trust boundary — a macOS or
iOS app on the operator's own hardware — that exposes some of its host
data and tooling to you. A Mac's calendar, contacts, and reminders are
the usual reasons to come here; an iPhone contributes observations it
pushes in the background, such as its last known location.

A device and its connection have different lifetimes. Phones lock and
laptops sleep, so connections come and go without warning — but the
device stays paired, and the "### Companion Devices" block in Live
State (always rendered on this tag, even when everything is offline) is
your ground truth for both halves: which devices exist, which are
online right now, and how fresh each offline device's last contact is.
An offline device is a normal state, not a fault.

Reading the device block:

- `availability` says whether a live connection is open. Only online
  devices have callable tools, listed per device by exact name.
- `device_id` is the durable handle for a device — stable across
  reconnects and credential changes. (A fresh install that presents a
  brand-new identity claim registers as a new device.) `client_id` is
  the device's current claim, and is what tool routing accepts today.
- `observations` lists the latest report per kind with two ages:
  `observed_ago` is the device's claim about when it happened,
  `received_ago` is when it reached you — they differ when a device
  uploads an old backlog. A `withdrawn` status means sharing was
  revoked; that data is gone, not stale. No tool fetches observation
  payloads yet — the block's freshness is what you know.

How to work here:

- A connected device authors its own tools, so their exact names come
  from it, not from a fixed list. Use the tool whose name and
  description match the data you need; `macos_calendar_events` lists
  calendar events, and a connected Mac may also offer contact-search
  and reminder-listing tools.
- If a tool you expect is absent, check the device block before
  concluding anything: when the device shows offline, say so plainly
  with its last-seen age; when it shows online without that tool, the
  device simply does not offer the capability right now — its platform
  may lack it or registration may still be settling. Neither case
  means the request was wrong, and neither is a reason to guess.
- When more than one account has a device connected, a call may come
  back asking you to disambiguate. Retry with `account` (and
  `client_id` if one account has several devices) set to one of the
  choices it names.
- A "### Calendar" block in Live State is the mechanical snapshot of
  the household's near-term calendar: events active right now (with an
  ends-in delta), the next upcoming one, and today's remainder,
  refreshed in the background from a connected Mac. Trust its
  snapshot-age, stale, and offline fields over assumptions about
  freshness; when it reads truncated, or you need more than two days
  out, pull the window you need with `macos_calendar_events`.
- These are read surfaces — inspect before you expect to change
  anything. Writing host data (e.g. creating calendar events) is gated
  separately and is not available just because a read tool is.
