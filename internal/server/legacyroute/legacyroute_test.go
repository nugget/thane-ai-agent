package legacyroute

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistryWellFormed(t *testing.T) {
	seen := make(map[string]bool)
	for _, a := range Aliases {
		if a.Method == "" || !strings.HasPrefix(a.Path, "/") {
			t.Errorf("alias %+v: Method must be set and Path must be rooted", a)
		}
		if seen[a.Path] {
			t.Errorf("alias %q: duplicate path in registry", a.Path)
		}
		seen[a.Path] = true

		if a.Canonical == a.Path {
			t.Errorf("alias %q: Canonical must differ from the deprecated path", a.Path)
		}
		if !strings.HasPrefix(a.Canonical, "/") {
			t.Errorf("alias %q: Canonical %q must be a rooted path", a.Path, a.Canonical)
		}
		if a.Issue <= 0 {
			t.Errorf("alias %q: Issue must reference a tracking issue", a.Path)
		}

		dep, err := time.Parse(dateLayout, a.DeprecatedSince)
		if err != nil {
			t.Errorf("alias %q: DeprecatedSince %q not %s", a.Path, a.DeprecatedSince, dateLayout)
		}
		rem, err := time.Parse(dateLayout, a.RemoveAfter)
		if err != nil {
			t.Errorf("alias %q: RemoveAfter %q not %s", a.Path, a.RemoveAfter, dateLayout)
		}
		if err == nil && !rem.After(dep) {
			t.Errorf("alias %q: RemoveAfter %s must be after DeprecatedSince %s", a.Path, a.RemoveAfter, a.DeprecatedSince)
		}
	}
}

// TestNoAliasPastRemovalWindow is the forcing function: it fails once an
// alias's RemoveAfter date has passed, demanding either removal (cull the
// entry, which drops the route and the coverage allowlist automatically)
// or a deliberate, justified extension of the window.
//
// It compares against time.Now rather than a build-stamped date because
// `go test` runs without the ldflags that populate buildinfo.BuildTime,
// so the build stamp is always "unknown" under test. A day-granularity
// clock comparison is the pragmatic equivalent: it flips exactly once,
// permanently, when the window closes — that is the reminder, not
// flakiness.
func TestNoAliasPastRemovalWindow(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, a := range Aliases {
		remove := a.RemoveAfterTime()
		if today.After(remove) {
			t.Errorf("alias %q is past its RemoveAfter window (%s): confirm zero usage via telemetry and cull the entry (drops the route + coverage allowlist), or extend RemoveAfter with justification. Tracking: #%d",
				a.Path, a.RemoveAfter, a.Issue)
		}
	}
}

func TestDeprecationHeaders(t *testing.T) {
	a := Alias{
		Method: "GET", Path: "/v1/companion/ws", Canonical: "/v1/realtime/ws",
		DeprecatedSince: "2026-06-25", RemoveAfter: "2026-12-25", Issue: 1081,
	}
	h := a.DeprecationHeaders()

	// RFC 9745 Deprecation: Structured Field Date "@<unix>" at the
	// deprecation date's UTC midnight.
	wantDep := "@" + strconv.FormatInt(time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC).Unix(), 10)
	if got := h.Get("Deprecation"); got != wantDep {
		t.Errorf("Deprecation = %q, want %q", got, wantDep)
	}
	// RFC 8594 Sunset: HTTP-date at the removal date.
	wantSunset := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC).Format("Mon, 02 Jan 2006 15:04:05 GMT")
	if got := h.Get("Sunset"); got != wantSunset {
		t.Errorf("Sunset = %q, want %q", got, wantSunset)
	}
	if got := h.Get("Link"); got != `<`+"/v1/realtime/ws"+`>; rel="successor-version"` {
		t.Errorf("Link = %q, want successor-version pointing at the canonical path", got)
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("/v1/companion/ws"); !ok {
		t.Error("Lookup missed a registered alias")
	}
	if _, ok := Lookup("/v1/realtime/ws"); ok {
		t.Error("Lookup matched the canonical path, which is not an alias")
	}
}
