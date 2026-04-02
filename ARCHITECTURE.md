# Thane Architecture

> **Thane**: A Scottish landholder who managed an estate on behalf of the crown.
> An autonomous AI agent that learns, remembers, and acts.

## Philosophy

A vibration sensor on a washer tells you it's done. But understanding that you're home, it's been 30 minutes, and you haven't moved the laundry — and gently reminding you before it gets musty — that's the difference between automation and comprehension.

This is what Thane is building toward: a system that notices, understands, and helps. Not through rigid automation rules, but through genuine contextual awareness.

**Understanding over Rules.** Traditional home automation fires events. Thane comprehends situations. The difference between "washer stopped" and "laundry needs attention before it mildews."

**Boring Tech, Creative Application.** Go, SQLite, MQTT, Home Assistant. Mature, stable, documented. The innovation isn't in the stack — it's in how these pieces compose to create something that can actually care about your environment.

**Open Source as Philosophy.** This isn't a product seeking users. It's an idea seeking evolution. Every component is accessible, every decision documented. Someone will fork this and do something we never imagined. That's the point.

### What We're Not Building

- Not another voice assistant (though voice is one interface)
- Not another automation platform (though we integrate with automation)
- Not a business model (this is gift culture)

### What We Are Building

A nervous system for living spaces. Sensors that notice. Anticipations that remember what matters. Context engines that understand. All working together to create an environment that's genuinely helpful without being intrusive.

*Wake frequently, speak rarely. The difference is the craft.*

---

## The Core Insight

Thane is an **autonomous agent** — an LLM with persistent memory, tool use, and the ability to act on your behalf. It's not constrained to a pre-defined set of capabilities; it discovers what's available and reasons about how to help.

Home Assistant is Thane's flagship integration, but not a requirement. With HA, Thane becomes a dramatically more capable replacement for the built-in Assist — querying any entity, discovering devices dynamically, and taking intelligent action without pre-selection. Without HA, Thane is still a capable self-hosted AI with semantic memory, shell access, and file operations.

## Design Principles

### Local-First, Cloud-Optional

Thane can run entirely on Ollama. Every conversation stays on your hardware. Cloud models are available for complex reasoning, but nothing requires them.

### Memory as Foundation

Memory isn't bolted on — it's foundational:

- **Semantic facts** — Learned knowledge with embeddings for natural language recall
- **Conversations** — Context that persists across sessions
- **Checkpoints** — Full state snapshots for crash recovery
- **Archives** — Searchable conversation history with tool call preservation
- **Episodic summaries** — Post-session analysis for long-term memory consolidation

Memory is queryable: the agent can ask "what do I know about the garage?" and get structured answers through semantic search.

### Talent-Driven Behavior

Agent personality and behavioral guidance live in markdown files (`talents/`). This keeps behavior transparent, editable, and version-controlled. Natural language is the configuration mechanism — it carries nuance that structured config cannot.

Talents are **tag-filtered** — each talent can declare which capability tags it requires (via YAML frontmatter). A talent tagged `[email]` only loads when the `email` tag is active, keeping context lean.

### Contact Directory

Thane maintains a persistent contact database — every person the agent interacts with gets a record. Contacts store names, communication addresses, relationship context, and a **trust zone** classification. The agent can create, update, and query contacts through dedicated tools. This is the foundation for personalized behavior: Thane knows who it's talking to and adjusts accordingly.

### Trust Zones

Every contact has a trust zone: `admin`, `household`, `trusted`, or `known`. Trust zones are the universal router — they gate:

- **Email send** — admin/household send freely, known requires confirmation, unknown blocked
- **Compute allocation** — admin/household get frontier models, others get local triage first
- **Notification priority** — trust zone determines urgency levels and rate limits
- **Proactive behavior** — how much initiative the agent takes depends on who's asking

### Dual-Port Architecture

Thane serves two APIs simultaneously:

- **Port 8080** — Native API (OpenAI-compatible `/v1/chat/completions`). For direct integration, development, and the built-in web chat.
- **Port 11434** — Ollama-compatible API. Home Assistant's native Ollama integration connects here without modification. Thane strips HA's injected tools and system prompts, substituting its own smarter toolset.

This means Thane works with HA out of the box — no custom integration needed.

## Architecture Overview

