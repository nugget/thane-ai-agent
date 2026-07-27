# Release Notes

This directory is Thane's durable release record. Each `v*.md` file contains the corresponding GitHub release body, retained as a standalone document rather than a cumulative changelog.

## Releases

| Published (UTC) | Release | State | GitHub |
|---|---|---|---|
| 2026-07-07T17:17:29Z | [v0.10.2 — The Native Home Assistant Release](v0.10.2.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.10.2) |
| 2026-07-02T21:01:36Z | [v0.10.1 — The Provenance Release](v0.10.1.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.10.1) |
| 2026-06-30T18:20:33Z | [v0.10.0 — The Loop Corpus Release](v0.10.0.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.10.0) |
| 2026-06-26T02:36:18Z | [v0.10.0-rc.1](v0.10.0-rc.1.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.10.0-rc.1) |
| 2026-05-01T00:43:43Z | [v0.9.2 - The Context Integrity Release](v0.9.2.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.9.2) |
| 2026-04-23T22:22:45Z | [v0.9.1 - The Operational Hardening Release](v0.9.1.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.9.1) |
| 2026-04-23T20:23:49Z | [v0.9.1-rc.2](v0.9.1-rc.2.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.9.1-rc.2) |
| 2026-04-23T15:49:40Z | [v0.9.1-rc.1](v0.9.1-rc.1.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.9.1-rc.1) |
| 2026-04-07T19:57:19Z | [v0.9.0-rc](v0.9.0-rc.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.9.0-rc) |
| 2026-03-12T23:30:48Z | [v0.8.4](v0.8.4.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.8.4) |
| 2026-03-12T01:13:58Z | [v0.8.3 — Loop Infrastructure & Cognition Engine](v0.8.3.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.8.3) |
| 2026-03-10T23:00:12Z | [v0.8.2 — OpenClaw Compatibility Profile](v0.8.2.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.8.2) |
| 2026-03-10T17:37:26Z | [v0.8.1 — Structured Logging & Session Log Viewer](v0.8.1.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.8.1) |
| 2026-03-10T13:20:11Z | [v0.8.0 — Notifications, CardDAV, Media Vault & Web Dashboard](v0.8.0.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.8.0) |
| 2026-02-23T08:36:46Z | [v0.7.1](v0.7.1.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.7.1) |
| 2026-02-21T02:44:43Z | [v0.7.0 — Native Email, Trust Zones & Capability Tags](v0.7.0.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.7.0) |
| 2026-02-16T15:03:22Z | [v0.6.0](v0.6.0.md) | release | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.6.0) |
| 2026-02-12T18:26:05Z | [v0.4.0: Cost-aware Model Routing with Conversation Isolation](v0.4.0.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.4.0) |
| 2026-02-12T07:34:54Z | [v0.3.1: Context Injection](v0.3.1.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.3.1) |
| 2026-02-12T05:45:28Z | [v0.3.0](v0.3.0.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.3.0) |
| 2026-02-11T23:04:43Z | [v0.2.3](v0.2.3.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.2.3) |
| 2026-02-11T20:13:07Z | [v0.2.2](v0.2.2.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.2.2) |
| 2026-02-11T19:58:50Z | [v0.2.1](v0.2.1.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.2.1) |
| 2026-02-10T23:04:00Z | [v0.2.0](v0.2.0.md) | prerelease | [source](https://github.com/nugget/thane-ai-agent/releases/tag/v0.2.0) |

## Archival contract

- Release-note filenames are the exact Git tag followed by `.md`.
- Published releases and prereleases are both retained.
- Historical files preserve the GitHub release body with only deterministic text normalization: UTF-8 encoding, LF line endings, and exactly one terminal newline.
- [`manifest.json`](manifest.json) records the GitHub release identity, provenance timestamps, author, release state, target, asset metadata, and SHA-256 digest of every archived Markdown file.
- `published_at` is the public-release timestamp. `created_at` and `updated_at` retain GitHub's separate lifecycle timestamps. Git commit dates are not rewritten to imitate publication history.
- GitHub remains the distribution surface, but the in-repository Markdown file is the source for every future release body.
- After publication, a normal follow-up commit appends the live release metadata and file digest to the manifest and index. Values that do not exist until GitHub publishes the release are never predicted or backdated.

The manifest is a snapshot of GitHub metadata captured at `2026-07-27T22:34:10Z`. Download counts and mutable GitHub fields describe that capture, not permanent facts. Artifact `digest` values are GitHub's reported hashes; `archived_file_sha256` covers the in-repository note itself.
