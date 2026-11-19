package mcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// httpTBatch is a batch of n-1 notifications followed by the response
// to id.
func httpTBatch(n int, id string) string {
	parts := make([]string, 0, n)
	for range n - 1 {
		parts = append(parts, `{"jsonrpc":"2.0","method":"notifications/progress"}`)
	}
	parts = append(parts, `{"jsonrpc":"2.0","id":`+id+`,"result":{}}`)
	return "[" + strings.Join(parts, ",") + "]"
}

// A JSON body or SSE event holding a batch over maxInboundBatch fails
// that request as a protocol violation without dispatching any of it,
// with one WARN per transport; a batch at the limit is handled.
func TestHTTP_InboundBatchLimit(t *testing.T) {
	var size atomic.Int32
	var sse atomic.Bool
	srv := httpTStart(t, func(w http.ResponseWriter, _ *http.Request, m *wireMessage) {
		body := httpTBatch(int(size.Load()), string(m.ID))
		if sse.Load() {
			httpTSSE(w).data(body)
			return
		}
		httpTJSON(w, http.StatusOK, body)
	})
	logs := &coreTLogBuffer{}
	var notified atomic.Int32
	tr, err := newHTTPTransport(t.Context(), httpConfig{url: srv.URL, logger: logs.logger()}, transportHooks{
		onNotification: func(context.Context, *Notification) { notified.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close(context.Background())

	for _, viaSSE := range []bool{false, true} {
		sse.Store(viaSSE)
		size.Store(maxInboundBatch)
		before := notified.Load()
		httpTCall(t, tr, &Request{Method: methodToolsCall}, callInfo{})
		if n := notified.Load() - before; n != maxInboundBatch-1 {
			t.Fatalf("sse=%v: handled %d notifications of a batch at the limit, want %d", viaSSE, n, maxInboundBatch-1)
		}
		size.Store(maxInboundBatch + 1)
		before = notified.Load()
		if _, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, callInfo{}); !errors.Is(err, errProtocol) {
			t.Fatalf("sse=%v: err = %v, want errProtocol", viaSSE, err)
		}
		if n := notified.Load() - before; n != 0 {
			t.Errorf("sse=%v: dispatched %d messages of a batch over the limit", viaSSE, n)
		}
	}
	if out := logs.String(); strings.Count(out, "over the length limit") != 2 || strings.Count(out, "level=WARN") != 1 {
		t.Errorf("want one WARN, then Debug:\n%s", out)
	}
}
