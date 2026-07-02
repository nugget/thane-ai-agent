// Package legacyroute is the single source of truth for deprecated API
// route aliases kept for backward compatibility. Both the mux route
// wiring (internal/server/api) and the OpenAPI route-coverage allowlist
// (internal/server/openapi) derive from [Aliases], so the routes and the
// test that guards them cannot drift apart, and a date-based gate
// (legacyroute_test.go) fails once an alias is past its removal window —
// turning "cull someday" into a scheduled obligation.
//
// The pattern is path-type-agnostic: today every entry is a WebSocket
// upgrade alias, but any deprecated REST path can be registered the same
// way.
package legacyroute

import (
	"net/http"
	"strconv"
	"time"
)

// dateLayout is the day-granularity form for the DeprecatedSince and
// RemoveAfter fields. Day granularity is deliberate: a deprecation
// window is a calendar policy, not a wall-clock instant.
const dateLayout = "2006-01-02"

// Alias is one deprecated route kept as a compatibility shim for an
// existing client, pointing at the canonical replacement.
type Alias struct {
	Method          string // HTTP method, e.g. "GET"
	Path            string // deprecated path, e.g. "/v1/companion/ws"
	Canonical       string // the path clients should move to
	DeprecatedSince string // dateLayout; when the alias was announced deprecated
	RemoveAfter     string // dateLayout; the alias may be culled once this date has passed
	Issue           int    // tracking issue number
}

// Aliases is the registry of live deprecated routes. Keep RemoveAfter
// honest: a first-party client (thane-agent-macos) gets a six-month
// window from the deprecation date. When telemetry shows a route's usage
// has dropped to zero past its window, one PR drops the entry here — the
// route wiring and the coverage allowlist follow automatically, and the
// overdue-window gate goes green by the entry's absence.
var Aliases = []Alias{
	{Method: "GET", Path: "/v1/companion/ws", Canonical: "/v1/realtime/ws", DeprecatedSince: "2026-06-25", RemoveAfter: "2026-12-25", Issue: 1081},
	{Method: "GET", Path: "/v1/platform/ws", Canonical: "/v1/realtime/ws", DeprecatedSince: "2026-06-25", RemoveAfter: "2026-12-25", Issue: 1081},
}

// Route returns the "METHOD /path" key used by the mux and the OpenAPI
// route-coverage allowlist.
func (a Alias) Route() string { return a.Method + " " + a.Path }

// DeprecatedSinceTime parses DeprecatedSince at UTC midnight. It panics
// on a malformed literal: the registry is compile-time author-controlled
// data that legacyroute_test.go validates, so a parse failure is a
// programming error, never a runtime condition.
func (a Alias) DeprecatedSinceTime() time.Time { return mustDate(a.DeprecatedSince) }

// RemoveAfterTime parses RemoveAfter at UTC midnight, with the same
// panic-on-malformed contract as [Alias.DeprecatedSinceTime].
func (a Alias) RemoveAfterTime() time.Time { return mustDate(a.RemoveAfter) }

func mustDate(s string) time.Time {
	t, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		panic("legacyroute: malformed date " + strconv.Quote(s) + ": " + err.Error())
	}
	return t
}

// DeprecationHeaders returns the standards-based response headers that
// signal this alias's deprecation to clients:
//
//   - Deprecation (RFC 9745): a Structured Field Date, "@<unix-seconds>"
//     at the deprecation date.
//   - Sunset (RFC 8594): an HTTP-date at the removal date.
//   - Link: rel="successor-version" pointing at the canonical path so a
//     client can discover the replacement programmatically.
func (a Alias) DeprecationHeaders() http.Header {
	h := http.Header{}
	h.Set("Deprecation", "@"+strconv.FormatInt(a.DeprecatedSinceTime().Unix(), 10))
	h.Set("Sunset", a.RemoveAfterTime().UTC().Format(http.TimeFormat))
	h.Set("Link", "<"+a.Canonical+">; rel=\"successor-version\"")
	return h
}

// Lookup returns the alias registered for path, if any. Callers use it to
// decide whether an inbound request arrived on a deprecated route.
func Lookup(path string) (Alias, bool) {
	for _, a := range Aliases {
		if a.Path == path {
			return a, true
		}
	}
	return Alias{}, false
}
