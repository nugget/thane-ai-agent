---
tags: [feeds]
teaser: "Open for following RSS/Atom feeds and YouTube channels — subscription management, not content reading."
---

# Feeds

Feeds is the subscription surface for RSS/Atom and YouTube channels.
Following a feed registers it for periodic polling; new entries are
detected and — when `notify: true` (the default) — dispatched as
event-source wakes to a loop (the built-in `media-default-handler`
by default, or a named curate loop you pass via `wake_loop`). With
`notify: false` the polling still happens and the entries still
land in the feed state, but no wake fires; the subscription
becomes a silent record of "we've seen these." Three tools cover
the lifecycle: follow, unfollow, list.

## The shape of this surface

`feeds` manages the *subscription*. Reading the *content* of a
feed entry is a different surface — when a new entry triggers a
wake, the receiving loop typically reaches for `media_transcript`
(under `media`) to get the actual text, or `web_fetch` (under
`web`) for arbitrary URLs. The bridge from "new entry exists"
to "what does it say" is wake-handler territory, not feeds-tool
territory.

## Follow a feed

```json
{
  "url": "https://www.youtube.com/@ChannelName",
  "wake_loop": {
    "name": "youtube_digest_curator"
  }
}
```

`url` accepts:
- Direct RSS/Atom feed URLs
- YouTube channel URLs (`https://www.youtube.com/@Name`) — the
  follower derives the canonical feed URL automatically

`wake_loop` is the `LoopWakeTarget` shape (same as
`mqtt_wake_add`): pass `{"name": "..."}` to route new entries
to a custom handler loop. Omit `wake_loop` entirely to use the
built-in `media-default-handler` — that handler exists for the
"just notice this feed; I'll figure out what to do with entries
later" case.

`notify: false` is the third configuration mode: still poll,
still record entries in the feed state, but do not dispatch
wakes. Useful when you want a passive audit trail (e.g., "we
saw these YouTube uploads this week" available via `media_feeds`
state) without spawning loop attention on each one. The
`media-default-handler` (or your custom `wake_loop` target) is
still registered; it just doesn't fire while `notify: false`.

When the right handler is a curate loop maintaining a digest
document, point `wake_loop` at it directly so each new entry
wakes the digest loop with the entry as context:

```json
{
  "url": "https://example.com/blog/feed.xml",
  "wake_loop": {
    "name": "example_blog_digest"
  }
}
```

The curate loop's task can then decide whether the new entry is
worth summarizing into the digest, ignore noise, etc. — model-
shaped judgment that the feed follower doesn't try to make on
its own.

## List current subscriptions

```json
{}
```

`media_feeds` returns currently followed feeds with status, last
check time, and latest entry title. Useful before adding a new
follow to confirm you're not duplicating, and as the source of
the `subscription_id` you'll need for unfollow.

## Stop following

```json
{
  "subscription_id": "01964ea7-7c2e-7d12-9a4b-1b2c3d4e5f6a"
}
```

`media_unfollow` retires a subscription by its ID. Get the ID
from `media_feeds` — like the mqtt-wake retirement pattern, this
is by-ID, not by-URL. The wake_loop continues to exist; this
only drops the source.

## Pairing with curate loops

The canonical shape: a `thane_curate` loop maintains a document
(`generated:youtube/<channel>.md` or similar), and the loop is
the `wake_loop` target on one or more follows. The follow notices
new entries, the curate loop wakes, the loop decides what to do
(summarize → append journal entry, skip → no-op, surface to user
→ `send_notification`).

See `loops_examples_curate` for the curate-loop shape; the
worked example there shows the wake-on-event pattern in full.

## Cross-references

- For *reading* the content of a feed entry (YouTube transcript,
  blog post body), bounce to `media` (`media_transcript`) for
  video/podcast or `web` (`web_fetch`) for arbitrary URLs.
- For the *loop shape* that consumes feed wakes, bounce to
  `loops_examples_curate` — the digest pattern is the canonical
  consumer.
- For other event-driven wakes (forge releases, MQTT messages),
  the same `wake_loop` parameter shape appears on
  `forge_repo_follow` and `mqtt_wake_add` respectively. Feeds
  is just one of three wake sources Thane supports natively.
