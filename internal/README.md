# internal/

Thane's internal packages, organized by architectural role.

## How the agent thinks

| Package | Purpose |
|---------|---------|
| `agent/` | Core loop: assembles context, plans via LLM, executes tools, returns responses |
| `llm/` | LLM provider abstraction (Anthropic, Ollama) and streaming |
| `router/` | Model selection — picks the best model for each request based on quality, cost, and hints |
| `prompts/` | System prompt templates for all agent modes (main, delegate, compaction, extraction) |
| `talents/` | Capability tag system — markdown-based behavior guidance that scopes tool access |
| `delegate/` | Delegate task execution — isolated sub-agent runs with their own tool sets |
| `metacognitive/` | Autonomous self-reflection loop that monitors state and adjusts its own attention cycle |
| `tools/` | Tool registry and all tool handler implementations |

## What the agent knows

| Package | Purpose |
|---------|---------|
| `memory/` | Conversation storage, compaction, archiving, episodic memory, and session summarization |
| `knowledge/` | Structured entity facts, semantic search via embeddings, and document ingestion |
| `contacts/` | Contact directory, trust zones, presence tracking, and relationship metadata |
| `awareness/` | System prompt context providers — current conditions, HA state window, entity watchlist |

## How the agent communicates

| Package | Purpose |
|---------|---------|
| `channels/email/` | Email integration (IMAP polling, SMTP sending, trust-based filtering) |
| `channels/signal/` | Signal messenger bridge (receives messages, routes to agent, sends replies) |
| `channels/mqtt/` | MQTT for Home Assistant device discovery and sensor state publishing |
| `server/api/` | REST API — OpenAI-compatible and Ollama-compatible endpoints |
| `server/web/` | Built-in web dashboard and chat UI |

## Domain integrations

| Package | Purpose |
|---------|---------|
| `homeassistant/` | Home Assistant REST + WebSocket client |
| `unifi/` | UniFi network client (device tracking, AP associations) |
| `forge/` | GitHub and Forgejo integration (issues, PRs, repo management) |
| `mcp/` | Model Context Protocol client and tool bridge |
| `media/` | Media transcript extraction, RSS/Atom feed polling, and content summarization |
| `search/` | Web search providers (Brave, SearXNG) and page content extraction |

## Platform

| Package | Purpose |
|---------|---------|
| `config/` | Configuration loading, validation, and defaults |
| `database/` | Shared SQLite helpers (WAL mode, busy timeout, schema migration) |
| `scheduler/` | Task scheduling (time-based and condition-based via anticipations) |
| `opstate/` | Operational state key-value store with TTLs |
| `events/` | In-process publish/subscribe event bus |
| `checkpoint/` | State snapshots for crash recovery |
| `usage/` | LLM token usage and cost tracking |
| `httpkit/` | Shared HTTP client construction (User-Agent, timeouts) |
| `connwatch/` | Service health monitoring with exponential backoff |
| `buildinfo/` | Build metadata injected via ldflags |
| `paths/` | Named path prefix resolution (e.g., `kb:file.md`) |
