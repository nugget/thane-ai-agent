package openapi

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// serverSource is the file that builds the native API mux. The route-coverage
// test reads its registration literals; if NewServer's routes move to another
// file, update this path — the test will tell you by extracting too few routes.
const serverSource = "../api/server.go"

// routeAllowlist are routes registered on the native mux that are intentionally
// absent from native.yaml:
//   - "GET /" is the JSON root / dashboard fallback, not an API resource.
//   - /v1/companion/ws and /v1/platform/ws are legacy WebSocket aliases kept
//     for existing thane-agent-macos installs; the canonical, documented path
//     is /v1/realtime/ws.
var routeAllowlist = map[string]bool{
	"GET /":                true,
	"GET /v1/companion/ws": true,
	"GET /v1/platform/ws":  true,
}

// muxRouteRe matches the Go 1.22 method-pattern literals that register handlers
// on the native mux, e.g. `mux.HandleFunc("GET /v1/loops", ...)` and
// `mux.Handle("GET /v1/realtime/ws", ...)`.
var muxRouteRe = regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+) (/[^"]*)"`)

// TestNativeSpecRouteCoverage guards drift between the native API routes
// registered in server.go and the paths documented in native.yaml. The spec is
// hand-authored — a mirror of the code that can silently fall out of step — so
// this fails CI when a registered route is undocumented or a documented path is
// not actually served. Intentional exceptions live in routeAllowlist.
func TestNativeSpecRouteCoverage(t *testing.T) {
	registered := registeredRoutes(t)
	documented := documentedRoutes(t)

	if len(registered) < 30 {
		t.Fatalf("only %d routes extracted from %s — the registration format likely changed; update muxRouteRe", len(registered), serverSource)
	}
	if len(documented) < 30 {
		t.Fatalf("only %d paths parsed from native.yaml — parsing likely broke", len(documented))
	}

	// Direction 1: every registered native route is documented (or allowlisted).
	for _, r := range sortedKeys(registered) {
		if routeAllowlist[r] || documented[r] {
			continue
		}
		t.Errorf("route %q is registered in server.go but undocumented in native.yaml", r)
	}

	// Direction 2: every documented path is actually registered.
	for _, d := range sortedKeys(documented) {
		if registered[d] {
			continue
		}
		t.Errorf("path %q is documented in native.yaml but not registered in server.go", d)
	}
}

// registeredRoutes returns the "METHOD /path" set the native mux registers,
// scraped from server.go's registration literals.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(serverSource)
	if err != nil {
		t.Fatalf("read %s: %v", serverSource, err)
	}
	out := make(map[string]bool)
	for _, m := range muxRouteRe.FindAllStringSubmatch(string(src), -1) {
		out[m[1]+" "+m[2]] = true
	}
	return out
}

// documentedRoutes returns the "METHOD /path" set declared under `paths` in the
// embedded native.yaml. Non-operation keys (parameters, $ref, summary) are
// skipped by the HTTP-method filter.
func documentedRoutes(t *testing.T) map[string]bool {
	t.Helper()
	data, err := files.ReadFile("native.yaml")
	if err != nil {
		t.Fatalf("read embedded native.yaml: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse native.yaml: %v", err)
	}
	httpMethods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"patch": true, "head": true, "options": true, "trace": true,
	}
	out := make(map[string]bool)
	for path, ops := range doc.Paths {
		for method := range ops {
			if httpMethods[strings.ToLower(method)] {
				out[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
