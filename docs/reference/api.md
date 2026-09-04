# API & Endpoints

Thane can serve up to six network listeners from a single binary. The native
API (port 8080) is always on; the OpenAI-compatible (8081), Ollama-compatible
(11434), and CardDAV (8843) listeners are each optional, enabled via config.
An optional HTTPS front door (443, plus a redirect-only listener on 80) terminates TLS
in-process and routes each configured hostname to the native, Ollama, or
OpenAI surface, reaching exactly the routes and guards that surface has on
its plaintext port; see
[HTTPS Front Door](../operating/configuration.md#https-front-door).

Every HTTP listener refuses state-changing requests (`POST`, `PUT`, `DELETE`,
`PATCH`) that a browser marks as cross-origin, via the `Sec-Fetch-Site` and
`Origin` request headers, with a `403` and an `{"error": ...}` body. Requests
without those headers, which is every non-browser client, are unaffected. See
[Listen Addresses](../operating/configuration.md#listen-addresses).

Request bodies are capped at 8 MiB on the native API and 32 MiB on the
compatibility shims (room for chat history with base64 images); a body past
the cap fails the request and closes the connection. All listeners bound
header read time, idle keep-alive time, and header size.

## Port 8080 — Native API

Port 8080 serves the Thane-native API and the embedded Cognition Engine
dashboard's static assets (no build step). The dashboard consumes its JSON and
SSE entirely from the native `/v1` API (graph, process table, and forensics
views). The OpenAI-compatible shim runs on its own port (see below).

### Authentication

The native API is gated whenever `listen.auth.tokens` holds at least one
token. Clients send `Authorization: Bearer <token>`; an operator token
authenticates as its label, a companion account token
(`companion.providers.<account>.tokens`) as that account, and a client
certificate the HTTPS front door verified as its subject. A missing or wrong
credential gets `401` with `WWW-Authenticate: Bearer realm="thane"`.

A companion account token is not an operator credential. It authenticates a
device offering data, and it reaches the companion surface — `/v1/realtime/ws`
and its aliases, `POST /v1/companion/observations` — and nothing else gated:
every other gated route answers `403` with an error of type `forbidden`.
The allowlist is deny-by-default, so a route added later is closed to
companions until it is named on purpose, and a test derives the companion
surface from the route table to keep the two in step. Public routes are
unaffected, being public to everyone. This matters because that credential
lives in a phone's Keychain and travels further than an operator token.

The routes that serve without a credential are exactly: `GET /health`,
`GET /v1/version`, `GET /v1/identity`, the console shell and `/static/*`,
`/docs*`, the three `/v1/auth/*` endpoints, and the companion endpoints
(`/v1/realtime/ws` and its aliases, `POST /v1/companion/observations`),
which authenticate in-band. A CI test walks the route table and fails if a
route is neither gated nor listed as public on purpose.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/auth/session` | Whether a credential is required and whether the caller has one; names the principal. |
| `POST` | `/v1/auth/login` | Exchange an **operator** token for an HttpOnly, SameSite=Strict `thane_session` cookie (the console's sign-in). A companion token is refused with `403`: a device credential cannot become an operator's browser session. |
| `POST` | `/v1/auth/logout` | Revoke the session and clear the cookie. |

The console never stores a token: it exchanges one at sign-in for the
session cookie, which the browser sends on every fetch and on the SSE
stream. The cookie is marked Secure when the request arrived over TLS,
directly or through a proxy reporting `X-Forwarded-Proto: https`. See
[Listen Addresses](../operating/configuration.md#listen-addresses).

### Chat

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/chat` | Minimal JSON chat endpoint for simple testing. |

### Runtime and Web Dashboard

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` | Embedded Cognition Engine dashboard. |
| `GET` | `/docs` | Interactive OpenAPI explorer (Scalar) for the API. |
| `GET` | `/health` | Dependency health for service monitoring. |
| `GET` | `/v1/version` | Build and runtime metadata. |
| `GET` | `/v1/identity` | Core-backed instance identity, birth and HEAD revisions, anchor posture, and local provenance verification evidence. |
| `GET` | `/v1/system` | Slim system rollup: status, dependency health, `uptime_seconds`, version. |
| `GET` | `/v1/system/logs` | Structured process-log tail (bare array, newest first; `?level`, `?limit` default 50, max 200). |

`GET /v1/identity` reports evidence for clients to pin and evaluate; it does
not issue a remote trust verdict. Its stable instance ID and fingerprints are
recomputed from public material committed in core's single birth commit.
`core.birth.asserted_at` is the time claimed by that signed commit, with
`time_assurance: signed_claim`; it is not an independently witnessed
timestamp. `core.current_commit` is the algorithm-qualified commit ID of the
active core document root and is the canonical forensic anchor for the state
being reported. The endpoint never returns private key material, local
filesystem paths, signer principals, or the contents of `.allowed_signers`.

### Router, Registry, and History

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/telemetry/router` | Router stats (with Anthropic rate-limit snapshot) plus the recent routing-audit trail (`?limit`, default 20). |
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
| `GET` | `/v1/contacts/{id}` | Get one contact with structured properties; property objects include model-turn provenance when known and `null` for legacy or non-model authorship. |
| `POST` | `/v1/contacts` | Create a contact with optional vCard-style `properties`; API-authored properties retain unknown provenance. |
| `PUT` | `/v1/contacts/{id}` | Replace a contact and its structured properties; API-authored properties retain unknown provenance. |
| `DELETE` | `/v1/contacts/{id}` | Soft-delete a contact. |
| `GET` | `/v1/loops` | Running loop status snapshots. Optional `?state=` filter (`pending`, `sleeping`, `waiting`, `processing`, `error`, `stopped`). |
| `GET` | `/v1/loops/{id}` | One running loop's status. |
| `GET` | `/v1/loops/{id}/logs` | Structured logs for a running loop's recent conversation IDs (bare array, newest first; `?limit=` default 50, max 200). |
| `GET` | `/v1/loops/{name}/outputs/{output}` | One declared loop output at a negotiated fidelity: `text/plain` = `status_line` when that signal shape is declared, `application/json` = typed facets + full body, `text/markdown` (default) = document body. A plain-only request is not acceptable for an output without `status_line`. |
| `GET` | `/v1/loops/events` | SSE stream: initial loop snapshot, then loop and delegate events. |
| `GET` | `/v1/schedules` | Scheduler tasks (`at`/`every`/`cron`) each with its next fire time. Optional `?enabled=true`. |
| `GET` | `/v1/schedules/{id}` | One scheduled task. |
| `GET` | `/v1/schedules/{id}/executions` | A task's execution history (bare array, newest first; `?limit` default 50, max 200). |
| `GET` | `/v1/loop-definitions` | Effective durable loop-definition registry view. |
| `GET` | `/v1/loop-definitions/{name}` | One loop definition. |
| `POST` | `/v1/loop-definitions` | Upsert a mutable overlay loop definition. |
| `DELETE` | `/v1/loop-definitions/{name}` | Delete a mutable overlay loop definition. |
| `POST` | `/v1/loop-definitions/policy` | Set a loop-definition policy. |
| `DELETE` | `/v1/loop-definitions/policy?name=...` | Clear a loop-definition policy. |
| `POST` | `/v1/loop-definitions/{name}/launch` | Launch a stored loop definition. |
| `GET` | `/v1/conversations` | Filter/sort/keyset-paginate conversation summaries. Filters: `ids` (comma-sep, max 200), `kind` (comma-sep id-prefix families), `channel`/`contact`/`address` (channel binding), `updated_after`/`updated_before`/`created_after`/`created_before` (RFC3339 or a duration like `1h` meaning "ago"), `min_messages`/`max_messages`, `q` (metadata substring: id/contact name/address — *not* message content; use `/v1/archive/search` for that). `sort` = `updated_at` (default)\|`created_at`\|`message_count`; `order` = `desc` (default)\|`asc`; `limit` default 50, max 200; `cursor` from `next_cursor`. Returns `{conversations, count, total, next_cursor}`. `message_count` is the true active count (previously capped at the per-conversation working-memory limit). |
| `GET` | `/v1/conversations/{id}` | Conversation detail (full transcript). |
| `GET` | `/v1/telemetry/tools` | Tool-call stats plus recent tool calls (`?tool`, `?conversation_id`, `?limit` default 50). |
| `GET` | `/v1/sessions/stats` | Current session usage and context stats. |
| `GET` | `/v1/telemetry/usage` | Token/cost usage summary over a time window (`?hours`, default 24; `?group_by` to break down by a dimension, e.g. model). |
| `GET` | `/v1/telemetry/capabilities` | Resolved capability-tag catalog (`?include=excluded` to surface operator-disabled tools). |
| `GET` | `/v1/telemetry/capabilities/{tag}` | One capability tag's resolved view (404 when absent). |
| `POST` | `/v1/sessions/balance` | Set reported balance for session cost tracking. |
| `POST` | `/v1/sessions/reset` | Reset current session stats. |
| `POST` | `/v1/sessions/compact` | Compact current session history. |
| `GET` | `/v1/sessions/history` | Current session history. |
| `GET` | `/v1/archive/sessions` | Archived session list. |
| `GET` | `/v1/archive/sessions/{id}` | Archived session detail. |
| `GET` | `/v1/archive/sessions/{id}/export` | Export one archived session. |
| `GET` | `/v1/archive/search` | Full-text archive search. |
| `GET` | `/v1/archive/messages` | Archived message query. |
| `GET` | `/v1/archive/stats` | Archive statistics. |
| `POST` | `/v1/archive/contact-dossier-backfill` | Advance one bounded page of the durable, one-time contact-dossier backfill (`?limit`, default 50, max 200). |

The backfill endpoint is an operator operation (`archive:write` in the native
API contract), not a model tool or an autonomous-loop behavior. It freezes a
cutoff on its first call, pages active contact subjects and then historical
closed sessions with durable keyset cursors, and adds catch-up work below fresh
session-close work. Repeat the request until `complete` is true:

```sh
curl -sS -X POST \
  'http://127.0.0.1:8080/v1/archive/contact-dossier-backfill?limit=50'
```

Retries and restarts are safe. Pending subjects and subjects in the retained
queue-completion journal are skipped, while the durable cursor prevents the
already-traversed archive from being scanned again. Once complete, future calls
are no-ops rather than recurring full-archive scans. The archivist is not woken
by the endpoint; it drains the resulting backlog at its normal self-paced
cadence.

### Checkpoints and Companion Apps

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/checkpoints` | Create a checkpoint. |
| `GET` | `/v1/checkpoints` | List checkpoints. |
| `GET` | `/v1/checkpoints/{id}` | Get checkpoint metadata/detail. |
| `DELETE` | `/v1/checkpoints/{id}` | Delete a checkpoint. |
| `POST` | `/v1/checkpoints/{id}/restore` | Restore from a checkpoint. |
| `GET` | `/v1/realtime/ws` | First-party realtime WebSocket (canonical). |
| `GET` | `/v1/companion/ws` | Realtime WebSocket — legacy alias (deprecated; see below). |
| `GET` | `/v1/platform/ws` | Realtime WebSocket — legacy alias (deprecated; see below). |
| `POST` | `/v1/companion/observations` | Submit a bounded latest-value observation batch from an authenticated companion. |

During the realtime handshake, the pre-authentication `auth_required.version`
field identifies the companion protocol version. After successful
authentication, `auth_ok.server_version` identifies the running Thane build
using the same stamped version reported by `GET /v1/version`, while
`auth_ok.server_uptime_seconds` reports whole seconds since that process
started. Both runtime diagnostics are disclosed only after authentication.

`POST /v1/companion/observations` uses a configured companion bearer token,
which determines the account, and a stable opaque `client_id` claim supplied by
the app. The ingestion authenticator resolves that account and claim through
the durable inventory to its immutable server-assigned `device_id`; observation
rows reference that ID rather than a credential or mutable claim. Future
device-key authentication can therefore resolve a verified key to the same
device without rewriting observation history.
The endpoint accepts at most 64 KiB and 16 events; each available event carries
a UUID idempotency key, kind, schema version, device `observed_at`, and a JSON
object payload of at most 32 KiB and 256 properties. Each device may retain at
most 64 distinct latest-value kinds. `observed_at` must be on or after
2000-01-01T00:00:00Z and no more than five minutes ahead of server receipt. A
`status` of `withdrawn` must omit the payload and prevents an earlier sensitive
value from remaining available; at equal timestamps, withdrawal wins. Optional
non-empty device metadata refreshes the durable inventory using server receipt
time as its recency guard.
Successful responses are `202 Accepted` and report stored versus ignored
(duplicate or older) events plus the independent server `received_at`.

The current bearer credential is replayable and is suitable only under Thane's
explicit private-network/Tailscale deployment assumption. Bearer comparisons
on this HTTP path use fixed-size digests and constant-time comparison. A
per-device signed-request replacement is tracked in issue #1444.

### Deprecated route aliases

Deprecated aliases (currently the two legacy WebSocket paths above) are
declared in one place — the `internal/server/legacyroute` registry — which is
the single source for the mux route wiring, the OpenAPI route-coverage
allowlist, and a current-date CI gate. Each entry carries a `DeprecatedSince`
date, a `RemoveAfter` date (a six-month window for first-party clients), and its
tracking issue.

A connection on a deprecated alias is logged (`legacy websocket alias in use`,
tagged with path, account, and client id) so usage can be watched to zero before
removal, and the client is signalled three ways: an RFC 8594 `Sunset` header and
RFC 9745 `Deprecation` header on the upgrade response, a `Link` header with
`rel="successor-version"` pointing at the canonical path, and an in-band
`deprecation` object in the `auth_ok` message for clients that don't surface
handshake headers. A CI test (comparing the current date, since `go test` has no
build stamp) fails once an alias is past its `RemoveAfter` date, forcing either
removal or a justified extension; culling is a one-line registry edit that drops
the route and the allowlist entry together.

## Port 8081 — OpenAI-Compatible API

A dedicated listener for the frozen OpenAI-compatible shim, kept off the native
`/v1` namespace so the two surfaces don't collide (mirrors the Ollama split).
Enabled via `openai_api` in config. The `model` field selects a
[virtual model](../operating/routing-profiles.md) such as `thane:latest` or
`thane:premium`.

**Authentication:** optional bearer token, `openai_api.api_key`. When set,
every request must carry `Authorization: Bearer <key>`, the header OpenAI
client libraries send by default; a missing or wrong token gets a `401` in
the OpenAI error envelope with a `WWW-Authenticate: Bearer realm="openai"`
challenge. When unset the surface is open to every host that can reach the
port, and it drives the full agent loop, so set a key unless the network
boundary already enforces who may connect. The Ollama shim has the same
option (`ollama_api.api_key`); it is off by default there because Home
Assistant's Ollama integration cannot send a bearer token.

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
