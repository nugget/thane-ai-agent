// Package openapi serves Thane's interactive API explorer and the OpenAPI
// specifications behind it, entirely from embedded assets. The Scalar UI
// bundle and the spec files are compiled into the binary, so the explorer at
// /docs works with no network access — consistent with Thane's
// internet-optional posture (an internal tool should not depend on a CDN).
package openapi

import (
	"embed"
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
// Content-Type. Missing files 404 rather than panicking, so a bad embed path
// fails loudly in tests instead of at request time in production.
func serveEmbedded(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := files.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}
