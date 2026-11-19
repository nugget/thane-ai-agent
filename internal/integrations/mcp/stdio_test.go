package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stdioTStart spawns cfg and closes it when the test ends.
func stdioTStart(t *testing.T, cfg stdioConfig, hooks transportHooks) *stdioTransport {
	t.Helper()
	tr, err := newStdioTransport(t.Context(), cfg, hooks)
	if err != nil {
		t.Fatalf("newStdioTransport: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = tr.Close(ctx)
	})
	return tr
}

// stdioTCall calls method with params (marshalled; nil = none) under a
// 10s bound.
func stdioTCall(t *testing.T, tr *stdioTransport, method string, params any, info callInfo) (*Response, error) {
	t.Helper()
	raw, err := marshalParams(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return tr.Call(ctx, &Request{Method: method, Params: raw}, info)
}

// stdioTMust calls method and decodes a successful result into out.
func stdioTMust(t *testing.T, tr *stdioTransport, method string, params, out any) *Response {
	t.Helper()
	resp, err := stdioTCall(t, tr, method, params, callInfo{cancellable: true})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if resp.Error != nil {
		t.Fatalf("%s: rpc error %v", method, resp.Error)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			t.Fatalf("%s: decode result %s: %v", method, resp.Result, err)
		}
	}
	return resp
}

// stdioTStats fetches the fake server's counters.
func stdioTStatsOf(t *testing.T, tr *stdioTransport) stdioTStats {
	t.Helper()
	var st stdioTStats
	stdioTMust(t, tr, "stats", nil, &st)
	return st
}

// stdioTEventually polls cond for up to 5s.
func stdioTEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	coreTWaitFor(t, 5*time.Second, what, cond)
}

// stdioTWaitDone waits up to d for ch to close.
func stdioTWaitDone(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("%s not closed within %v", what, d)
	}
}

// stdioTIsDone reports whether ch is closed.
func stdioTIsDone(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// stdioTLogs returns a helper config whose logs are captured.
func stdioTLogs(t *testing.T, mode string) (stdioConfig, *coreTLogBuffer) {
	cfg := stdioTHelper(t, mode)
	buf := &coreTLogBuffer{}
	cfg.logger = buf.logger()
	return cfg, buf
}

func TestStdio_CallRoundTrip(t *testing.T) {
	tr := stdioTStart(t, stdioTHelper(t, "stdio-script"), transportHooks{})

	req := &Request{Method: "echo", Params: json.RawMessage(`{"a": 1}`)}
	for want := int64(1); want <= 3; want++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		resp, err := tr.Call(ctx, req, callInfo{})
		cancel()
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if got, ok := idInt(resp.ID); !ok || got != want {
			t.Errorf("response id = %s, want %d", resp.ID, want)
		}
		if string(resp.Result) != `{"a":1}` {
			t.Errorf("result = %s, want the compacted params", resp.Result)
		}
	}
	if req.ID != nil || req.JSONRPC != "" {
		t.Errorf("Call mutated the caller's request: id=%s jsonrpc=%q", req.ID, req.JSONRPC)
	}

	if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	stdioTEventually(t, "initialized notification", func() bool { return stdioTStatsOf(t, tr).Initialized })

	resp, err := stdioTCall(t, tr, "no/such", nil, callInfo{})
	if err != nil || resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method: resp=%+v err=%v, want (resp with -32601, nil)", resp, err)
	}

	if err := tr.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tr.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	stdioTWaitDone(t, tr.Done(), time.Second, "Done")
	stdioTWaitDone(t, tr.exited, time.Second, "exited")
	if tr.exitErr != nil {
		t.Errorf("server exit = %v, want a clean exit on stdin EOF", tr.exitErr)
	}
}

func TestStdio_EnvAndDir(t *testing.T) {
	dir := t.TempDir()
	cfg := stdioTHelper(t, "stdio-script")
	cfg.env = append(cfg.env, "THANE_STDIO_TEST=from-config")
	cfg.dir = dir
	tr := stdioTStart(t, cfg, transportHooks{})

	var got struct{ Value, Cwd string }
	stdioTMust(t, tr, "env", map[string]string{"name": "THANE_STDIO_TEST"}, &got)
	if got.Value != "from-config" {
		t.Errorf("env value = %q, want from-config", got.Value)
	}
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(got.Cwd)
	if gotDir != wantDir {
		t.Errorf("cwd = %q, want %q", gotDir, wantDir)
	}
}

