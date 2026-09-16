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

	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

// files holds the served assets: the two OpenAPI documents and the vendored
// Scalar standalone bundle. Vendored (not CDN-loaded) so /docs renders
// offline.
//
//go:embed native.yaml compat.yaml assets/scalar.standalone.js assets/explorer-boot.js
var files embed.FS

// indexHTML bootstraps the Scalar API reference against the embedded specs.
// Scalar is loaded from /docs/scalar.js (vendored) and configured by
// /docs/boot.js; both are files rather than inline blocks so the
// explorer's content policy can keep script-src 'self'. The reference
// renders both the native and compatibility documents with a built-in
// switcher.
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
    <script src="/docs/boot.js"></script>
  </body>
</html>
`

// RegisterRoutes mounts the explorer and the raw specs under /docs on mux.
// The surface is read-only and unauthenticated (documentation only); restrict
// it at the reverse proxy if it should not be public.
func RegisterRoutes(mux listen.RouteRegistrar) {
	// The explorer replaces the API posture with the one that suits the
	// vendored Scalar bundle, whose data: images and runtime-built
	// stylesheets are the only reason any directive anywhere is loosened.
	docs := func(h http.HandlerFunc) http.Handler {
		return listen.SecurityHeaders(listen.PostureDocuments, h)
	}
	mux.Handle("GET /docs", docs(handleIndex))
	mux.Handle("GET /docs/", docs(handleIndex))
	mux.Handle("GET /docs/scalar.js", docs(serveEmbedded("assets/scalar.standalone.js", "application/javascript; charset=utf-8")))
	mux.Handle("GET /docs/boot.js", docs(serveEmbedded("assets/explorer-boot.js", "application/javascript; charset=utf-8")))
	mux.Handle("GET /docs/openapi/native.yaml", docs(serveEmbedded("native.yaml", "application/yaml; charset=utf-8")))
	mux.Handle("GET /docs/openapi/compat.yaml", docs(serveEmbedded("compat.yaml", "application/yaml; charset=utf-8")))
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
