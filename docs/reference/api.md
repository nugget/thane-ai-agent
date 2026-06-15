# API & Endpoints

Thane serves four network listeners simultaneously from a single binary.

## Port 8080 — Native API

Port 8080 serves the Thane-native API and the embedded Cognition Engine
dashboard. The dashboard has no build step and fetches JSON/SSE endpoints from
the same listener. The OpenAI-compatible shim runs on its own port (see below).

### Chat

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/chat` | Minimal JSON chat endpoint for simple testing. |

### Runtime and Web Dashboard

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` | Embedded Cognition Engine dashboard. |
| `GET` | `/health` | Dependency health for service monitoring. |
| `GET` | `/v1/version` | Build and runtime metadata. |
| `GET` | `/v1/system` | Slim system rollup: status, dependency health, `uptime_seconds`, version. |
| `GET` | `/v1/system/logs` | Structured process-log tail (bare array, newest first; `?level`, `?limit` default 50, max 200). |

### Router, Registry, and History

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/insights/router` | Router stats (with Anthropic rate-limit snapshot) plus the recent routing-audit trail (`?limit`, default 20). |
| `GET` | `/v1/requests/{id}` | Detail for one model turn: prompt, messages, tool calls, token metadata. |
| `GET` | `/v1/requests/{id}/routing` | Router decision trace for the request (replaces `/v1/router/explain`). |
| `GET` | `/v1/requests/{id}/tools` | Tool calls made during the request (bare array). |
| `GET` | `/v1/models` | Native fleet view: deployable models with resource, provider, and routability (bare array). |
| `GET` | `/v1/models/registry` | Effective model registry snapshot. |
| `PUT` | `/v1/models/registry/policy` | Set a deployment policy. |
| `DELETE` | `/v1/models/registry/policy?deployment=...` | Clear a deployment policy. |
| `PUT` | `/v1/models/registry/resource-policy` | Set a resource policy. |
| `DELETE` | `/v1/models/registry/resource-policy?resource=...` | Clear a resource policy. |
| `GET` | `/v1/contacts` | List or search contacts. Supports `query`, `kind`, `trust_zone`, `property`, `value`, `exact=true`, and `limit` (default 100, max 500). |
| `GET` | `/v1/contacts/{id}` | Get one contact with structured properties. |
| `POST` | `/v1/contacts` | Create a contact with optional vCard-style `properties`. |
| `PUT` | `/v1/contacts/{id}` | Replace a contact and its structured properties. |
| `DELETE` | `/v1/contacts/{id}` | Soft-delete a contact. |
| `GET` | `/v1/loops` | Running loop status snapshots. Optional `?state=` filter (`pending`, `sleeping`, `waiting`, `processing`, `error`, `stopped`). |
| `GET` | `/v1/loops/{id}` | One running loop's status. |
| `GET` | `/v1/loops/{id}/logs` | Structured logs for a running loop's recent conversation IDs (bare array, newest first; `?limit=` default 50, max 200). |
| `GET` | `/v1/loops/events` | SSE stream: initial loop snapshot, then loop and delegate events. |
| `GET` | `/v1/loop-definitions` | Effective durable loop-definition registry view. |
| `GET` | `/v1/loop-definitions/{name}` | One loop definition. |
| `POST` | `/v1/loop-definitions` | Upsert a mutable overlay loop definition. |
| `DELETE` | `/v1/loop-definitions/{name}` | Delete a mutable overlay loop definition. |
| `POST` | `/v1/loop-definitions/policy` | Set a loop-definition policy. |
| `DELETE` | `/v1/loop-definitions/policy?name=...` | Clear a loop-definition policy. |
| `POST` | `/v1/loop-definitions/{name}/launch` | Launch a stored loop definition. |
| `GET` | `/v1/conversations` | Conversation summaries. |
| `GET` | `/v1/conversations/{id}` | Conversation detail. |
| `GET` | `/v1/insights/tools` | Tool-call stats plus recent tool calls (`?tool`, `?conversation_id`, `?limit` default 50). |
| `GET` | `/v1/session/stats` | Current session usage and context stats. |
| `GET` | `/v1/insights/usage` | Token/cost usage summary over a time window (`?hours`, default 24). |
| `GET` | `/v1/insights/capabilities` | Resolved capability-tag catalog (`?include=excluded` to surface operator-disabled tools). |
| `GET` | `/v1/insights/capabilities/{tag}` | One capability tag's resolved view (404 when absent). |
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

## Port 8081 — OpenAI-Compatible API

A dedicated listener for the frozen OpenAI-compatible shim, kept off the native
`/v1` namespace so the two surfaces don't collide (mirrors the Ollama split).
Enabled via `openai_api` in config. The `model` field selects a
[virtual model](../operating/routing-profiles.md) such as `thane:latest` or
`thane:premium`.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat completions with streaming support. |
| `GET` | `/v1/models` | OpenAI-compatible model list (routing aliases as model ids). |

## Port 11434 — Ollama-Compatible API

Speaks the Ollama chat API so Home Assistant's native Ollama integration
connects without modification. From HA's perspective, Thane *is* an Ollama
instance.

When HA sends a conversation to this port, Thane:

1. Strips HA's injected tools and system prompts
2. Maps the requested model name to a virtual model
3. Processes through the full agent loop
4. Returns the response in Ollama's expected format

Available models are listed at `GET /api/tags`. Each exposed
[virtual model](../operating/routing-profiles.md) appears
(e.g., `thane:latest`, `thane:premium`, `thane:assist`).

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