func TestStdio_SpawnFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		command string
		also    error
	}{
		{"missing executable", t.Context(), filepath.Join(t.TempDir(), "no-such-mcp-server"), nil},
		{"empty command", t.Context(), "", nil},
		{"ctx already done", cancelled, "/bin/true", context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := newStdioTransport(tt.ctx, stdioConfig{command: tt.command, logger: coreTLogger(t)}, transportHooks{})
			if err == nil {
				_ = tr.Close(t.Context())
				t.Fatal("newStdioTransport succeeded, want an error")
			}
			if tr != nil {
				t.Errorf("transport = %v, want nil on error", tr)
			}
			if !errors.Is(err, errUnreachable) {
				t.Errorf("err = %v, want errUnreachable", err)
			}
			if tt.also != nil && !errors.Is(err, tt.also) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.also)
			}
		})
	}
}

func TestStdio_ConcurrentCallsOutOfOrder(t *testing.T) {
	tr := stdioTStart(t, stdioTHelper(t, "stdio-script"), transportHooks{})
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			// Later calls sleep less, so replies come back reversed.
			params := map[string]int{"ms": (n - i) * 10, "token": i}
			resp, err := stdioTCall(t, tr, "sleep", params, callInfo{cancellable: true})
			if err != nil {
				errs <- fmt.Errorf("call %d: %w", i, err)
				return
			}
			var got struct{ Token int }
			if err := json.Unmarshal(resp.Result, &got); err != nil || got.Token != i {
				errs <- fmt.Errorf("call %d got token %d (%s, %v)", i, got.Token, resp.Result, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestStdio_ServerRequests(t *testing.T) {
	type seen struct {
		method string
		id     string
		ctxErr error
	}
	var mu sync.Mutex
	var got []seen
	hooks := transportHooks{onRequest: func(ctx context.Context, req *Request) *Response {
		mu.Lock()
		got = append(got, seen{req.Method, string(req.ID), ctx.Err()})
		mu.Unlock()
		if req.Method == methodSampling {
			return nil // no reply
		}
		return &Response{Result: json.RawMessage(`{"handled":"` + req.Method + `"}`)}
	}}
	tr := stdioTStart(t, stdioTHelper(t, "stdio-script"), hooks)

	stdioTMust(t, tr, "echo", nil, nil) // id 1

	// id 2 stays pending while the server sends a request with id 2.
	pending := make(chan error, 1)
	go func() {
		resp, err := stdioTCall(t, tr, "sleep", map[string]int{"ms": 500, "token": 42}, callInfo{})
		if err == nil && !strings.Contains(string(resp.Result), `"token":42`) {
			err = fmt.Errorf("pending call got %s", resp.Result)
		}
		pending <- err
	}()
	stdioTEventually(t, "call 2 to be in flight", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		_, ok := tr.pending[2]
		return ok
	})

	params := map[string]any{"requests": []map[string]any{
		{"id": "abc", "method": methodPing},
		{"id": 2, "method": methodRootsList},
		{"self": true, "method": methodElicitation},
		{"id": "quiet", "method": methodSampling, "wait_ms": 300},
	}}
	var res struct{ Replies []stdioTAsked }
	resp := stdioTMust(t, tr, "ask", params, &res)
	selfID := string(resp.ID)

	wantIDs := []string{`"abc"`, `2`, selfID, `"quiet"`}
	wantMethods := []string{methodPing, methodRootsList, methodElicitation, ""}
	if len(res.Replies) != len(wantIDs) {
		t.Fatalf("replies = %+v, want %d", res.Replies, len(wantIDs))
	}
	for i, a := range res.Replies {
		if string(a.ID) != wantIDs[i] {
			t.Errorf("reply %d asked id %s, want %s", i, a.ID, wantIDs[i])
		}
		if wantMethods[i] == "" {
			if string(a.Reply) != "null" {
				t.Errorf("reply %d = %s, want no reply for a nil hook result", i, a.Reply)
			}
			continue
		}
		var r Response
		if err := json.Unmarshal(a.Reply, &r); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		if string(r.ID) != wantIDs[i] || r.JSONRPC != jsonrpcVersion || !strings.Contains(string(r.Result), wantMethods[i]) {
			t.Errorf("reply %d = %s, want id %s answering %s", i, a.Reply, wantIDs[i], wantMethods[i])
		}
	}
	if err := <-pending; err != nil {
		t.Errorf("pending call with a colliding server id: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("hook saw %d requests, want 4: %+v", len(got), got)
	}
	for _, s := range got {
		if s.ctxErr != nil {
			t.Errorf("hook ctx for %s already done: %v", s.method, s.ctxErr)
		}
	}
}

func TestStdio_Notifications(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	var lifeCtx context.Context
	hooks := transportHooks{onNotification: func(ctx context.Context, n *Notification) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, n.Method)
		lifeCtx = ctx
	}}
	tr := stdioTStart(t, stdioTHelper(t, "stdio-script"), hooks)
	stdioTMust(t, tr, "notify", nil, nil)

	mu.Lock()
	got := strings.Join(methods, ",")
	ctx := lifeCtx
	mu.Unlock()
	if want := methodLogMessage + "," + methodToolsListChanged; got != want {
		t.Fatalf("notifications = %s, want %s (delivered before the response)", got, want)
	}
	if ctx.Err() != nil {
		t.Fatalf("hook ctx done while the transport is live: %v", ctx.Err())
	}
	_ = tr.Close(t.Context())
	if ctx.Err() == nil {
		t.Error("hook ctx still live after Close")
	}
}

func TestStdio_Batches(t *testing.T) {
	var mu sync.Mutex
	var notes, reqs []string
	hooks := transportHooks{
		onNotification: func(_ context.Context, n *Notification) {
			mu.Lock()
			notes = append(notes, n.Method)
			mu.Unlock()
		},
		onRequest: func(_ context.Context, req *Request) *Response {
			mu.Lock()
			reqs = append(reqs, req.Method+" "+string(req.ID))
			mu.Unlock()
			return &Response{Result: json.RawMessage(`{}`)}
		},
	}
	tr := stdioTStart(t, stdioTHelper(t, "stdio-script"), hooks)

	t.Run("mixed batch", func(t *testing.T) {
		var res struct{ Batched bool }
		stdioTMust(t, tr, "batch", nil, &res)
		if !res.Batched {
			t.Fatal("batched response not routed")
		}
		stdioTEventually(t, "reply to the batched request", func() bool {
			_, ok := stdioTStatsOf(t, tr).Replies[`"b1"`]
			return ok
		})
		mu.Lock()
		defer mu.Unlock()
		if len(notes) != 1 || notes[0] != methodLogMessage || len(reqs) != 1 || reqs[0] != `ping "b1"` {
			t.Errorf("notifications %v, requests %v; want the batch's notification and ping \"b1\"", notes, reqs)
		}
	})

	t.Run("responses to several calls in one batch", func(t *testing.T) {
		type held struct {
			resp *Response
			err  error
		}
		results := make(chan held, 2)
		for range 2 {
			go func() {
				resp, err := stdioTCall(t, tr, "hold", nil, callInfo{})
				results <- held{resp, err}
			}()
		}
		stdioTEventually(t, "two held calls", func() bool { return stdioTStatsOf(t, tr).Held == 2 })
		var rel struct{ Released int }
		stdioTMust(t, tr, "release", nil, &rel)
		if rel.Released != 2 {
			t.Fatalf("released %d, want 2", rel.Released)
		}
		for range 2 {
			h := <-results
			if h.err != nil {
				t.Fatalf("held call: %v", h.err)
			}
			var res struct {
				HeldID json.RawMessage `json:"held_id"`
			}
			if err := json.Unmarshal(h.resp.Result, &res); err != nil || string(res.HeldID) != string(h.resp.ID) {
				t.Errorf("held call id %s got result %s", h.resp.ID, h.resp.Result)
			}
		}
	})
}

func TestStdio_Cancellation(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-script")
	tr := stdioTStart(t, cfg, transportHooks{})

	tests := []struct {
		name       string
		info       callInfo
		cancelBy   func(context.Context) (context.Context, context.CancelFunc)
		wantErr    error
		wantReason string // "" = no notifications/cancelled expected
	}{
		{
			name: "deadline sends cancelled with reason timeout",
			info: callInfo{cancellable: true},
			cancelBy: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 100*time.Millisecond)
			},
			wantErr:    context.DeadlineExceeded,
			wantReason: "timeout",
		},
		{
			name: "cancel sends cancelled with reason cancelled",
			info: callInfo{cancellable: true},
			cancelBy: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			},
			wantErr:    context.Canceled,
			wantReason: "cancelled",
		},
		{
			name: "not cancellable sends nothing",
			info: callInfo{cancellable: false},
			cancelBy: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 100*time.Millisecond)
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := stdioTStatsOf(t, tr)
			ctx, cancel := tt.cancelBy(t.Context())
			defer cancel()
			start := time.Now()
			resp, err := tr.Call(ctx, &Request{Method: "sleep", Params: json.RawMessage(`{"ms":600,"late":true}`)}, tt.info)
			if !errors.Is(err, tt.wantErr) || resp != nil {
				t.Fatalf("Call = (%v, %v), want (nil, %v)", resp, err, tt.wantErr)
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Errorf("Call returned after %v, want promptly after ctx ended", elapsed)
			}
			id := tr.nextID.Load()

			// The server replies anyway; the late reply must be dropped
			// and the process must keep serving.
			stdioTEventually(t, "late reply", func() bool { return stdioTStatsOf(t, tr).Slept > before.Slept })
			after := stdioTStatsOf(t, tr)
			newCancels := after.Cancelled[len(before.Cancelled):]
			if tt.wantReason == "" {
				if len(newCancels) != 0 {
					t.Errorf("sent %+v, want no notifications/cancelled", newCancels)
				}
			} else if len(newCancels) != 1 || string(newCancels[0].RequestID) != fmt.Sprint(id) || newCancels[0].Reason != tt.wantReason {
				t.Errorf("cancellations = %+v, want one for id %d with reason %q", newCancels, id, tt.wantReason)
			}
			if !strings.Contains(logs.String(), fmt.Sprintf("dropping late or unknown response\" id=%d", id)) {
				t.Errorf("late reply for id %d not logged as dropped", id)
			}
			if stdioTIsDone(tr.Done()) {
				t.Fatal("transport finished after a cancelled call; it must never kill the server")
			}
		})
	}
}

