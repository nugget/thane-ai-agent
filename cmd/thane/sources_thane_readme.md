# sources/thane

Pristine primary data from the current-era Thane process. Every record
written here is the canonical, immutable on-disk source-of-truth for
what Thane actually did at that moment.

## Datasets

Each dataset is hourly-partitioned JSONL with self-describing
filenames:

    <dataset>/YYYY/MM/DD/<dataset>-YYYY-MM-DD-HH.jsonl

For example:

    loops/2026/05/26/loops-2026-05-26-14.jsonl

Datasets in this directory:

- **`loops/`** — loop lifecycle events (state transitions, iteration
  start/complete). High volume; mostly heartbeat-shaped.
- **`events/`** — broader operational events. Ad-hoc slog records
  from runtime components.
- **`http_access/`** — HTTP access logs from the API server. (Named
  `http_access/` to disambiguate from `requests/`, which is LLM
  request lifecycle, not HTTP.)
- **`envelopes/`** — message bus delivery audit records.
- **`conversations/`** — every completed LLM request envelope: model,
  system prompt, user/assistant content, token counts, tool usage.
  One record per request.

## Schema

Every record follows the `DatasetRecord` envelope: `event_id`, `ts`,
`dataset`, `kind`, `schema_version`, plus per-record context fields
(`request_id`, `session_id`, `conversation_id`, `loop_id`, etc.) and a
free `payload` bag for dataset-specific content.

The canonical schema spec lives in `meta/schema/interactions.v1.json`
once the interactions normalizer (#938) lands.

## Era

Records in this tree begin **2026-04-22T18:57:13Z**, when Thane cut
over from the deprecated monolithic `thane.log` slog stream to this
per-dataset JSONL pipeline. Pre-cutover history lives at
`../thane_legacy/` (when the legacy backfill from #941 has run).

## Interpretation notes

- All timestamps in records and in partition paths are UTC.
- The embedded `ts` field is the authoritative event time; the
  directory partition (`YYYY/MM/DD/`) is derived from it for fast
  date-range scans.
- Records are append-only. Never edit a file in place.
- Filenames include the dataset name so a segment file remains
  identifiable when copied out of tree.
