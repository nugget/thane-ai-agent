# internal/

Thane's internal packages are organized by architectural role. The
directory layout is meant to answer the first question future humans and
agents ask: "what kind of thing am I looking at?"

## Placement Rules

- `app/` is the composition root. It may wire broad subsystems together.
- `server/` is the local API/UI surface.
- `tools/` owns tool runtime primitives: registry, context plumbing,
  provider interfaces, common errors, and shared tool helpers.
- Domain-specific tool behavior belongs with the domain package whenever
  practical. Do not add new domain tool families to `tools/` by default.
  Existing domain tool families still live in `tools/` and should migrate
  incrementally when there is a focused reason to move them.
- Packages should generally import inward toward platform/model/state
  primitives, not sideways through unrelated domains.

## Runtime

Execution machinery for agent runs and child work.

| Package | Purpose |
|---------|---------|
| `runtime/agent/` | Core agent loop: context assembly, model calls, tool execution, responses |
| `runtime/loop/` | Durable loop runtime, loop definitions, launch/request bridge |
| `runtime/delegate/` | Delegate task execution and child-loop handoff |
| `runtime/iterate/` | Model/tool iteration engine |
| `runtime/metacognitive/` | Autonomous self-reflection loop |

## Model Interface

How Thane talks to models and shapes model-facing context.

| Package | Purpose |
|---------|---------|
| `model/llm/` | LLM client contracts, messages, streaming, tool-call parsing |
| `model/fleet/` | Model fleet catalog, provider clients, registry, runtime inventory |
| `model/router/` | Model selection and routing profiles |
| `model/prompts/` | Prompt templates and runtime contracts |
| `model/talents/` | Markdown behavioral guidance loaded by capability tags |
| `model/toolcatalog/` | Capability tag catalog and model-facing tool summaries |
| `model/promptfmt/` | Shared prompt-formatting helpers |

## State

Durable knowledge, persisted projections, and assembled situational
context the agent reasons over.

| Package | Purpose |
|---------|---------|
| `state/memory/` | Conversations, archives, compaction, episodic memory |
| `state/contacts/` | Contact directory, trust zones, CardDAV-facing records |
| `state/documents/` | Managed document roots, queries, mutation helpers |
| `state/knowledge/` | Structured facts, embeddings, semantic search |
| `state/awareness/` | Agent-facing situational context: current conditions, HA state windows, watchlists |
| `state/attachments/` | Attachment storage and metadata |

## Channels

Entrypoints and communication surfaces that can wake, converse, notify,
or carry human/event interaction.

`channels/messages/` is the in-process envelope and bus substrate shared
by channel adapters and runtime loop signaling. It lives here because it
defines message-channel contracts, even though it is not an external
adapter like Signal or email.

| Package | Purpose |
|---------|---------|
| `channels/messaging/signal/` | Signal request/reply bridge and provider-specific messaging tools |
| `channels/email/` | Email polling, reading, sending, trust filtering |
| `channels/mqtt/` | MQTT event/wake subscriptions and HA discovery publishing |
| `channels/messages/` | In-process message envelopes and buses |
| `channels/notifications/` | Notification routing, actionable callbacks, timeout escalation |

## Integrations

External systems exposed as capabilities or provider-backed tool surfaces.

| Package | Purpose |
|---------|---------|
| `integrations/homeassistant/` | Home Assistant REST/WebSocket client and state projections |
| `integrations/unifi/` | UniFi network client and device tracking |
| `integrations/forge/` | GitHub/Forgejo integration for issues, PRs, repos |
| `integrations/media/` | Feeds, transcripts, media vault, summarization |
| `integrations/search/` | Web search providers and page extraction |
| `integrations/mcp/` | Model Context Protocol client and tool bridge |
| `integrations/companion/` | Native companion app WebSocket endpoint |

## Platform

Cross-cutting process, storage, scheduling, and operational substrate.

| Package | Purpose |
|---------|---------|
| `platform/config/` | Configuration loading, validation, defaults, generated example config |
| `platform/database/` | Shared SQLite helpers and timestamp parsing |
| `platform/logging/` | Structured logging, request archival, queryable log index |
| `platform/events/` | In-process publish/subscribe event bus |
| `platform/opstate/` | Operational state key-value store with TTLs |
| `platform/paths/` | Named path prefix resolution, such as `kb:file.md` |
| `platform/httpkit/` | Shared HTTP client construction and response helpers |
| `platform/scheduler/` | Time-based task scheduling |
| `platform/usage/` | LLM token usage and cost accounting |
| `platform/telemetry/` | Runtime telemetry collection |
| `platform/checkpoint/` | State snapshots for crash recovery |
| `platform/buildinfo/` | Build metadata injected via ldflags |
| `platform/provenance/` | Tool-call and content provenance helpers |

## API And Composition

| Package | Purpose |
|---------|---------|
| `app/` | Wires configuration, stores, providers, tools, loops, and servers |
| `server/api/` | OpenAI-compatible, Ollama-compatible, and admin HTTP APIs |
| `server/web/` | Built-in web dashboard and chat UI |
| `server/carddav/` | CardDAV server for native contact app sync |