func TestStdio_CrashMidCall(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-script")
	tr := stdioTStart(t, cfg, transportHooks{})
	stdioTMust(t, tr, "echo", nil, nil)

	_, err := stdioTCall(t, tr, "crash", nil, callInfo{cancellable: true})
	if !errors.Is(err, errConnectionLost) {
		t.Fatalf("crash: err = %v, want errConnectionLost", err)
	}
	stdioTWaitDone(t, tr.Done(), 5*time.Second, "Done")
	stdioTEventually(t, "exit WARN with stderr tail", func() bool {
		s := logs.String()
		return strings.Contains(s, "level=WARN") && strings.Contains(s, "exited unexpectedly") &&
			strings.Contains(s, "exit status 3") && strings.Contains(s, "boom: simulated crash")
	})

	if _, err := stdioTCall(t, tr, "echo", nil, callInfo{}); !errors.Is(err, errUnreachable) {
		t.Errorf("Call after exit: err = %v, want errUnreachable", err)
	}
	if err := tr.Notify(t.Context(), &Notification{Method: "x"}, callInfo{}); !errors.Is(err, errUnreachable) {
		t.Errorf("Notify after exit: err = %v, want errUnreachable", err)
	}
}

func TestStdio_SilentExitLogsStderr(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-silent-exit")
	tr := stdioTStart(t, cfg, transportHooks{})
	stdioTWaitDone(t, tr.Done(), 5*time.Second, "Done")
	stdioTEventually(t, "exit WARN", func() bool {
		s := logs.String()
		return strings.Contains(s, "exited unexpectedly") && strings.Contains(s, "exit status 1") &&
			strings.Contains(s, "fatal: missing API token")
	})
}

