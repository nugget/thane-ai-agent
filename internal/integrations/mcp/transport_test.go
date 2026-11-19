package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// coreTLogWriter forwards log lines to t.Log until the test's cleanup
// runs, so goroutines that outlive a test never call t.Log late.
type coreTLogWriter struct {
	mu   sync.Mutex
	t    testing.TB
	done bool
}

func (w *coreTLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (w *coreTLogWriter) stop() {
	w.mu.Lock()
	w.done = true
	w.mu.Unlock()
}

// coreTLogger returns a Debug-level logger that writes to t.Log (shown
// on failure or with -v). Safe to use from goroutines that outlive the
// test: output after cleanup is discarded. Any stage's tests may use it.
func coreTLogger(t testing.TB) *slog.Logger {
	t.Helper()
	w := &coreTLogWriter{t: t}
	t.Cleanup(w.stop)
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// coreTLogBuffer is a goroutine-safe log sink for asserting on output.
type coreTLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *coreTLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *coreTLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *coreTLogBuffer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// coreTTransport is a minimal in-memory Transport proving the interface
// and factory shapes compile together.
type coreTTransport struct {
	once sync.Once
	done chan struct{}
	last callInfo
}

var _ Transport = (*coreTTransport)(nil)

func (c *coreTTransport) Call(ctx context.Context, req *Request, info callInfo) (*Response, error) {
	c.last = info
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Response{JSONRPC: jsonrpcVersion, ID: intID(1), Result: json.RawMessage(`{}`)}, nil
}

func (c *coreTTransport) Notify(ctx context.Context, n *Notification, info callInfo) error {
	c.last = info
	return ctx.Err()
}

func (c *coreTTransport) Close(context.Context) error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *coreTTransport) Done() <-chan struct{} { return c.done }

func TestTransport_FactoryShape(t *testing.T) {
	var factory transportFactory = func(ctx context.Context, hooks transportHooks) (Transport, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &coreTTransport{done: make(chan struct{})}, nil
	}
	tr, err := factory(t.Context(), transportHooks{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	info := callInfo{
		protocolVersion: modernProtocolVersion,
		modern:          true,
		name:            "get_weather",
		paramHeaders:    map[string]string{"Region": "us-west1"},
		cancellable:     true,
	}
	if _, err := tr.Call(t.Context(), &Request{Method: methodToolsCall}, info); err != nil {
		t.Fatalf("Call: %v", err)
	}
	ct, ok := tr.(*coreTTransport)
	if !ok {
		t.Fatalf("factory returned %T", tr)
	}
	if ct.last.name != "get_weather" || !ct.last.modern || !ct.last.cancellable ||
		ct.last.protocolVersion != modernProtocolVersion || ct.last.paramHeaders["Region"] != "us-west1" {
		t.Errorf("callInfo not passed through: %+v", ct.last)
	}
	if err := tr.Notify(t.Context(), &Notification{Method: methodInitialized}, callInfo{}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	for range 2 {
		if err := tr.Close(t.Context()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	select {
	case <-tr.Done():
	default:
		t.Error("Done not closed after Close")
	}
}

func TestTransport_CancelledNotification(t *testing.T) {
	tests := []struct {
		name   string
		id     json.RawMessage
		reason string
		want   string
	}{
		{name: "with reason", id: intID(7), reason: "timeout", want: `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"timeout"}}`},
		{name: "without reason", id: intID(8), want: `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":8}}`},
		{name: "string id verbatim", id: json.RawMessage(`"abc"`), reason: "cancelled", want: `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"abc","reason":"cancelled"}}`},
		{name: "invalid id drops params", id: json.RawMessage(`{`), want: `{"jsonrpc":"2.0","method":"notifications/cancelled"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := cancelledNotification(tt.id, tt.reason)
			b, err := json.Marshal(n)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("cancelledNotification = %s, want %s", b, tt.want)
			}
		})
	}
}

func TestTransport_CancelNotifyContext(t *testing.T) {
	type ctxKey struct{}
	callCtx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "logger"))
	cancel() // the call's ctx has ended; the cancellation must still go out

	ctx, stop := cancelNotifyContext(callCtx)
	defer stop()
	if ctx.Err() != nil {
		t.Fatalf("cancel-notify ctx inherited cancellation: %v", ctx.Err())
	}
	if ctx.Value(ctxKey{}) != "logger" {
		t.Error("cancel-notify ctx lost the call ctx's values")
	}
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) > cancelNotifyTimeout || time.Until(dl) <= 0 {
		t.Errorf("deadline = %v (ok=%v), want within %v", time.Until(dl), ok, cancelNotifyTimeout)
	}
	stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("stop did not cancel: %v", ctx.Err())
	}
}

// coreTDecodeOne decodes a single inbound message.
func coreTDecodeOne(t *testing.T, raw string) *wireMessage {
	t.Helper()
	msgs, err := decodeMessages([]byte(raw))
	if err != nil || len(msgs) != 1 {
		t.Fatalf("decodeMessages(%s) = %v, %v", raw, msgs, err)
	}
	return &msgs[0]
}

type coreTReply struct {
	resp        *Response
	hasDeadline bool
	deadlineIn  time.Duration
}

func TestTransport_DispatchInboundRequest(t *testing.T) {
	type ctxKey struct{}
	lifeCtx := context.WithValue(t.Context(), ctxKey{}, "life")

	tests := []struct {
		name    string
		raw     string
		reply   *Response // what onRequest returns
		wantRaw string    // expected marshalled reply ("" = none)
	}{
		{
			name:    "ping with int id",
			raw:     `{"jsonrpc":"2.0","id":3,"method":"ping"}`,
			reply:   &Response{Result: json.RawMessage(`{}`)},
			wantRaw: `{"jsonrpc":"2.0","id":3,"result":{}}`,
		},
		{
			name:    "string id echoed verbatim",
			raw:     `{"jsonrpc":"2.0","id":"srv-1","method":"roots/list"}`,
			reply:   &Response{Error: &RPCError{Code: codeMethodNotFound, Message: "Method not found"}},
			wantRaw: `{"jsonrpc":"2.0","id":"srv-1","error":{"code":-32601,"message":"Method not found"}}`,
		},
		{
			name:    "hook id and version overridden",
			raw:     `{"jsonrpc":"2.0","id":9,"method":"elicitation/create"}`,
			reply:   &Response{JSONRPC: "1.0", ID: json.RawMessage(`12345`), Error: &RPCError{Code: codeInvalidParams, Message: "unsupported"}},
			wantRaw: `{"jsonrpc":"2.0","id":9,"error":{"code":-32602,"message":"unsupported"}}`,
		},
		{
			name:    "empty reply gets an empty result",
			raw:     `{"jsonrpc":"2.0","id":4,"method":"ping"}`,
			reply:   &Response{},
			wantRaw: `{"jsonrpc":"2.0","id":4,"result":{}}`,
		},
		{
			name: "nil reply sends nothing",
			raw:  `{"jsonrpc":"2.0","id":5,"method":"sampling/createMessage"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hookDone := make(chan *Request, 1)
			replies := make(chan coreTReply, 1)
			var hookCtxValue any
			hooks := transportHooks{
				onRequest: func(ctx context.Context, req *Request) *Response {
					hookCtxValue = ctx.Value(ctxKey{})
					defer func() { hookDone <- req }()
					return tt.reply
				},
				onNotification: func(context.Context, *Notification) { t.Error("onNotification called for a request") },
			}
			reply := func(ctx context.Context, resp *Response) error {
				dl, ok := ctx.Deadline()
				replies <- coreTReply{resp: resp, hasDeadline: ok, deadlineIn: time.Until(dl)}
				return nil
			}
			var original Response
			if tt.reply != nil {
				original = *tt.reply
			}

			dispatchInbound(lifeCtx, hooks, coreTDecodeOne(t, tt.raw), reply, coreTLogger(t))

			select {
			case req := <-hookDone:
				if hookCtxValue != "life" {
					t.Error("onRequest did not receive the lifetime ctx")
				}
				if req.Method == "" || !hasID(req.ID) {
					t.Errorf("onRequest got %+v", req)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("onRequest never ran")
			}

			if tt.wantRaw == "" {
				select {
				case r := <-replies:
					t.Fatalf("unexpected reply %+v", r.resp)
				case <-time.After(50 * time.Millisecond):
				}
				return
			}
			select {
			case r := <-replies:
				b, err := json.Marshal(r.resp)
				if err != nil {
					t.Fatalf("Marshal: %v", err)
				}
				if string(b) != tt.wantRaw {
					t.Errorf("reply = %s, want %s", b, tt.wantRaw)
				}
				if !r.hasDeadline || r.deadlineIn > replyTimeout || r.deadlineIn <= 0 {
					t.Errorf("reply ctx deadline: has=%v in=%v, want within %v", r.hasDeadline, r.deadlineIn, replyTimeout)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no reply sent")
			}
			if tt.reply != nil && (tt.reply.JSONRPC != original.JSONRPC || string(tt.reply.ID) != string(original.ID) ||
				string(tt.reply.Result) != string(original.Result)) {
				t.Errorf("hook's Response was mutated: %+v", tt.reply)
			}
		})
	}
}

func TestTransport_DispatchInboundRequestRunsConcurrently(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	hooks := transportHooks{onRequest: func(context.Context, *Request) *Response {
		started <- struct{}{}
		<-release
		return nil
	}}
	m := coreTDecodeOne(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	// Both dispatches must return while the first hook is still blocked:
	// a slow hook never stalls the transport's reader goroutine.
	dispatchInbound(t.Context(), hooks, m, nil, coreTLogger(t))
	dispatchInbound(t.Context(), hooks, m, nil, coreTLogger(t))
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("hook did not start")
		}
	}
	close(release)
}

func TestTransport_DispatchInboundReplyErrorIsLogged(t *testing.T) {
	var logs coreTLogBuffer
	sent := make(chan struct{})
	hooks := transportHooks{onRequest: func(context.Context, *Request) *Response {
		return &Response{Result: json.RawMessage(`{}`)}
	}}
	reply := func(context.Context, *Response) error {
		defer close(sent)
		return errors.New("pipe closed")
	}
	dispatchInbound(t.Context(), hooks, coreTDecodeOne(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`), reply, logs.logger())
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("reply never attempted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "pipe closed") {
		if time.Now().After(deadline) {
			t.Fatalf("reply error not logged; logs: %s", logs.String())
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(logs.String(), "level=DEBUG") {
		t.Errorf("reply error not at Debug: %s", logs.String())
	}
}

func TestTransport_DispatchInboundDrops(t *testing.T) {
	doneCtx, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		raw     string
		noHooks bool
	}{
		{name: "request without hook", ctx: t.Context(), raw: `{"jsonrpc":"2.0","id":1,"method":"ping"}`, noHooks: true},
		{name: "request after lifetime ended", ctx: doneCtx, raw: `{"jsonrpc":"2.0","id":1,"method":"ping"}`},
		{name: "response", ctx: t.Context(), raw: `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{name: "invalid", ctx: t.Context(), raw: `{"jsonrpc":"2.0","id":1}`},
		{name: "notification without hook", ctx: t.Context(), raw: `{"jsonrpc":"2.0","method":"notifications/progress"}`, noHooks: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs coreTLogBuffer
			hooks := transportHooks{}
			if !tt.noHooks {
				hooks = transportHooks{
					onRequest: func(context.Context, *Request) *Response {
						t.Error("onRequest called")
						return nil
					},
					onNotification: func(context.Context, *Notification) { t.Error("onNotification called") },
				}
			}
			reply := func(context.Context, *Response) error {
				t.Error("reply called")
				return nil
			}
			dispatchInbound(tt.ctx, hooks, coreTDecodeOne(t, tt.raw), reply, logs.logger())
			time.Sleep(20 * time.Millisecond) // let a wrongly spawned hook goroutine run
			if tt.name != "notification without hook" && !strings.Contains(logs.String(), "level=DEBUG") {
				t.Errorf("drop not logged at Debug: %q", logs.String())
			}
		})
	}
}

func TestTransport_DispatchInboundNotificationIsSynchronous(t *testing.T) {
	type ctxKey struct{}
	lifeCtx := context.WithValue(t.Context(), ctxKey{}, "life")
	var got []*Notification
	hooks := transportHooks{
		onRequest: func(context.Context, *Request) *Response {
			t.Error("onRequest called for a notification")
			return nil
		},
		onNotification: func(ctx context.Context, n *Notification) {
			if ctx.Value(ctxKey{}) != "life" {
				t.Error("onNotification did not receive the lifetime ctx")
			}
			got = append(got, n)
		},
	}
	reply := func(context.Context, *Response) error {
		t.Error("reply called for a notification")
		return nil
	}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"jsonrpc":"2.0","id":null,"method":"notifications/message","params":{"level":"info","data":"hi"}}`,
	} {
		dispatchInbound(lifeCtx, hooks, coreTDecodeOne(t, raw), reply, nil)
	}
	// No synchronisation needed: the hook ran before dispatchInbound returned.
	if len(got) != 2 || got[0].Method != methodToolsListChanged || got[1].Method != methodLogMessage ||
		string(got[1].Params) != `{"level":"info","data":"hi"}` {
		t.Errorf("notifications = %+v", got)
	}
}

func TestTransport_CoreTLoggerIsSafeAfterTest(t *testing.T) {
	var logger *slog.Logger
	t.Run("inner", func(t *testing.T) {
		logger = coreTLogger(t)
		logger.Debug("during test")
	})
	// The inner test has finished; this must not panic or race.
	logger.Info("after test")
}
