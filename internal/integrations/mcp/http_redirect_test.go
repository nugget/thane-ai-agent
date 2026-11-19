package mcp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHTTP_RedirectPolicy(t *testing.T) {
	tests := []struct {
		name   string
		from   string
		to     string
		status int
		hops   int // len(via); 0 = 1
		ok     bool
	}{
		{name: "307 same origin", from: "http://h:8080/mcp", to: "http://h:8080/v2/mcp", status: 307, ok: true},
		{name: "308 same origin", from: "https://h/mcp", to: "https://h/other", status: 308, ok: true},
		{name: "host case-insensitive", from: "http://LOCALHOST:1/a", to: "http://localhost:1/b", status: 307, ok: true},
		{name: "default port spelled out", from: "http://h/a", to: "http://h:80/b", status: 307, ok: true},
		{name: "upgrade to https", from: "http://h:8080/mcp", to: "https://h:8443/mcp", status: 308, ok: true},
		{name: "third hop allowed", from: "http://h/a", to: "http://h/b", status: 307, hops: 3, ok: true},
		{name: "fourth hop refused", from: "http://h/a", to: "http://h/b", status: 307, hops: 4},
		{name: "301 refused", from: "http://h/a", to: "http://h/b", status: 301},
		{name: "302 refused", from: "http://h/a", to: "http://h/b", status: 302},
		{name: "303 refused", from: "http://h/a", to: "http://h/b", status: 303},
		{name: "other host", from: "http://h/a", to: "http://evil/a", status: 307},
		{name: "subdomain is another host", from: "http://h.example/a", to: "http://x.h.example/a", status: 307},
		{name: "other port", from: "http://h:1/a", to: "http://h:2/a", status: 307},
		{name: "downgrade", from: "https://h/a", to: "http://h/a", status: 308},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, _ := url.Parse(tt.from)
			to, _ := url.Parse(tt.to)
			hops := tt.hops
			if hops == 0 {
				hops = 1
			}
			via := []*http.Request{{URL: from}}
			for len(via) < hops {
				via = append(via, &http.Request{URL: from})
			}
			req := &http.Request{URL: to, Response: &http.Response{StatusCode: tt.status}}
			err := httpRedirectPolicy(req, via)
			if tt.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tt.ok && (!errors.Is(err, errProtocol) || !strings.Contains(err.Error(), "refused redirect")) {
				t.Fatalf("err = %v, want a refused redirect", err)
			}
		})
	}
}

func TestHTTP_RedirectFollowsSameOrigin(t *testing.T) {
	srv := httpTStart(t, func(w http.ResponseWriter, r *http.Request, m *wireMessage) {
		if r.URL.Path != "/final" {
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		httpTJSON(w, http.StatusOK, httpTResult(m.ID, `{"followed":true}`))
	})
	tr := httpTNew(t, srv.URL+"/mcp", map[string]string{"Authorization": "Bearer tok"}, transportHooks{})
	resp := httpTCall(t, tr, &Request{Method: methodToolsCall}, callInfo{protocolVersion: modernProtocolVersion, modern: true, name: "x"})
	if string(resp.Result) != `{"followed":true}` {
		t.Fatalf("result = %s", resp.Result)
	}
	reqs := srv.requests(methodToolsCall)
	if len(reqs) != 2 {
		t.Fatalf("server saw %d tools/call POSTs, want 2 (body re-sent on 307)", len(reqs))
	}
	final := reqs[1]
	if final.header.Get("Authorization") != "Bearer tok" || final.header.Get(httpHeaderName) != "x" {
		t.Errorf("same-origin hop lost headers: %v", final.header)
	}
}

func TestHTTP_RedirectRefused(t *testing.T) {
	other := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, _ *wireMessage) {
		t.Error("credentials followed a cross-origin redirect")
		w.WriteHeader(http.StatusOK)
	})
	// Same server, other hostname: localhost vs 127.0.0.1.
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	tests := []struct {
		name     string
		status   int
		location string
	}{
		{name: "cross origin 307", status: http.StatusTemporaryRedirect, location: otherURL + "/mcp?token=secret"},
		{name: "302 same origin", status: http.StatusFound, location: "/elsewhere"},
		{name: "redirect loop", status: http.StatusPermanentRedirect, location: "/again"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &coreTLogBuffer{}
			srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, _ *wireMessage) {
				w.Header().Set("Location", tt.location)
				w.WriteHeader(tt.status)
			})
			tr, err := newHTTPTransport(t.Context(), httpConfig{url: srv.URL + "/mcp", logger: logs.logger(),
				headers: map[string]string{"Authorization": "Bearer tok"}}, transportHooks{})
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close(t.Context())
			if _, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{}); !errors.Is(err, errProtocol) {
				t.Fatalf("Call err = %v, want errProtocol", err)
			}
			if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); !errors.Is(err, errProtocol) {
				t.Fatalf("Notify err = %v, want errProtocol", err)
			}
			out := logs.String()
			if !strings.Contains(out, "refused HTTP redirect") || !strings.Contains(out, "location=") {
				t.Errorf("no WARN naming the Location: %s", out)
			}
			if strings.Contains(out, "secret") || strings.Contains(out, "Bearer") {
				t.Errorf("log leaks credentials: %s", out)
			}
		})
	}
	if n := len(other.requests("")); n != 0 {
		t.Fatalf("cross-origin server received %d requests", n)
	}
}
