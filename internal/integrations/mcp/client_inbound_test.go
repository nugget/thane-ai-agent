package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClient_InboundRequests(t *testing.T) {
	type want struct {
		reply bool
		code  int    // 0: result {}
		msg   string // error message
	}
	// Thane's own declines carry distinctive messages, so a decline a
	// server relays back as a tools/call error is recognized.
	cases := []struct {
		method string
		legacy want // legacy and era unknown
	}{
		{method: methodPing, legacy: want{reply: true}},
		{method: methodRootsList, legacy: want{reply: true, code: codeMethodNotFound, msg: declineRoots}},
		{method: methodSampling, legacy: want{reply: true, code: codeMethodNotFound, msg: declineSampling}},
		{method: methodElicitation, legacy: want{reply: true, code: codeInvalidParams, msg: declineElicitation}},
		{method: "vendor/custom", legacy: want{reply: true, code: codeMethodNotFound, msg: "Method not found"}},
	}
	for _, e := range []era{eraUnknown, eraLegacy, eraModern} {
		for _, tt := range cases {
			t.Run(e.String()+"/"+tt.method, func(t *testing.T) {
				c := clientTNew(t, &clientTFakeServer{}, clientTConfig(transportStdio, ""))
				conn := &clientConn{}
				conn.eraState.Store(int32(e))
				resp := c.hooksFor(conn).onRequest(t.Context(), &Request{ID: json.RawMessage(`"s-1"`), Method: tt.method})
				w := tt.legacy
				if e == eraModern {
					w = want{} // modern: never answered
				}
				switch {
				case !w.reply:
					if resp != nil {
						t.Fatalf("modern server request answered with %+v; clients must not reply", resp)
					}
				case resp == nil:
					t.Fatal("no reply; legacy server requests must be answered")
				case w.code == 0:
					if resp.Error != nil || string(resp.Result) != `{}` {
						t.Errorf("reply = %+v, want result {}", resp)
					}
				default:
					if resp.Error == nil || resp.Error.Code != w.code || resp.Error.Message != w.msg {
						t.Errorf("reply = %+v, want error %d %q", resp.Error, w.code, w.msg)
					}
				}
			})
		}
	}
}