```
┌───────────────────────────────────────────────────────────────────┐
│                          Thane Agent                               │
├───────────────────────────────────────────────────────────────────┤
│                                                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────────────┐  │
│  │  Native API  │  │ Ollama-Compat│  │     Event Sources       │  │
│  │  (port 8080) │  │ (port 11434) │  │  HA WS │ MQTT │ Email  │  │
│  │  + Dashboard │  │              │  │  Signal │ RSS Feeds     │  │
│  └──────┬───────┘  └──────┬───────┘  │  Scheduler │ Anticip.  │  │
│         │                 │          └────────┬────────────────┘  │
│  ┌──────┴───────┐         │                   │                   │
│  │ CardDAV Srvr │         │                   │                   │
│  │ (port 8843)  │         │                   │                   │
│  └──────┬───────┘         │                   │                   │
│         └─────────────────┼───────────────────┘                   │
│                           ▼                                        │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │                      Agent Loop                              │  │
│  │                                                              │  │
│  │  Context Assembly → Tag Activation → Planning → Delegation   │  │
│  │        ↑                                  │                  │  │
│  │    Talents &                          ┌───┴────┐             │  │
│  │    Memory &                           ↓        ↓             │  │
│  │    Contacts                      Direct    Delegate          │  │
│  │                                (orchestr.) (local model)     │  │
│  └─────────────────────────────────────────────────────────────┘  │
│         │                │                    │                     │
│         ▼                ▼                    ▼                     │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │   Memory    │  │   Model    │  │      Integrations         │  │
│  │   Store     │  │   Router   │  │  HA (REST/WS) │ MCP Host  │  │
│  │  (SQLite)   │  │ (scoring)  │  │  Email │ Signal │ Forge   │  │
│  └─────────────┘  └────────────┘  │  Shell │ Files │ Search   │  │
│                                    └───────────────────────────┘  │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │ Anticipation│  │  Scheduler │  │  Contacts & Trust Zones   │  │
│  │   Engine    │  │  (cron)    │  │  Notifications (HITL)     │  │
│  └─────────────┘  └────────────┘  └───────────────────────────┘  │
│                                                                    │
│  ┌─────────────┐  ┌────────────┐  ┌───────────────────────────┐  │
│  │   OpState   │  │    MQTT    │  │    Self-Reflection        │  │
│  │  (KV store) │  │ Publisher  │  │    (ego.md)               │  │
│  └─────────────┘  └────────────┘  └───────────────────────────┘  │
│                                                                    │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │                      Event Bus                               │  │
│  │  Agent │ Signal │ Delegate │ Email │ Metacog │ Scheduler    │  │
│  └─────────────────────────────────────────────────────────────┘  │
│                                                                    │
└───────────────────────────────────────────────────────────────────┘
```

## Components

### Agent Loop

The core reasoning cycle:

1. **Context Assembly** — Gather relevant memory, talents, contacts, and the current request. Inject context usage line showing token consumption.
2. **Tag Activation** — Determine which capability tags are active for this session. Tags control which tools and talents are visible.
3. **Planning** — Determine what information or actions are needed
4. **Delegation** — Orchestrator model plans; tool-heavy work is delegated to local models with execution summaries
5. **Tool Execution** — Run tool calls (parallel where possible), via native tools or MCP
6. **Response Shaping** — Format output for the requesting interface

### Capability Tag System

Tools and talents are organized by **semantic tags** (`ha`, `email`, `signal`, `github`, `web`, etc.). Sessions start with a minimal set of `always_active` tags (core tools like memory, files, planning). Additional tags are activated on demand — either by the agent, the user, or automatically by the channel.

This creates **delegation pressure by architecture**: the orchestrator model starts with ~15-20 tools instead of 60+, naturally reaching for `thane_delegate` when it needs capabilities outside its active set.

Tags are defined in config:
```yaml
capability_tags:
  ha:
    description: "Home Assistant device control and monitoring"
    tools: [control_device, find_entity, get_state, list_entities, call_service]
  email:
    description: "Email reading, sending, and management"
    tools: [email_list, email_read, email_search, email_folders, email_mark, email_send, email_reply, email_move]
  memory:
    description: "Fact storage and recall"
    tools: [remember_fact, recall_fact, forget_fact]
    always_active: true
```

### Delegation System

The orchestrator model (cloud or large local) focuses on reasoning and orchestration. When tool-heavy work is needed — HA queries, file operations, shell commands — it delegates to smaller, faster local models that execute the task and return structured execution summaries (iteration count, tool call trace, errors, duration).