func TestStdio_CloseBeforeAnyMessageWarns(t *testing.T) {
	t.Run("never spoke", func(t *testing.T) {
		cfg, logs := stdioTLogs(t, "stdio-script")
		tr := stdioTStart(t, cfg, transportHooks{})
		_ = tr.Close(t.Context())
		stdioTEventually(t, "WARN", func() bool {
			return strings.Contains(logs.String(), "closed before it sent any JSON-RPC message")
		})
	})
	t.Run("spoke", func(t *testing.T) {
		cfg, logs := stdioTLogs(t, "stdio-script")
		tr := stdioTStart(t, cfg, transportHooks{})
		stdioTMust(t, tr, "echo", nil, nil)
		_ = tr.Close(t.Context())
		stdioTEventually(t, "exit log", func() bool { return strings.Contains(logs.String(), "stdio server exited") })
		if s := logs.String(); strings.Contains(s, "level=WARN") {
			t.Errorf("a requested shutdown logged a WARN:\n%s", s)
		}
	})
}

func TestStdio_GarbageAndNullIDs(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-script")
	tr := stdioTStart(t, cfg, transportHooks{})
	var res struct{ OK bool }
	stdioTMust(t, tr, "garbage", nil, &res)
	if !res.OK {
		t.Fatal("response after unparseable lines not routed")
	}
	stdioTMust(t, tr, "nullid", nil, &res)
	stdioTMust(t, tr, "nullid", nil, &res)
	if n := strings.Count(logs.String(), "error response with no id"); n != 1 {
		t.Errorf("null-id WARN logged %d times, want once per transport", n)
	}
	if !strings.Contains(logs.String(), "skipping unparseable stdout line") {
		t.Error("unparseable line not logged")
	}
	if stdioTIsDone(tr.Done()) {
		t.Error("garbage on stdout finished the transport")
	}
}

