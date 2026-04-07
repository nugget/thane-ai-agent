# API & Endpoints

Thane serves three network listeners simultaneously from a single binary.

## Port 8080 — Native API

OpenAI-compatible `/v1/chat/completions` endpoint. This is the primary
interface for direct integration, development, and the built-in web
experience.

**Chat API:** Accepts standard OpenAI chat completion requests with
streaming support. The `model` field selects a
[routing profile](../operating/routing-profiles.md) (e.g., `thane:latest`,
`thane:premium`).

**Web Dashboard:** Served on the same port at the root path. Includes:
- **Overview** — runtime stats, dependency health, model router info
- **Chat** — interactive web chat interface with streaming
- **Contacts** — browse and inspect the contact directory
- **Facts** — search the semantic knowledge store
- **Sessions** — list sessions with full transcripts and timeline view
- **Tasks** — view scheduled tasks with execution history

The dashboard uses embedded HTML templates and htmx for lightweight
interactivity. No build step, no JavaScript framework.

**Health endpoint:** Available for service monitoring.

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
