# Thane archive

This directory holds Thane's accumulated history — the agent's primary
sources, the normalized interactions corpus derived from them, and the
indexes that make recall fast.

## Layout

- **`interactions/`** — normalized data, structured to assist recall.
  The derived corpus the model searches via `archive_search` and
  friends. One record per moment of agent interaction
  (conversations, sessions, wakes); the same record shape across every
  era and every source.

- **`sources/`** — pristine primary data, preserved as-is in whatever
  shape arrived. Each era / source has its own subdirectory with a
  `README.md` documenting its provenance and interpretation quirks.

- **`meta/`** — schemas, manifest, FTS5 index. Everything in here is
  derivable from `sources/` + `interactions/`; if it's deleted it can
  be rebuilt via `thane archive reindex` (#939).

## Invariants

- `sources/` is canonical and immutable. Never edit a file under there.
- `interactions/` is derived. Indexes under `meta/` are also derived.
- Every `interactions/` record carries a deterministic `source_ref`
  pointing back to its pristine origin.
- Nothing is dropped on the floor — every piece of primary data lives
  somewhere under `sources/`.

## For a fresh agent landing here

If you want to find what was said or done, query `interactions/`
(typically via `archive_search`). If you need full fidelity for one
specific thing, follow that record's `source_ref` to the pristine
record under `sources/`.

If you're doing operator forensics (loop health, errors, request
lifecycle), look under `sources/thane/` — those streams are the
operational telemetry, partitioned hourly by dataset.
