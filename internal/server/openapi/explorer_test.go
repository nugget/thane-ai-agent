package openapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterRoutes verifies the explorer harness serves each asset with the
// right status and content type, and that the embedded specs are well-formed
// OpenAPI 3.1 documents. It does not assert how Scalar renders them in a
// browser — only that the bytes are served correctly.
func TestRegisterRoutes(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cases := []struct {
		path       string
		wantCTPart string
		wantBody   string
	}{
		{"/docs", "text/html", "createApiReference"},
		{"/docs/scalar.js", "javascript", "createApiReference"},
		{"/docs/openapi/native.yaml", "yaml", "openapi: 3.1.0"},
		{"/docs/openapi/compat.yaml", "yaml", "openapi: 3.1.0"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, tc.wantCTPart) {
			t.Errorf("GET %s content-type = %q, want substring %q", tc.path, ct, tc.wantCTPart)
		}
		if !strings.Contains(string(body), tc.wantBody) {
			t.Errorf("GET %s body missing %q", tc.path, tc.wantBody)
		}
	}
}

// TestSpecsEmbedded guards against an empty or misnamed embed: both specs must
// be present and non-trivial.
func TestSpecsEmbedded(t *testing.T) {
	for _, name := range []string{"native.yaml", "compat.yaml"} {
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("embed missing %s: %v", name, err)
		}
		if len(data) < 200 {
			t.Errorf("%s embedded but only %d bytes", name, len(data))
		}
	}
}
