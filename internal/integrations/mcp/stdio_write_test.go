package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// stdioTPending reports how many calls are waiting for a response.
func stdioTPending(tr *stdioTransport) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.pending)
}

// stdioTBigParams is a params object well over the 64 KiB pipe buffer.
func stdioTBigParams() json.RawMessage {
	return json.RawMessage(`{"pad":"` + strings.Repeat("x", 256<<10) + `"}`)
}

// A call whose ctx ends while its line is half written (the server is
// busy and not reading stdin) returns the ctx error at once, but the
// line is finished in the background: the server stays up, the other
// call in flight is answered, and the server reads the whole request
// followed by notifications/cancelled for it.
func TestStdio_CtxEndingMidWriteKeepsServer(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	cfg, logs := stdioTLogs(t, "stdio-gate")
	cfg.env = append(cfg.env, stdioTGateEnv+"="+gate)
	tr := stdioTStart(t, cfg, transportHooks{})

	small := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		resp, err := tr.Call(ctx, &Request{Method: "echo", Params: json.RawMessage(`{"small":true}`)}, callInfo{cancellable: true})
		if err == nil && (resp.Error != nil || string(resp.Result) != `{"small":true}`) {
			err = fmt.Errorf("echo = %+v", resp)
		}
		small <- err
	}()
	stdioTEventually(t, "the first call in flight", func() bool { return stdioTPending(tr) == 1 })

	// Nothing drains stdin until the gate opens, so this line blocks
	// once the pipe is full and its ctx ends part-way through.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := tr.Call(ctx, &Request{Method: "hold", Params: stdioTBigParams()}, callInfo{cancellable: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cut-short call: err = %v, want context.DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("cut-short call returned after %v, want right after its ctx ended", took)
	}
	big := fmt.Sprint(tr.nextID.Load())
	if stdioTIsDone(tr.Done()) {
		t.Fatal("the transport finished because a ctx ended mid-write")
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-small:
		if err != nil {
			t.Fatalf("call in flight: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("call in flight never answered")
	}
	// The helper records what it decodes on its stdin reader, in order:
	// the long line arrived whole, and the cancellation right after it.
	st := stdioTStatsOf(t, tr)
	i := slices.Index(st.Received, "request "+big)
	if i < 0 || i+1 >= len(st.Received) || st.Received[i+1] != "cancelled "+big {
		t.Errorf("server read %v, want request %s followed by its cancellation", st.Received, big)
	}
	if !slices.ContainsFunc(st.Cancelled, func(c stdioTCancel) bool { return string(c.RequestID) == big && c.Reason == "timeout" }) {
		t.Errorf("cancellations = %+v, want id %s with reason timeout", st.Cancelled, big)
	}
	stdioTMust(t, tr, "echo", nil, nil)
	if !stdioTAlive(tr.pid) || stdioTIsDone(tr.Done()) {
		t.Fatal("server gone after a cut-short write")
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a finished line logged a WARN:\n%s", logs.String())
	}
}

// A line whose ctx ended part-way that the server never drains within
// frameFinishWait breaks the stream's framing: the caller still gets
// its ctx error at once, and the transport is then torn down with a
// WARN.
func TestStdio_CutShortWriteNeverFinishedTearsDown(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-noread")
	cfg.closeStdinWait = 100 * time.Millisecond
	cfg.frameFinishWait = 200 * time.Millisecond
	tr := stdioTStart(t, cfg, transportHooks{})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := tr.Call(ctx, &Request{Method: "hold", Params: stdioTBigParams()}, callInfo{cancellable: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	stdioTWaitDone(t, tr.Done(), 10*time.Second, "Done")
	if cause := tr.cause().Error(); !strings.Contains(cause, "partial write") {
		t.Errorf("cause = %q, want a partial write", cause)
	}
	stdioTEventually(t, "WARN", func() bool {
		return strings.Contains(logs.String(), `level=WARN msg="mcp: stdio write was cut short; closing the connection"`)
	})
}

// Close while a cut-short line is still being finished: closing stdin
// makes the pending write fail with os.ErrClosed, which is the stop
// sequence at work, not a broken stream. The transport finishes with
// Close's cause, Close returns once the process is gone, and writeSem
// is free again.
func TestStdio_CloseDuringFinishFrameKeepsCloseCause(t *testing.T) {
	cfg := stdioTHelper(t, "stdio-noread")
	cfg.closeStdinWait = 100 * time.Millisecond
	cfg.termWait = 100 * time.Millisecond
	cfg.frameFinishWait = time.Minute
	tr := stdioTStart(t, cfg, transportHooks{})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err := tr.Call(ctx, &Request{Method: "hold", Params: stdioTBigParams()}, callInfo{cancellable: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if stdioTIsDone(tr.Done()) {
		t.Fatal("the transport finished before Close")
	}

	cctx, ccancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer ccancel()
	start := time.Now()
	if err := tr.Close(cctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Close took %v, want it back once the process is gone", took)
	}
	if cause := tr.cause(); !errors.Is(cause, errStdioClosed) {
		t.Errorf("cause = %q, want Close's %q", cause, errStdioClosed)
	}
	stdioTEventually(t, "writeSem free", func() bool { return stdioTWriteSemFree(tr) })
}

// stdioTWriteSemFree reports whether no write holds writeSem.
func stdioTWriteSemFree(tr *stdioTransport) bool {
	select {
	case tr.writeSem <- struct{}{}:
		<-tr.writeSem
		return true
	default:
		return false
	}
}

// The write finishing a cut-short line that fails because stdin was
// closed is a casualty of the stop sequence, which finishes the
// transport with its own cause; finishFrame must not finish it with a
// partial write. Close would mask that (once Close is asked for, any
// finish records Close's cause), so this closes stdin alone, the stop
// sequence's first step, on a server that keeps running: once the write
// has given writeSem back, the transport is still up.
func TestStdio_StdinClosedDuringFinishFrameIsNotALoss(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-noread")
	cfg.frameFinishWait = time.Minute
	tr := stdioTStart(t, cfg, transportHooks{})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err := tr.Call(ctx, &Request{Method: "hold", Params: stdioTBigParams()}, callInfo{cancellable: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if stdioTWriteSemFree(tr) {
		t.Fatal("no write is finishing the cut-short line")
	}
	tr.closeStdin()
	stdioTEventually(t, "writeSem free", func() bool { return stdioTWriteSemFree(tr) })
	if !strings.Contains(logs.String(), `msg="mcp: stdin closed while finishing a stdio write"`) {
		t.Errorf("the casualty write was not logged:\n%s", logs.String())
	}
	if stdioTIsDone(tr.Done()) {
		t.Fatalf("the casualty write finished the transport (cause %v)", tr.cause())
	}
}
