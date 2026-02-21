# Release v0.7.0

> From "Thane should be user-aware" (#2) to native email with trust-zone-gated send in three weeks.

## Highlights

**Native Email** — Full IMAP/SMTP support replaces the MCP email server. Read, search, send, reply, move, and mark messages. Outbound email uses markdown-to-MIME conversion (multipart/alternative with text/plain + text/html via goldmark). Trust zone gating on all recipients. Auto-Bcc owner for audit trail. Sent folder storage via IMAP APPEND.

**Email Polling** — Scheduled IMAP checks with high-water mark tracking via the new operational state store. Wakes the agent only when new messages arrive — zero LLM tokens on empty cycles.

**Capability Tag System** — Dynamic tool loading based on semantic tags. Sessions start lightweight (~15-20 tools) and activate capabilities on demand. Creates delegation pressure by architecture, not rules. Tags are config-driven, semantic (one `ha` tag, not `ha_native` + `ha_mcp`), and filter both tools and talents.

**Trust Zones** — First-class `trust_zone` attribute on contacts (owner/trusted/known). Gates email send permissions, compute allocation, and proactive behavior. Idempotent migration from freeform facts. `FindByTrustZone` query method for bulk operations.

**Model Routing Overhaul** — Every non-interactive code path now has explicit quality/speed hints. Delegates get `prefer_speed` + quality floors (general=5, ha=4). Session summarizer gets quality_floor=7 (permanent memory deserves quality). Compaction summarizer routes through the router instead of bypassing it. New `HintPreferSpeed` scoring signal.

**Self-Reflection** — Periodic ego.md analysis with tightened prompt and model routing to Sonnet-class models. Reflection observes but doesn't act.

## What's New

### Email (`internal/email/`)
- Native IMAP client with lazy connect, mutex serialization, connwatch health monitoring
- 8 tools: `email_list`, `email_read`, `email_search`, `email_folders`, `email_mark`, `email_send`, `email_reply`, `email_move`
- Markdown → MIME: goldmark renders HTML, regex strips to plain text, `multipart/alternative` envelope
- SMTP with implicit TLS (port 465) and STARTTLS (port 587), context-aware dial timeouts
- Reply threading: `In-Reply-To`, `References`, `Re:` subject prefix, reply-all with self-exclusion
- IMAP MOVE with extension detection (falls back to COPY + STORE \Deleted + EXPUNGE)
- Unknown charset handling: `message.IsUnknownCharset` check prevents empty body on ISO-8859-1/Windows-1252 emails
- Trust zone gating via `ContactResolver` interface (decouples email→contacts packages)
- Auto-Bcc owner: configurable audit trail, skips when owner is already a recipient
- Sent folder: IMAP APPEND with `\Seen` flag, best-effort (warns on failure)
- Email polling: `Poller` checks accounts against opstate high-water marks, first-run seeding

### Capability Tags (`internal/config/`, `internal/tools/`, `internal/agent/`)
- `capability_tags` config section with `description`, `tools` list, `always_active` flag
- `activate_tags` / `deactivate_tags` / `list_tags` tools for runtime tag management
- Talent frontmatter filtering: talents with `tags: [email]` only load when `email` tag is active
- Channel-pinned tags (future): Signal channel will immutably get `signal` tag

### Contacts & Trust Zones (`internal/contacts/`)
- `trust_zone` column: owner/trusted/known, validated by `ValidTrustZones` map
- Idempotent migration from freeform "trust_level" facts to structured column
- `FindByTrustZone` query method
- `FindByFact` for email/phone lookups
- Context injection: contacts with trust tags in system prompt
- `save_contact` double-unmarshal fix: rescues top-level fields that `json.Unmarshal` silently drops

### Model Router (`internal/router/`)
- `HintPreferSpeed`: +15 scoring bonus for models with Speed ≥ 7
- Fixed `rulesMatched` bug: was appending to `decision.RulesMatched` during scoring, accumulating rules from all candidate models
- All non-interactive paths now have explicit routing hints

### Session Management (`internal/agent/`, `internal/tools/`)
- `session_close` with required `carry_forward` note
- `session_checkpoint` for crash recovery
- `session_split` for context forking
- Context usage line in system prompt (token count, percentage, session/conversation IDs)

### Delegation (`internal/delegate/`)
- Execution summaries: iteration count, tool call trace, errors, duration
- Profile routing hints: general=quality_floor:5+prefer_speed, ha=quality_floor:4+prefer_speed
- `iter0_tools` → `orchestrator_tools` rename with backward compatibility

### Scheduler & Anticipations
- Per-task model and routing overrides (`internal/scheduler/`)
- Per-anticipation `Model`, `LocalOnly`, `QualityFloor` stored in SQLite (`internal/anticipation/`)
- Wake bridge uses stored hints with sensible defaults

### Operational State (`internal/opstate/`)
- Generic namespace/key/value SQLite store
- Used by email poller for high-water marks
- Designed for reuse: feature flags, session preferences, future poller cursors

### Self-Reflection (`internal/prompts/`)
- Tightened reflection prompt: observe and record, don't act
- Routed to quality_floor=7 models (Sonnet-class)
- Daily interval (changed from 15-minute)

### Other
- `inject_files` re-read per turn (was read-once at startup)
- Compaction summarizer routed through model router (was bypassing with `cfg.Models.Default`)
- Tool description tightening to prevent cross-contamination between contacts and facts tools

## Breaking Changes

- `iter0_tools` config key deprecated in favor of `orchestrator_tools` (backward compatible, logs warning)
- MCP email server no longer needed — native email replaces it entirely
- `capability_tags` config section is new and required for tag-based tool filtering

## Bug Fixes

- `save_contact` silently dropping top-level fields due to `json.Unmarshal` behavior (#268, #278)
- `session_close` `handoff_note` renamed to `carry_forward` for clarity (#270)
- Tool calls re-archived across session splits (#271)
- Router `rulesMatched` accumulation bug (fixed in #301)
- MIME body parser returning empty body on unknown charsets (#303, #305)
- SMTP Bcc header leak — blind-copy recipients now only in SMTP envelope (#302)
- SMTP implicit TLS support for port 465 (#302)
- SMTP password validation in config (#302)
- Trust zone bypass on auto-Bcc owner address (#302)
- Silent trust zone failures now return errors (#302)

## Stats

- **24,612 lines added** across 124 files since v0.6.0
- **21 open issues** (down from ~30)
- PRs #278–#306 merged in this release
