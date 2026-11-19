package mcp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// A stdout line holding a batch over maxInboundBatch is skipped as a
// protocol violation, with one WARN per transport; the connection and
// later lines are unaffected. A batch at the limit is handled.
func TestStdio_InboundBatchLimit(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-script")
	var notified atomic.Int32
	tr := stdioTStart(t, cfg, transportHooks{
		onNotification: func(_ context.Context, _ *Notification) { notified.Add(1) },
	})

	stdioTMust(t, tr, "bigbatch", map[string]int{"count": maxInboundBatch}, nil)
	if n := notified.Load(); n != maxInboundBatch {
		t.Fatalf("handled %d notifications of a batch at the limit, want %d", n, maxInboundBatch)
	}
	for range 2 {
		stdioTMust(t, tr, "bigbatch", map[string]int{"count": maxInboundBatch + 1}, nil)
	}
	if n := notified.Load(); n != maxInboundBatch {
		t.Errorf("handled %d notifications from batches over the limit", n-maxInboundBatch)
	}
	out := logs.String()
	if strings.Count(out, "over the length limit") != 2 || !strings.Contains(out, "level=WARN msg=\"mcp: skipping a JSON-RPC batch") ||
		strings.Count(out, "level=WARN") != 1 {
		t.Errorf("want one WARN and one Debug for the two long batches:\n%s", out)
	}
	if stdioTIsDone(tr.Done()) {
		t.Fatal("a long batch closed the connection")
	}
	stdioTMust(t, tr, "echo", nil, nil)
}