// TestClient_InboundReplyThroughDispatch checks the hook against the
// transports' shared dispatcher: the reply echoes the server's id verbatim, including a
// string id and an id that collides with one of Thane's own.
func TestClient_InboundReplyThroughDispatch(t *testing.T) {
	c := clientTNew(t, &clientTFakeServer{}, clientTConfig(transportStdio, ""))
	conn := &clientConn{}
	conn.eraState.Store(int32(eraLegacy))
	hooks := c.hooksFor(conn)
	for _, id := range []string{`"abc"`, `1`, `-7`} {
		msgs, err := decodeMessages([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"ping"}`))
		if err != nil {
			t.Fatal(err)
		}
		got := make(chan *Response, 1)
		dispatchInbound(t.Context(), hooks, &msgs[0], func(_ context.Context, r *Response) error {
			got <- r
			return nil
		}, coreTLogger(t))
		select {
		case r := <-got:
			if string(r.ID) != id || r.JSONRPC != jsonrpcVersion || string(r.Result) != `{}` {
				t.Errorf("reply = %s %s %s, want id %s result {}", r.JSONRPC, r.ID, r.Result, id)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no reply")
		}
	}
}

func TestClient_ModernRequestViolationsWarnOncePerMethod(t *testing.T) {
	buf := &coreTLogBuffer{}
	cfg := clientTConfig(transportStdio, "")
	cfg.logger = buf.logger()
	c := clientTNew(t, &clientTFakeServer{}, cfg)
	conn := &clientConn{}
	conn.eraState.Store(int32(eraModern))
	hooks := c.hooksFor(conn)
	for _, m := range []string{methodPing, methodPing, methodRootsList, methodPing} {
		if r := hooks.onRequest(t.Context(), &Request{ID: json.RawMessage(`1`), Method: m}); r != nil {
			t.Fatalf("replied to %s", m)
		}
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Errorf("WARN lines = %d, want 2 (once per method)\n%s", n, buf.String())
	}
}

// TestClient_PingDuringTheProbeIsAnswered checks that while the era is
// unknown, a server ping (evidence of legacy) gets {}.
func TestClient_PingDuringTheProbeIsAnswered(t *testing.T) {
	routes := clientTLegacyRoutes("")
	var reply *Response
	routes[methodDiscover] = func(_ context.Context, tr *clientTFakeTransport, _ *Request, _ callInfo) (*Response, error) {
		reply = tr.serverRequest(methodPing, `"keepalive"`)
		return clientTRPCErr(codeMethodNotFound, ""), nil
	}
	srv := &clientTFakeServer{handle: routes.handler()}
	c := clientTNew(t, srv, clientTConfig(transportStdio, ""))
	conn, err := c.negotiate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c.closeConn(conn)
	if reply == nil || reply.Error != nil || string(reply.Result) != `{}` {
		t.Errorf("ping during the probe answered %+v, want {}", reply)
	}
}

// clientTLevelHandler records log records.
type clientTLevelHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *clientTLevelHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *clientTLevelHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *clientTLevelHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *clientTLevelHandler) WithGroup(string) slog.Handler      { return h }

func (h *clientTLevelHandler) find(msg string) (slog.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

func TestClient_InboundNotifications(t *testing.T) {
	t.Run("list_changed requests a refresh without blocking", func(t *testing.T) {
		c := clientTNew(t, &clientTFakeServer{}, clientTConfig(transportStdio, ""))
		hooks := c.hooksFor(&clientConn{})
		for range 3 { // coalesces; never blocks the reader goroutine
			hooks.onNotification(t.Context(), &Notification{Method: methodToolsListChanged})
		}
		select {
		case <-c.refreshCh:
		default:
			t.Fatal("no refresh requested")
		}
	})
	for _, tt := range []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug}, {"info", slog.LevelDebug}, {"notice", slog.LevelDebug},
		{"warning", slog.LevelInfo},
		{"error", slog.LevelWarn}, {"critical", slog.LevelWarn}, {"alert", slog.LevelWarn}, {"emergency", slog.LevelWarn},
		{"bogus", slog.LevelDebug},
	} {
		t.Run("message "+tt.level, func(t *testing.T) {
			h := &clientTLevelHandler{}
			cfg := clientTConfig(transportStdio, "")
			cfg.logger = slog.New(h)
			c := clientTNew(t, &clientTFakeServer{}, cfg)
			params := `{"level":"` + tt.level + `","logger":"db","data":{"msg":"` + strings.Repeat("x", 3000) + `"}}`
			c.hooksFor(&clientConn{}).onNotification(t.Context(), &Notification{Method: methodLogMessage, Params: json.RawMessage(params)})
			r, ok := h.find("mcp: server log message")
			if !ok {
				t.Fatal("server log message not logged")
			}
			if r.Level != tt.want {
				t.Errorf("level = %v, want %v", r.Level, tt.want)
			}
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "data" && len(a.Value.String()) > serverLogCapBytes {
					t.Errorf("data not capped: %d bytes", len(a.Value.String()))
				}
				return true
			})
		})
	}
	t.Run("progress, cancelled and unknown are quiet", func(t *testing.T) {
		h := &clientTLevelHandler{}
		cfg := clientTConfig(transportStdio, "")
		cfg.logger = slog.New(h)
		c := clientTNew(t, &clientTFakeServer{}, cfg)
		hooks := c.hooksFor(&clientConn{})
		for _, m := range []string{methodProgress, methodCancelled, "vendor/thing", methodLogMessage} {
			hooks.onNotification(t.Context(), &Notification{Method: m, Params: json.RawMessage(`{"progress":1}`)})
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, r := range h.records {
			if r.Level > slog.LevelDebug {
				t.Errorf("%q logged at %v, want Debug", r.Message, r.Level)
			}
		}
		select {
		case <-c.refreshCh:
			t.Error("a non-list_changed notification requested a refresh")
		default:
		}
	})
}

func TestClient_ServerLogText(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{``, ``},
		{`"plain text"`, `plain text`},
		{`{ "a" : 1 }`, `{"a":1}`},
		{`not json`, `not json`},
	} {
		if got := serverLogText(json.RawMessage(tt.in)); got != tt.want {
			t.Errorf("serverLogText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClient_WarnOnceIsBounded(t *testing.T) {
	h := &clientTLevelHandler{}
	cfg := clientTConfig(transportStdio, "")
	cfg.logger = slog.New(h)
	c := clientTNew(t, &clientTFakeServer{}, cfg)
	for i := range maxWarnKeys + 10 {
		c.warnOnce(strings.Repeat("k", i+1), "warn")
	}
	c.warnOnce("k", "warn")
	h.mu.Lock()
	defer h.mu.Unlock()
	warns := 0
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			warns++
		}
	}
	if warns != maxWarnKeys || len(c.warnings.seen) != maxWarnKeys {
		t.Errorf("WARN lines = %d, keys = %d; want %d", warns, len(c.warnings.seen), maxWarnKeys)
	}
}
