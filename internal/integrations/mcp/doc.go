// Package mcp is Thane's MCP (Model Context Protocol) client. It
// connects to external MCP servers and exposes their tools to the agent
// loop, and so to delegates, as ordinary Thane tools. Thane is only ever the
// client; it does not act as an MCP server.
//
// # Transports
//
// Two transports carry JSON-RPC 2.0: stdio (a subprocess in its own
// process group, newline-delimited messages, many concurrent requests)
// and Streamable HTTP (one POST per message, answered with JSON or an
// SSE stream). Each Transport value is one connection lifetime; a
// reconnect builds a new one through a transportFactory.
//
// # Protocol eras
//
// Servers speak one of two protocol eras:
//
//   - legacy (2024-11-05 through 2025-11-25): an initialize handshake
//     followed by notifications/initialized; HTTP servers may hold a
//     session (Mcp-Session-Id).
//   - modern (2026-07-28): stateless; every request carries the protocol
//     version, client info and capabilities in params._meta, and the
//     server describes itself through server/discover.
//
// With protocol "auto" the client probes with server/discover and falls
// back to initialize when the reply shows a legacy server; "modern" and
// "legacy" pin the era. Health checks are era-correct: ping for legacy,
// server/discover for modern.
//
// # Lifecycle
//
// A [Manager] owns every configured server. Each server gets a
// supervisor goroutine that connects, negotiates, lists tools (paginated)
// and reconnects with backoff when the connection is lost or the
// session expires. [Manager.Start] connects all servers in parallel and,
// after the first attempts finish (bounded), registers the tools of
// servers with a tool list on the shared registry, before capability
// tags are resolved, so tag resolution and the capability views see
// them. Tools that appear later (a server that came up late, a
// tools/list_changed notification, an expired modern list ttl), and new
// definitions of registered tools whose description or schema changed,
// are served by [Manager.Snapshot], an agent.DynamicToolSource overlay
// the agent loop applies to every run. Delegates run through the same
// loop, so these tools reach the main loop and any delegate whose tag
// scope covers them. Only what is computed from the shared registry alone (a
// delegate's model routing by tool count, tag_inspect and the manifest
// tool counts) reflects the tools registered at Start.
//
// Bridged tools are named mcp_{server}_{tool} (sanitized, capped at 64
// bytes with a stable hash suffix) and carry CanonicalID
// mcp:{server}/{tool}, Source "mcp" and Origin {server}. Their handlers
// route through the live client: a server that is down answers
// tools.ErrUnavailable, and every other failure is classified into
// fixed, actionable model-facing text that never includes hosts, URLs or
// raw HTTP bodies. Results are rendered to text (structuredContent
// preferred) and capped.
//
// Deferred: OAuth (only static headers authenticate), the legacy HTTP
// GET notification stream, SSE resumption, the deprecated 2024-11-05
// HTTP+SSE transport, subscriptions/listen, resources, prompts,
// completion, tasks, injecting server instructions into prompts, and
// multimodal tool results.
package mcp
