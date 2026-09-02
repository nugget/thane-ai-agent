package listen

import "net/http"

// Posture names what a surface's responses are, which is what decides
// the content policy they carry. It is an enumeration rather than a set
// of branches because the set grows: #1509's artifact primitive, when it
// lands, serves arbitrary client-supplied bytes and will want a posture
// stricter than any of these.
type Posture int

const (
	// PostureAPI is for surfaces that answer with data: the native /v1
	// routes, the two compat shims, CardDAV. Their responses are not
	// documents and must never be treated as any, which matters more
	// than it looks: a companion materialization (#1509) is opaque
	// client-authored JSON that Thane stores without parsing and serves
	// back from the same origin the console's session cookie lives on.
	// Sniffing is the mechanism that would turn such a payload into
	// script on that origin, so nosniff and default-src 'none' together
	// are what keep stored data from becoming stored scripting.
	PostureAPI Posture = iota

	// PostureConsole is for the web console, which is a real document
	// and can afford a real policy: the console loads no external
	// origin, evaluates no strings, embeds no images, and reaches the
	// server over one same-origin EventSource, so every directive below
	// names 'self' or 'none' and none is loosened.
	PostureConsole

	// PostureDocuments is for the OpenAPI explorer at /docs, whose
	// vendored Scalar bundle is the one asset Thane serves that it did
	// not write. The bundle carries inline images as data: URIs and
	// builds stylesheets at runtime, so this posture — and only this
	// posture — loosens img-src and style-src to suit it. Keeping that
	// looseness on its own surface is the reason postures exist at all.
	PostureDocuments
)

// contentPolicy is the Content-Security-Policy each posture sends.
var contentPolicy = map[Posture]string{
	PostureAPI: "default-src 'none'; frame-ancestors 'none'; base-uri 'none'",

	PostureConsole: "default-src 'none'; " +
		"script-src 'self'; " +
		"style-src 'self'; " +
		"img-src 'self'; " +
		"connect-src 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'",

	// data: for the bundle's inline icons; 'unsafe-inline' for the
	// styles it applies at runtime. Both are the vendored bundle's
	// requirements, not Thane's, and both were verified by removing them
	// and watching the page break: without 'unsafe-inline' the explorer
	// renders as unstyled HTML. They stop at this surface.
	//
	// Scripts are NOT loosened here, which matters most on the one
	// surface serving code Thane did not write. The bundle tries to run
	// one inline script and is refused; the page renders and works
	// without it, and an inline script that cannot be identified is a
	// reason to keep refusing rather than to allowlist a hash.
	//
	// font-src and connect-src stay closed on purpose. The bundle
	// reaches for fonts.scalar.com and api.scalar.com at runtime despite
	// being vendored to render offline; those requests are refused and
	// the page falls back to system fonts. See #1512.
	PostureDocuments: "default-src 'none'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'",
}

// SecurityHeaders sets the response headers that tell a browser what it
// may do with what Thane just sent it. Every surface takes one, so a
// listener cannot be added without a decision about what its responses
// are, the same way it cannot be added without the shared bounds.
//
// Headers are set before the handler runs, so a handler that knows
// better can overwrite them. That is deliberate and load-bearing: the
// native API, the console, and the explorer share one mux, so the API
// posture is applied to the whole chain and the console and explorer
// sub-trees replace the policy for their own routes. A route added to
// that mux without a thought therefore gets the strictest posture rather
// than none, which is the right way round.
//
// Strict-Transport-Security is deliberately absent. It is a claim about
// transport and belongs to the surface that terminates TLS; the front
// door sets it, and a plaintext listener asserting it would be making a
// promise it cannot keep.
func SecurityHeaders(posture Posture, next http.Handler) http.Handler {
	policy := contentPolicy[posture]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", policy)
		// A JSON body is never sniffed into script, and a document is
		// never reinterpreted as something else.
		h.Set("X-Content-Type-Options", "nosniff")
		// frame-ancestors already says this to anything current;
		// X-Frame-Options is its legacy spelling and costs one line.
		h.Set("X-Frame-Options", "DENY")
		// The console links out to the project's GitHub page. Under the
		// default policy that request would carry the deployment's
		// hostname to a third party, and no Thane surface has a reason
		// to send a referrer anywhere.
		h.Set("Referrer-Policy", "no-referrer")
		// Nothing Thane serves asks for a device.
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		next.ServeHTTP(w, r)
	})
}
