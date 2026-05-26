# sources/thane_legacy

Pristine pre-cutover Thane history. The files here predate the current
JSONL dataset pipeline under `sources/thane/`; they are preserved
exactly as they arrived so future archaeologists can reconstruct the
agent's earliest history.

## Cutover

At **2026-04-22T18:57:13Z**, the deprecated monolithic `thane.log`
slog stream shut down (its final record is the rotator closing) and
one second later the first record landed in
`sources/thane/events/2026/04/22/events-2026-04-22-18.jsonl`. The
cutover was clean — no overlap, no gap. Everything in this directory
is from BEFORE that moment.

## Likely contents (depending on what the migration found)

- **`thane.log`** — the final-write tombstone of the monolithic slog
  era. Last record is the rotator's own shutdown line.
- **`thane-YYYY-MM-DD.log.gz`** — daily-rotated gzipped slog from
  the structured-but-monolithic era (March 2026).
- **`stderr.log`** — captured stderr from the same era.
- **`original-thane-log.tar.gz`, `final-thane-log.tar.gz`** —
  Feb 2026 snapshots from the pre-structured era. These two files
  appear to be byte-identical; one is redundant.

## Interpretation notes

- These files use varying timestamp conventions. Pre-cutover entries
  may not be UTC; consult the timestamps inside each file.
- The pre-structured tarballs are plain-text logs without a fixed
  schema; treat them as best-effort historical reads.
- The interactions corpus under `archive/interactions/` will eventually
  carry normalized records derived from this directory (see #941 for
  the backfill issue); until then this is the only place that history
  lives.

## Don't edit

Like everything under `sources/`, files here are canonical and
immutable. Read freely, never modify.