Delegate profiles control routing hints:
- **general** — quality_floor=5, prefer_speed=true (fast local models for file checks, web searches)
- **ha** — quality_floor=4, prefer_speed=true (device control needs speed, not deep reasoning)

Delegation is guided by **talents** — markdown files that teach the orchestrator how to write effective delegation prompts, which tools are available to delegates, and what patterns to follow.

### Contact Directory & Trust Zones

Persistent contacts database (`contacts.db`) with vCard-native storage:

- **Trust zones** — `admin`, `household`, `trusted`, `known` (validated by `ValidTrustZones` in Go; `unknown` is implicit for unrecognized senders)
- **vCard-aligned Properties** — structured contact fields (email, phone, address, Signal handle) stored as typed properties with vCard field names, parameters, and preference ordering
- **Context injection** — contacts relevant to the active channel injected into system prompt with relationship metadata and last-interaction context
- **`FindByTrustZone`** — query method for bulk operations (email triage, compute routing)
- **`FindByProperty`** — lookup contacts by email address, phone number, or any property key
- **`FilterCardForTrustZone`** — trust-aware vCard export for CardDAV (strips sensitive fields for lower trust zones)

Trust zones gate behavior across the system — email send permissions, model selection for inbound messages, notification priority, and proactive behavior thresholds.

### Native Email

Full IMAP/SMTP email support (`internal/email/`):

**Read-side (IMAP):**
- `email_list` — browse messages with unseen filtering
- `email_read` — fetch full message with MIME body parsing (handles nested multipart, unknown charsets)
- `email_search` — server-side IMAP search
- `email_folders` — list available mailboxes
- `email_mark` — flag/unflag, mark read/unread
- `email_move` — move messages between folders (uses IMAP MOVE extension with COPY+DELETE fallback)

**Write-side (SMTP):**
- `email_send` — compose new email with markdown-to-MIME conversion (multipart/alternative: text/plain + text/html via goldmark)
- `email_reply` — reply with proper threading headers (In-Reply-To, References)

**Infrastructure:**
- Multi-account support with lazy IMAP connections and connwatch health monitoring
- Trust zone gating on all outbound recipients
- Auto-Bcc owner for audit trail
- Sent folder storage via IMAP APPEND
- Email polling via scheduler with opstate high-water marks

### Tools

**Native Tools (80+):**