func TestStdio_BrokenStdinIsUnreachable(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-close-stdin")
	cfg.closeStdinWait = 100 * time.Millisecond
	tr := stdioTStart(t, cfg, transportHooks{})

	// Until the helper has closed its stdin a write may still land in
	// the pipe buffer; after that every write gets EPIPE.
	var err error
	stdioTEventually(t, "a broken-pipe write", func() bool {
		err = tr.Notify(t.Context(), &Notification{Method: "test/ping"}, callInfo{})
		return err != nil
	})
	if !errors.Is(err, errUnreachable) || errors.Is(err, errConnectionLost) {
		t.Fatalf("err = %v, want errUnreachable (nothing was delivered)", err)
	}
	stdioTWaitDone(t, tr.Done(), time.Second, "Done")
	// The server stopped reading on its own: its exit is still a WARN,
	// not hidden as a Thane-initiated teardown.
	stdioTEventually(t, "exit WARN", func() bool { return strings.Contains(logs.String(), "exited unexpectedly") })
}

func TestStdio_OversizeLineIsProtocolError(t *testing.T) {
	cfg, logs := stdioTLogs(t, "stdio-script")
	tr := stdioTStart(t, cfg, transportHooks{})
	_, err := stdioTCall(t, tr, "oversize", nil, callInfo{cancellable: true})
	if !errors.Is(err, errProtocol) || !errors.Is(err, errConnectionLost) {
		t.Fatalf("err = %v, want errProtocol and errConnectionLost", err)
	}
	stdioTWaitDone(t, tr.Done(), time.Second, "Done")
	stdioTWaitDone(t, tr.exited, 5*time.Second, "process exit after a framing failure")
	if !strings.Contains(logs.String(), "over the size limit") {
		t.Error("oversize message not logged")
	}
}

func TestStdio_CloseEscalation(t *testing.T) {
	const stdinWait, termWait = 500 * time.Millisecond, 500 * time.Millisecond
	tests := []struct {
		mode    string
		minTime time.Duration
		maxTime time.Duration
		signal  syscall.Signal // 0 = clean exit
	}{
		{"stdio-script", 0, stdinWait, 0},
		{"stdio-ignore-stdin", stdinWait, stdinWait + termWait, syscall.SIGTERM},
		{"stdio-stubborn", stdinWait + termWait, stdinWait + termWait + 2*time.Second, syscall.SIGKILL},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			cfg := stdioTHelper(t, tt.mode)
			cfg.closeStdinWait, cfg.termWait = stdinWait, termWait
			tr := stdioTStart(t, cfg, transportHooks{})
			stdioTMust(t, tr, "echo", nil, nil) // the helper is up and has set its signals

			start := time.Now()
			if err := tr.Close(t.Context()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			elapsed := time.Since(start)
			if elapsed < tt.minTime || elapsed > tt.maxTime {
				t.Errorf("Close took %v, want between %v and %v", elapsed, tt.minTime, tt.maxTime)
			}
			if !stdioTIsDone(tr.Done()) || !stdioTIsDone(tr.exited) {
				t.Fatal("Close returned before the process exited")
			}
			stdioTCheckSignal(t, tr.exitErr, tt.signal)
		})
	}

	t.Run("ctx expiry kills at once", func(t *testing.T) {
		cfg := stdioTHelper(t, "stdio-stubborn")
		cfg.closeStdinWait, cfg.termWait = 5*time.Second, 5*time.Second
		tr := stdioTStart(t, cfg, transportHooks{})
		stdioTMust(t, tr, "echo", nil, nil)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_ = tr.Close(ctx)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("Close took %v with a 100ms ctx", elapsed)
		}
		if !stdioTIsDone(tr.Done()) {
			t.Error("Done not closed when Close returned")
		}
		stdioTWaitDone(t, tr.exited, 2*time.Second, "exited")
		stdioTCheckSignal(t, tr.exitErr, syscall.SIGKILL)
	})
}

