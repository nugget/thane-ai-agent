# API & Endpoints

Thane serves three network listeners simultaneously from a single binary.

## Port 8080 — Native API

Port 8080 serves the OpenAI-compatible native API and the embedded Cognition
Engine dashboard. The dashboard has no build step and fetches JSON/SSE
endpoints from the same listener.

### Chat and Models

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat completions with streaming support. The `model` field selects a [routing profile](../operating/routing-profiles.md), such as `thane:latest` or `thane:premium`. |
| `POST` | `/v1/chat` | Minimal JSON chat endpoint for simple testing. |
| `GET` | `/v1/models` | OpenAI-compatible model list. |

### Runtime and Web Dashboard

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` | Embedded Cognition Engine dashboard. |
| `GET` | `/health` | Dependency health for service monitoring. |
| `GET` | `/v1/version` | Build and runtime metadata. |
| `GET` | `/api/system` | Dashboard aggregate: health, uptime, version, model registry, router stats, Anthropic rate limits, loop definitions, and capability catalog. |
| `GET` | `/api/system/logs` | Structured log tail across the runtime, excluding API/web feedback noise. |
| `GET` | `/api/loops` | Live loop status snapshots, including recent iterations, active tags, tooling, and config. |
| `GET` | `/api/loops/events` | SSE stream. Sends an initial loop snapshot, then loop and delegate events. |
| `GET` | `/api/loops/{id}/logs` | Structured logs scoped to a loop's recent conversation IDs. |
| `GET` | `/api/loop-definitions` | Effective durable loop-definition registry view, including runtime state. |
| `GET` | `/api/capabilities` | Resolved capability catalog. Pass `include=excluded` to include operator-excluded tools. |
| `GET` | `/api/capabilities/{tag}` | Single capability entry. |
| `GET` | `/api/request-detail/_probe` | Request-detail availability probe. |
| `GET` | `/api/requests/{id}` | Live or retained request detail: prompt, messages, tool calls, results, and token metadata. |

### Router, Registry, and History

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/router/stats` | Router statistics, including Anthropic rate-limit snapshot when available. |
| `GET` | `/v1/router/audit` | Recent routing decisions. |
| `GET` | `/v1/router/explain/{requestId}` | Routing decision for one request ID. |
| `GET` | `/v1/model-registry` | Effective model registry snapshot. |
| `POST` | `/v1/model-registry/policy` | Set a deployment policy. |
| `DELETE` | `/v1/model-registry/policy?deployment=...` | Clear a deployment policy. |
| `POST` | `/v1/model-registry/resource-policy` | Set a resource policy. |
| `DELETE` | `/v1/model-registry/resource-policy?resource=...` | Clear a resource policy. |
| `GET` | `/v1/contacts` | List or search contacts. Supports `query`, `kind`, `trust_zone`, `property`, `value`, `exact=true`, and `limit` (default 100, max 500). |
| `GET` | `/v1/contacts/{id}` | Get one contact with structured properties. |
| `POST` | `/v1/contacts` | Create a contact with optional vCard-style `properties`. |
| `PUT` | `/v1/contacts/{id}` | Replace a contact and its structured properties. |
| `DELETE` | `/v1/contacts/{id}` | Soft-delete a contact. |
| `GET` | `/v1/loop-definitions` | Effective durable loop-definition registry view. |
| `GET` | `/v1/loop-definitions/{name}` | One loop definition. |
| `POST` | `/v1/loop-definitions` | Upsert a mutable overlay loop definition. |
| `DELETE` | `/v1/loop-definitions/{name}` | Delete a mutable overlay loop definition. |
| `POST` | `/v1/loop-definitions/policy` | Set a loop-definition policy. |
| `DELETE` | `/v1/loop-definitions/policy?name=...` | Clear a loop-definition policy. |
| `POST` | `/v1/loop-definitions/{name}/launch` | Launch a stored loop definition. |
| `GET` | `/v1/conversations` | Conversation summaries. |
| `GET` | `/v1/conversations/{id}` | Conversation detail. |
| `GET` | `/v1/tools/calls` | Tool-call history. |
| `GET` | `/v1/tools/stats` | Tool usage stats. |
| `GET` | `/v1/session/stats` | Current session usage and context stats. |
| `GET` | `/v1/usage/summary` | Usage summary over a time window. |
| `POST` | `/v1/session/balance` | Set reported balance for session cost tracking. |
| `POST` | `/v1/session/reset` | Reset current session stats. |
| `POST` | `/v1/session/compact` | Compact current session history. |
| `GET` | `/v1/session/history` | Current session history. |
| `GET` | `/v1/archive/sessions` | Archived session list. |
| `GET` | `/v1/archive/sessions/{id}` | Archived session detail. |
| `GET` | `/v1/archive/sessions/{id}/export` | Export one archived session. |
| `GET` | `/v1/archive/search` | Full-text archive search. |
| `GET` | `/v1/archive/messages` | Archived message query. |
| `GET` | `/v1/archive/stats` | Archive statistics. |

### Checkpoints and Companion Apps

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/checkpoint` | Create a checkpoint. |
| `GET` | `/v1/checkpoints` | List checkpoints. |
| `GET` | `/v1/checkpoint/{id}` | Get checkpoint metadata/detail. |
| `DELETE` | `/v1/checkpoint/{id}` | Delete a checkpoint. |
| `POST` | `/v1/checkpoint/{id}/restore` | Restore from a checkpoint. |
| `GET` | `/v1/companion/ws` | Native companion app WebSocket. |
| `GET` | `/v1/platform/ws` | Legacy companion WebSocket alias. |

## Port 11434 — Ollama-Compatible API

Speaks the Ollama chat API so Home Assistant's native Ollama integration
connects without modification. From HA's perspective, Thane *is* an Ollama
instance.

When HA sends a conversation to this port, Thane:

1. Strips HA's injected tools and system prompts
2. Maps the requested model name to a routing profile
3. Processes through the full agent loop
4. Returns the response in Ollama's expected format

Available models are listed at `GET /api/tags`. Each
[routing profile](../operating/routing-profiles.md) appears as a model
(e.g., `thane:latest`, `thane:command`, `thane:premium`).

## Port 8843 — CardDAV Server

Native contact sync via the CardDAV protocol (RFC 6352). Backed by the
contacts store — no separate data source.

**Compatible clients:** macOS Contacts.app, iOS Contacts, Thunderbird,
any CardDAV client.

**Authentication:** Basic Auth with credentials configured in
`contacts.carddav` config section.

**Trust-zone aware:** vCard export respects trust zones — lower-trust
contacts have sensitive fields stripped via `FilterCardForTrustZone`.

**Dynamic rebind:** Handles interfaces that appear after startup (Tailscale,
VPN) by periodically retrying the bind.

## Connecting Home Assistant

1. In HA: **Settings > Devices & Services > Add Integration > Ollama**
2. Set URL to `http://thane-host:11434`
3. Select model `thane:latest`
4. Under **Voice Assistants**, set the conversation agent to this integration

See [Home Assistant](../operating/homeassistant.md) for the full setup guide.