| Tool | Tag | Description |
|------|-----|-------------|
| **Home Assistant** | | |
| `control_device` | ha | Natural language device control with fuzzy matching |
| `find_entity` | ha | Smart entity discovery across all HA domains |
| `get_state` | ha | Current state of any entity |
| `list_entities` | ha | Browse entities by domain or pattern |
| `call_service` | ha | Direct HA service invocation |
| `add_context_entity` | ha | Add entity to the state watchlist |
| `remove_context_entity` | ha | Remove entity from the state watchlist |
| `ha_notify` | ha | HA companion app push notification |
| **Email** | | |
| `email_list` | email | List messages in a folder |
| `email_read` | email | Read message with full body |
| `email_search` | email | Server-side IMAP search |
| `email_folders` | email | List available mailboxes |
| `email_mark` | email | Flag/unflag messages |
| `email_send` | email | Compose and send (markdown → MIME) |
| `email_reply` | email | Reply with threading |
| `email_move` | email | Move messages between folders |
| **Contacts** | | |
| `save_contact` | contacts | Create or update contacts with vCard properties |
| `lookup_contact` | contacts | Search contacts by name, query, kind, or property |
| `forget_contact` | contacts | Delete a contact |
| `list_contacts` | contacts | List and filter contacts |
| `export_vcf` | contacts | Export contact as vCard |
| `export_vcf_qr` | contacts | Export contact as vCard QR code |
| `export_all_vcf` | contacts | Bulk vCard export |
| `import_vcf` | contacts | Import vCard data |
| **Notifications** | | |
| `send_notification` | always | Provider-agnostic fire-and-forget notification |
| `request_human_decision` | always | Actionable notification with HITL callbacks |
| **Media & Feeds** | | |
| `media_transcript` | media | Fetch video/podcast transcript via yt-dlp |
| `media_follow` | media | Follow an RSS/Atom feed or YouTube channel |
| `media_unfollow` | media | Stop following a feed |
| `media_feeds` | media | List followed feeds with status |
| `media_save_analysis` | media | Save analysis to Obsidian vault |
| **Memory & Knowledge** | | |
| `remember_fact` | memory | Store knowledge with embeddings |
| `recall_fact` | memory | Retrieve knowledge by category or search |
| `forget_fact` | memory | Remove stored knowledge |
| **Archive** | | |
| `archive_search` | always | Full-text search across conversation history |
| `archive_sessions` | always | Browse session archive |
| `archive_session_transcript` | always | Retrieve full session transcript |
| **Session** | | |
| `session_working_memory` | always | Read/write scratchpad for active session |
| `session_close` | always | Close session with carry-forward context |
| `session_checkpoint` | always | Save current session state |
| `session_split` | always | Fork the current session |
| `conversation_reset` | always | Reset conversation context |
| **Planning** | | |
| `schedule_task` | planning | Time-based future actions |
| `list_tasks` | planning | List scheduled tasks |
| `cancel_task` | planning | Cancel a scheduled task |
| **Capabilities** | | |
| `activate_capability` | always | Activate capability tags for current conversation |
| `deactivate_capability` | always | Deactivate capability tags |
| `activate_lens` | always | Activate a persistent global behavioral lens |
| `deactivate_lens` | always | Deactivate a global behavioral lens |
| `list_lenses` | always | List active behavioral lenses |
| **Forge (GitHub/Forgejo)** | | |
| `forge_issue_create` | forge | Create an issue |
| `forge_issue_get` | forge | Get issue details |
| `forge_issue_list` | forge | List issues with filters |
| `forge_issue_update` | forge | Update issue fields |
| `forge_issue_comment` | forge | Comment on an issue |
| `forge_pr_list` | forge | List pull requests |
| `forge_pr_get` | forge | Get PR details |
| `forge_pr_diff` | forge | Get PR diff |
| `forge_pr_files` | forge | List changed files |
| `forge_pr_commits` | forge | List PR commits |
| `forge_pr_reviews` | forge | List PR reviews |
| `forge_pr_review` | forge | Submit a review |
| `forge_pr_review_comment` | forge | Comment on specific code |
| `forge_pr_checks` | forge | Get CI check status |
| `forge_pr_merge` | forge | Merge a PR |
| `forge_pr_request_review` | forge | Request reviewers |
| `forge_react` | forge | Add emoji reaction |
| `forge_search` | forge | Search code and issues |
| **Web** | | |
| `web_search` | web | Search via SearXNG or Brave |
| `web_fetch` | web | Extract readable content from URLs |
| **Files** | | |
| `file_read` | files | Read file contents |
| `file_write` | files | Write file contents |
| `file_edit` | files | Edit file with diff |
| `file_list` | files | List directory contents |
| `file_search` | files | Search for files by name |
| `file_grep` | files | Search file contents with regex |
| `file_stat` | files | Get file metadata |
| `file_tree` | files | Directory tree view |
| `create_temp_file` | files | Create a temporary file |
| **Shell** | | |
| `exec` | shell | Host shell command execution |
| **Delegation** | | |
| `thane_delegate` | always | Delegate tasks to local models |
| **Utility** | | |
| `get_version` | always | Agent version and build info |
| `cost_summary` | always | Token usage and cost summary |

**MCP Tools (via Model Context Protocol):**