// stdioTCheckSignal asserts how the process ended (0 = exit status 0).
func stdioTCheckSignal(t *testing.T, err error, want syscall.Signal) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Errorf("exit = %v, want a clean exit", err)
		}
		return
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("exit = %v, want killed by %v", err, want)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != want {
		t.Errorf("exit = %v, want killed by %v", err, want)
	}
}

func TestStdio_WriteHonoursDeadline(t *testing.T) {
	cfg := stdioTHelper(t, "stdio-noread")
	cfg.closeStdinWait = 100 * time.Millisecond
	tr := stdioTStart(t, cfg, transportHooks{})

	// Fill the stdin pipe with atomic (< PIPE_BUF) lines until a write
	// blocks; it must give up at the ctx deadline with nothing written.
	params := json.RawMessage(`{"pad":"` + strings.Repeat("x", 1000) + `"}`)
	var err error
	for i := 0; i < 10_000 && err == nil; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		err = tr.Notify(ctx, &Notification{Method: "test/fill", Params: params}, callInfo{})
		cancel()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write: err = %v, want context.DeadlineExceeded", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := tr.Call(ctx, &Request{Method: "echo"}, callInfo{cancellable: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Call on a full pipe: err = %v, want context.Canceled", err)
	}
	if stdioTIsDone(tr.Done()) {
		t.Fatal("a write that sent nothing finished the transport")
	}
	_ = tr.Close(t.Context())
	stdioTCheckSignal(t, tr.exitErr, syscall.SIGTERM)
}

func TestStdio_StderrFloodDoesNotBlock(t *testing.T) {
	cfg := stdioTHelper(t, "stdio-script")
	cfg.logger = slog.New(slog.NewTextHandler(&coreTLogBuffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tr := stdioTStart(t, cfg, transportHooks{})

	stdioTMust(t, tr, "flood", map[string]int{"bytes": 8 << 20}, nil)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := stdioTCall(t, tr, "echo", map[string]int{"i": i}, callInfo{}); err != nil {
				t.Errorf("echo during the flood: %v", err)
			}
		})
	}
	wg.Wait()
	stdioTMust(t, tr, "flood-wait", nil, nil) // never returns if stderr backs up
	stdioTEventually(t, "flood tail", func() bool { return strings.HasSuffix(tr.stderrTail.String(), "flood done") })
	tail := tr.stderrTail.String()
	if !strings.Contains(tail, "…[truncated]") {
		t.Error("the 1 MiB stderr line was not truncated in the tail")
	}
	for line := range strings.SplitSeq(tail, "\n") {
		if len(line) > stdioStderrLineBytes+len(" …[truncated]") {
			t.Errorf("tail line of %d bytes, want <= %d", len(line), stdioStderrLineBytes)
		}
	}
}

func TestStdio_GrandchildHoldingStdout(t *testing.T) {
	cfg := stdioTHelper(t, "stdio-script")
	cfg.exitDrainWait = 10 * time.Second // the group kill, not this grace, must free stdout
	var mu sync.Mutex
	grandchild := 0
	hooks := transportHooks{onNotification: func(_ context.Context, n *Notification) {
		var p struct{ PID int }
		if json.Unmarshal(n.Params, &p) == nil && p.PID > 0 {
			mu.Lock()
			grandchild = p.PID
			mu.Unlock()
		}
	}}
	tr := stdioTStart(t, cfg, hooks)

	start := time.Now()
	_, err := stdioTCall(t, tr, "exit-orphan", nil, callInfo{cancellable: true})
	if !errors.Is(err, errConnectionLost) {
		t.Fatalf("err = %v, want errConnectionLost", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("connection loss took %v; the grandchild kept stdout open", elapsed)
	}
	mu.Lock()
	pid := grandchild
	mu.Unlock()
	if pid == 0 {
		t.Fatal("grandchild pid not reported")
	}
	stdioTEventually(t, "grandchild to die", func() bool { return !stdioTAlive(pid) })
}

// stdioTAlive reports whether pid is a live (non-zombie) process. On
// Linux a killed orphan can linger as a zombie when PID 1 does not reap,
// so /proc decides there; elsewhere kill(pid, 0) does.
func stdioTAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return !os.IsNotExist(err)
	}
	// Field 3 follows the parenthesised command name.
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}
