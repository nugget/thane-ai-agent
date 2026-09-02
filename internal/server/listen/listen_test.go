package listen

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRejectCrossOriginWrites(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name     string
		method   string
		host     string
		headers  map[string]string
		wantCode int
	}{
		{"GET cross-site passes: safe method", http.MethodGet, "thane:8080", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, http.StatusOK},
		{"HEAD passes", http.MethodHead, "thane:8080", map[string]string{"Origin": "https://evil.example"}, http.StatusOK},
		{"OPTIONS passes", http.MethodOptions, "thane:8080", map[string]string{"Origin": "https://evil.example"}, http.StatusOK},
		{"POST without browser headers passes: curl, HA, proxies", http.MethodPost, "thane:8080", nil, http.StatusOK},
		{"POST same-origin passes", http.MethodPost, "thane:8080", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://thane:8080"}, http.StatusOK},
		{"POST user-initiated navigation passes", http.MethodPost, "thane:8080", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
		{"POST matching Origin passes without Sec-Fetch-Site", http.MethodPost, "thane:8080", map[string]string{"Origin": "http://thane:8080"}, http.StatusOK},
		{"POST Origin host case-insensitive", http.MethodPost, "Thane:8080", map[string]string{"Origin": "http://thane:8080"}, http.StatusOK},
		{"POST proxied hostname without port matches", http.MethodPost, "aimee.example.net", map[string]string{"Origin": "https://aimee.example.net"}, http.StatusOK},
		{"POST cross-site refused", http.MethodPost, "thane:8080", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"POST same-site refused: sibling host is another boundary", http.MethodPost, "thane:8080", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"POST mismatched Origin refused", http.MethodPost, "thane:8080", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"POST Origin differing only by port refused", http.MethodPost, "thane:8080", map[string]string{"Origin": "http://thane:8081"}, http.StatusForbidden},
		{"POST opaque null Origin refused", http.MethodPost, "thane:8080", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"POST unparseable Origin refused", http.MethodPost, "thane:8080", map[string]string{"Origin": "not a url"}, http.StatusForbidden},
		{"PUT mismatched Origin refused", http.MethodPut, "thane:8080", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"DELETE cross-site refused", http.MethodDelete, "thane:8080", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"PATCH mismatched Origin refused", http.MethodPatch, "thane:8080", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/v1/loop-definitions", nil)
			req.Host = tc.host
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			RejectCrossOriginWrites(nil, next).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusForbidden {
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("Content-Type = %q, want application/json", ct)
				}
			}
		})
	}
}

func TestNewServerAppliesSharedBounds(t *testing.T) {
	t.Parallel()

	srv := NewServer(":0", http.NotFoundHandler(), 30*time.Second, 120*time.Second)
	if srv.ReadTimeout != 30*time.Second || srv.WriteTimeout != 120*time.Second {
		t.Fatalf("per-surface timeouts not preserved: read=%s write=%s", srv.ReadTimeout, srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Fatalf("shared bounds missing: header=%s idle=%s maxHeader=%d", srv.ReadHeaderTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
}

func TestLimitRequestBody(t *testing.T) {
	t.Parallel()

	var readErr error
	var readLen int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readLen, readErr = len(b), err
		w.WriteHeader(http.StatusOK)
	})

	t.Run("body within cap reads fully", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(strings.Repeat("a", 16)))
		LimitRequestBody(32, next).ServeHTTP(httptest.NewRecorder(), req)
		if readErr != nil || readLen != 16 {
			t.Fatalf("read = (%d, %v), want (16, nil)", readLen, readErr)
		}
	})

	t.Run("body past cap fails with MaxBytesError", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(strings.Repeat("a", 64)))
		LimitRequestBody(32, next).ServeHTTP(httptest.NewRecorder(), req)
		var maxErr *http.MaxBytesError
		if !errors.As(readErr, &maxErr) {
			t.Fatalf("read error = %v, want *http.MaxBytesError", readErr)
		}
	})
}
