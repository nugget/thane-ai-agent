// Package openapi serves Thane's interactive API explorer and the OpenAPI
// specifications behind it, entirely from embedded assets. The Scalar UI
// bundle and the spec files are compiled into the binary, so the explorer at
// /docs works with no network access — consistent with Thane's
// internet-optional posture (an internal tool should not depend on a CDN).
package openapi

import (
	"embed"
	"fmt"
	"net/http"
)

// files holds the served assets: the two OpenAPI documents and the vendored
// Scalar standalone bundle. Vendored (not CDN-loaded) so /docs renders
// offline.
//
//go:embed native.yaml compat.yaml assets/scalar.standalone.js
var files embed.FS

// indexHTML bootstraps the Scalar API reference against the embedded specs.
// Scalar is loaded from /docs/scalar.js (vendored) and renders both the
// native and compatibility documents with a built-in switcher.
const indexHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Thane API Explorer</title>
  </head>
  <body>
    <div id="app"></div>
    <script src="/docs/scalar.js"></script>
    <script>
      Scalar.createApiReference('#app', {
        sources: [
          { url: '/docs/openapi/native.yaml', title: 'Thane Native API', slug: 'native' },
          { url: '/docs/openapi/compat.yaml', title: 'Compatibility API', slug: 'compat' },
        ],
        // Default the "Try it" code samples to curl — the operator's lingua
        // franca for a self-hosted API.
        defaultHttpClient: { targetKey: 'shell', clientKey: 'curl' },
        // Label the auto-rendered components/schemas section "Schemas" so it no
        // longer reads as a bare "Models" — that title collided with the
        // "Model Routing" tag and the "Routing & Telemetry" group.
        modelsSectionLabel: 'Schemas',
      })
    </script>
  </body>
</html>
`

// RegisterRoutes mounts the explorer and the raw specs under /docs on mux.
// The surface is read-only and unauthenticated (documentation only); restrict
// it at the reverse proxy if it should not be public.
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /docs", handleIndex)
	mux.HandleFunc("GET /docs/", handleIndex)
	mux.HandleFunc("GET /docs/scalar.js", serveEmbedded("assets/scalar.standalone.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /docs/openapi/native.yaml", serveEmbedded("native.yaml", "application/yaml; charset=utf-8"))
	mux.HandleFunc("GET /docs/openapi/compat.yaml", serveEmbedded("compat.yaml", "application/yaml; charset=utf-8"))
}

func handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

// serveEmbedded returns a handler that writes one embedded file with a fixed
// Content-Type. The bytes are read once when the handler is built (at route
// registration) and reused for every request, so serving the multi-megabyte
// Scalar bundle never re-reads the embed FS or re-allocates per request. A
// missing name is a build-time error — //go:embed guarantees the files exist
// (TestSpecsEmbedded asserts it) — so it panics at registration rather than
// 404ing on every request forever.
func serveEmbedded(name, contentType string) http.HandlerFunc {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("openapi: embedded asset %q: %v", name, err))
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}
