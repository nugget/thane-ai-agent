---
tags: [attachments]
teaser: "Open for working with inbound attachments — list, find, or get a vision description."
---

# Attachments

The attachment store holds files that arrived through inbound
channels — images sent on Signal, PDFs forwarded to email, anything
the system received and persisted alongside the message that
carried it. The three tools cover *finding* attachments
(list/search) and *understanding* their content (describe). They
do not upload, send, or modify attachments — those are
channel-side concerns (`signal_send_message`, `email_send`).

## Three operations, three shapes

| You want to... | Tool |
|---|---|
| Browse what's been received, optionally filtered | `attachment_list` |
| Find an attachment whose subject you can name | `attachment_search` |
| Get or generate a vision description for an image | `attachment_describe` |

These are independent surfaces, not a funnel — you don't have to
list before describing, or search before listing. Each accepts
different arguments and answers different questions.

## Browse — `attachment_list`

```json
{
  "conversation_id": "signal-15551234567",
  "content_type": "image/",
  "limit": 20
}
```

Returns metadata (file name, type, sender, channel, timestamps,
cached vision description if any). All filters are optional:

- `conversation_id` scopes to a specific channel conversation
- `channel` filters by source (`signal`, `email`)
- `sender` filters by sender identifier (phone, email)
- `content_type` filters by MIME prefix — `image/` for all images,
  `application/pdf` for PDFs, `image/png` for one format

`conversation_id` uses the `channel-address` shape with a hyphen
separator: `signal-15551234567` (no plus sign, bare digits),
`email-someone@example.com`. The plus-and-colon shape (`signal:+1...`)
is *not* what the store uses.

`limit` defaults to 20 and caps at 50. The result is metadata,
not content — to *see* what's in an image, follow with
`attachment_describe` using the returned UUID.

**`attachment_list` does not support time-range filtering.** If
you need attachments from a specific window, the right path is
either: (a) scope by `conversation_id` if the window aligns with
a conversation, or (b) list and post-filter on the returned
timestamps. The tool exposes no `since`/`before` arguments.

## Find — `attachment_search`

```json
{
  "query": "thermostat photo",
  "limit": 10
}
```

Free-text search across attachment metadata: file names, cached
vision descriptions, senders, channels. Use when you remember
*what* the attachment was about but not when it arrived or who
sent it. The cached descriptions are the most useful match
surface — if vision analysis has run, "photo of the laundry
room sensor" finds the image even when the file name is
`IMG_4827.jpg`.

When search returns nothing useful, fall back to `attachment_list`
with filters — by conversation, sender, or content type.

## Describe — `attachment_describe`

```json
{
  "id": "01964ea7-7c2e-7d12-9a4b-1b2c3d4e5f6a"
}
```

Returns a vision description for an image. If a cached description
exists, returns that immediately. If not, runs vision analysis,
caches the result, and returns it. The default prompt covers
"describe what's in this image" generically.

For targeted analysis, pass a custom prompt:

```json
{
  "id": "01964ea7-7c2e-7d12-9a4b-1b2c3d4e5f6a",
  "prompt": "What text is visible in this image? Read it back verbatim."
}
```

To re-analyze with a better model now that one's available:

```json
{
  "id": "01964ea7-7c2e-7d12-9a4b-1b2c3d4e5f6a",
  "reanalyze": true,
  "model": "claude-opus-4.7"
}
```

`reanalyze: true` forces a fresh call even when a cached
description exists. The new description replaces the cached one.

## Why this is a *receive-side* surface

The catalog has no attachment-side outbound tool. Outbound
attachments are the sending channel's responsibility — Signal
doesn't currently support outbound attachments via tool, and
email's send path inlines the body without an attachment
parameter. The attachment tools also don't expose a filesystem
path in their output; the stored bytes aren't reachable through
this surface for re-attaching. If the request is "send Alice a
photo I took," the right answer is to describe the photo in
prose and ask the human to attach it through their normal
client — not a tool call here.

## Cross-references

- For finding *which conversation* an attachment came from, the
  `conversation_id` and `channel` filters on `attachment_list`
  are the right surface; the conversation history itself is in
  `archive` (search by content there if you remember what was
  said *around* the attachment).
- For sender details on an attachment (who's `+15551234567`?),
  bounce to `contacts` (`contact_lookup` by the phone or email).
- For the *content* of the surrounding messages, not the
  attachment itself, bounce to `archive_text` scoped to the
  conversation.
