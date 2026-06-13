package openapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRegisterRoutes verifies the explorer harness serves each asset with the
// right status and content type. It only checks the served bytes (status,
// Content-Type, a marker substring); spec well-formedness is validated in
// TestSpecsEmbedded, and Scalar's browser rendering is out of scope.
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

// TestSpecsEmbedded validates that both specs are present, parse as YAML, and
// declare OpenAPI 3.1 with a paths object — so a malformed or truncated spec
// fails here in CI rather than only breaking the explorer in a browser.
func TestSpecsEmbedded(t *testing.T) {
	for _, name := range []string{"native.yaml", "compat.yaml"} {
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("embed missing %s: %v", name, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s is not valid YAML: %v", name, err)
		}
		if doc["openapi"] != "3.1.0" {
			t.Errorf("%s openapi = %v, want \"3.1.0\"", name, doc["openapi"])
		}
		if _, ok := doc["paths"].(map[string]any); !ok {
			t.Errorf("%s has no paths object", name)
		}
	}
}