Thane hosts MCP servers as subprocesses, bridging their tools into the agent loop. Example: [ha-mcp](https://github.com/karimkhaleel/ha-mcp) provides 90+ Home Assistant tools. MCP tools are filtered via `include_tools` and assigned to capability tags.

### Model Router

Score-based routing that selects the right model for each task. Models are scored on quality (1-10), speed (1-10), and cost tier (1-5). Routing hints control selection:

| Hint | Effect |
|------|--------|
| `quality_floor` | Minimum quality score (excludes cheaper models) |
| `prefer_speed` | +15 scoring bonus for models with speed ≥ 7 |
| `local_only` | Restrict to Ollama models (no cloud API calls) |
| `model_preference` | Soft preference for a specific model |
| `mission` | Task category for future routing policies |

**Routing by code path:**

| Path | Quality Floor | Speed Pref | Local Only | Rationale |
|------|:---:|:---:|:---:|-----------|
| Orchestrator (interactive) | — | — | — | User-facing, uses configured default |
| Delegate (general) | 5 | ✓ | ✓ | File checks, web searches — speed matters |
| Delegate (ha) | 4 | ✓ | ✓ | Device control — fast and cheap |
| Session summarizer | 7 | — | ✓ | Long-term memory — quality matters |
| Compaction summarizer | 7 | — | ✓ | In-conversation summaries — routed through router |
| Self-reflection | 7 | — | — | Personality development — allows cloud |
| Scheduled tasks | — | — | — | Per-task model/routing overrides |

### Memory Store

SQLite-backed with optional vector search. Conversations, memory, and scheduler state live in a unified `thane.db` (contacts maintain a separate `contacts.db`):

- **Facts** — Categorized knowledge (user, home, device, routine, preference) with embeddings
- **Conversations** — Full history with tool calls
- **Checkpoints** — Compressed state snapshots triggered by message count, shutdown, or manual request
- **Archives** — Searchable conversation history with full-text search
- **Working Memory** — Per-session scratchpad cleared on session close
- **Episodic Summaries** — Post-session analysis for long-term memory consolidation

### Self-Reflection

Periodic scheduled task that analyzes recent interactions and updates `ego.md` — a machine-readable YAML file tracking behavioral patterns, personality traits, and growth areas. Runs daily on a quality-floor=7 model (typically Sonnet-class) to ensure nuanced self-analysis.

Reflection is constrained to observation, not action: it reads files and memory tags but doesn't execute tools or interact with external systems.

### Operational State Store

Generic namespaced KV store (`internal/opstate/`) for lightweight persistent state:

- Email polling high-water marks (`email_poll/primary:INBOX`)
- Future: feature flags, session preferences, poller cursors

Schema: `(namespace, key, value, updated_at)` with namespace+key primary key. Not for structured domain data — those get their own stores.

### Session Management

Sessions can be explicitly managed by the agent:

- **session_close** — End current session with a `carry_forward` note for the next session
- **session_checkpoint** — Save state without closing (crash recovery)
- **session_split** — Fork the session (post-split messages stay in memory)

Context usage is injected into the system prompt so the agent can monitor its own token consumption and make informed decisions about when to checkpoint or close.

### Thane + Home Assistant (optional)

When configured, deep HA integration via multiple protocols:

- **REST API** — State queries, service calls, template rendering
- **WebSocket API** — Persistent connection for real-time `state_changed` events, area/device/entity registry access. Client-side filtering by entity glob patterns.
- **MCP** — [ha-mcp](https://github.com/karimkhaleel/ha-mcp) server provides 90+ tools for comprehensive HA interaction
- **MQTT** — Thane publishes its own sensor telemetry as HA-discoverable entities. Subscribes to Frigate events for NVR-driven triggers.

### Scheduler

Time-based task execution with cron-style scheduling. Tasks support:

- **Model overrides** — specify which model handles the wake
- **Routing hints** — quality_floor, local_only per task
- **Wake payloads** — message string injected into agent context
- Built-in tasks: `periodic_reflection`, `email_poll`

### MQTT Publisher

Thane publishes operational telemetry as HA-discoverable MQTT entities:

- `sensor.aimee_thane_uptime` — service uptime
- `sensor.aimee_thane_tokens_today` — daily token consumption
- `sensor.aimee_thane_default_model` — current routing model
- `sensor.aimee_thane_last_request` — timestamp of last interaction
- `sensor.aimee_thane_version` — running version

### Notification System

Provider-agnostic notification delivery with human-in-the-loop (HITL) callback support (`internal/notifications/`):

- **Fire-and-forget** — `send_notification` delivers messages through configured providers (currently HA companion app push)
- **Actionable notifications** — `request_human_decision` sends notifications with response buttons and tracks callbacks via UUIDv7 request IDs
- **Timeout handling** — configurable per-request timeouts with auto-execute, escalation (re-send at urgent priority), or cancel behaviors
- **Provider interface** — `NotificationProvider` abstracts delivery; new channels implement `Send` and `SendActionable`
- **Contact-aware routing** — recipient resolution through the contact directory; delivery channel selected from contact properties
- **Callback dispatch** — MQTT-based action button responses route back to the originating session or spawn a delegate task if the session has rotated

### CardDAV Server

Native contact sync via CardDAV protocol (`internal/carddav/`):

- Backed by the contacts store — no separate data source
- Implements RFC 6352 via `emersion/go-webdav`
- Enables macOS Contacts.app, iOS, and Thunderbird to sync with Thane's contact directory
- Basic Auth with configurable credentials
- Trust zone-aware vCard export (`FilterCardForTrustZone` strips sensitive fields for lower zones)
- VCF import/export tools for bulk operations (`import_vcf`, `export_vcf`, `export_vcf_qr`, `export_all_vcf`)
- Dynamic rebind loop for interfaces that appear after startup (Tailscale, VPN)

### Media Feeds & Analysis

RSS/Atom feed monitoring and media content analysis (`internal/media/`):

- **Feed polling** — periodic checks for new entries on followed feeds (RSS, Atom, YouTube channels via yt-dlp URL discovery)
- **Transcript extraction** — fetches subtitles and transcripts from YouTube, podcasts, and other yt-dlp-supported sources
- **Analysis vault** — structured analysis output to Obsidian-compatible markdown files with YAML frontmatter
- **Engagement tracking** — records which content has been analyzed for dedup and follow-up
- **VTT cleaning** — strips timing lines, HTML tags, and deduplicates rolling captions from auto-generated subtitles
- **Feed management tools** — `media_follow`, `media_unfollow`, `media_feeds`, `media_transcript`, `media_save_analysis`

### Event Bus

In-process publish/subscribe system for operational observability (`internal/events/`):

- Source-tagged events from agent loop, Signal bridge, delegate, email, metacognitive loop, and scheduler
- Event kinds: `request_start`, `llm_call`, `llm_response`, `tool_call`, `tool_done`, `request_complete`, and more
- Nil-safe `Bus` — components publish without guard checks; a nil bus is a no-op
- Powers the web dashboard's real-time execution view

### Web Dashboard

Built-in operational visibility interface (`internal/server/web/`):

- **Overview** — runtime stats, model router info, dependency health, uptime
- **Chat** — interactive web chat interface (OpenAI-compatible streaming)
- **Contacts** — list, search, and inspect contacts with property details
- **Facts** — browse the semantic knowledge store
- **Sessions** — list and inspect conversation sessions with full transcripts and timeline view
- **Tasks** — view scheduled tasks with execution history
- **Anticipations** — monitor active event-driven triggers

Served on the same port as the native API (8080). Uses embedded HTML templates and static assets (htmx).

The session detail page includes a **Session Logs** panel that queries the structured log index for entries matching that session's ID, with level and subsystem filters.

### Structured Logging

Three-layer logging infrastructure (`internal/logging/`):

1. **Self-managed rotation** — `Rotator` implements `io.WriteCloser` with daily file rotation and optional gzip compression. No external log rotation tooling needed.
2. **Context propagation** — `WithLogger`/`FromContext` thread a session-scoped logger through the call chain. Subsystem tags (`agent`, `delegate`, `metacog`, `scheduler`, `signal`, `api`) are attached automatically so every log line carries its origin.
3. **SQLite index** — `IndexHandler` wraps the primary `slog.Handler` and asynchronously indexes every record into a `log_entries` table. Promoted fields (`session_id`, `conversation_id`, `subsystem`, `tool`, `model`, `level`) get their own indexed columns; remaining attributes go into a JSON catch-all.

The index enables fast queries (by session, level, subsystem, time range) without parsing raw log files. A background pruner removes old DEBUG/TRACE entries based on a configurable retention policy (default: 7 days) while preserving raw log files as the canonical record.

## Technology Choices

| Choice | Rationale |
|--------|-----------|
| **Go** | Single binary, excellent concurrency, no runtime deps |
| **SQLite** | Embedded, reliable, proven at scale |
| **YAML** | Human-readable config with env var substitution |
| **OpenAI-compatible API** | Broad ecosystem compatibility |
| **Markdown talents** | Transparent, editable, version-controlled behavior |
| **goldmark** | Markdown → HTML for email MIME conversion |
| **go-message** | RFC 5322 MIME parsing and composition |
| **go-imap/v2** | Modern IMAP client with extension support |
| **go-vcard** | vCard parsing and serialization for contact import/export |
| **go-webdav** | CardDAV server implementation (RFC 6352) |
| **go-qrcode** | QR code generation for vCard contact sharing |
| **htmx** | Lightweight frontend interactivity for the web dashboard |

## Deployment

### Standalone Binary

```bash
just build
./dist/thane-*/thane -config config.yaml serve
```

### Service Installation

**macOS** — User launch agent (no sudo):
```bash
just install && just service-install
```

**Linux** — systemd with dedicated user and security hardening:
```bash
sudo just install && sudo just service-install
```

See [README.md](README.md) for detailed deployment instructions.

## License

Apache 2.0 — aligned with Home Assistant.
