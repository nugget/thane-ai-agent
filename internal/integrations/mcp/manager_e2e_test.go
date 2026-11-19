package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestManager_RealTransports drives the Manager's own transport
// factories: a legacy stdio helper process negotiated with
// protocol auto, and a modern Streamable HTTP server.
func TestManager_RealTransports(t *testing.T) {
	t.Parallel()
	helper := stdioTHelper(t, "mcp-legacy")
	web := e2eTHTTPStart(t, e2eTStyleModern)
	m := NewManager([]ServerConfig{
		{
			Name: "local", Transport: transportStdio, Protocol: protocolAuto,
			Command: helper.command, Args: helper.args, Env: helper.env,
			Tags: []string{"files"},
		},
		{
			Name: "web", Transport: transportHTTP, URL: web.URL + "/mcp",
			Headers: map[string]string{"Authorization": "Bearer test"},
			Tags:    []string{"web"},
		},
	}, coreTLogger(t))
	m.startWait = e2eTWait
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), e2eTWait)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	reg := tools.NewEmptyRegistry()
	m.Start(t.Context(), reg)
	want := []string{"mcp_local_echo", "mcp_local_sleep", "mcp_web_echo", "mcp_web_sleep", "mcp_web_weather"}
	if got := reg.AllToolNames(); !slices.Equal(got, want) {
		t.Fatalf("registered = %v, want %v", got, want)
	}
	for _, name := range []string{"mcp_local_echo", "mcp_web_echo"} {
		out, err := reg.Get(name).Handler(t.Context(), map[string]any{"text": "ping " + name})
		if err != nil || !strings.Contains(out, "ping "+name) {
			t.Errorf("%s = %q, %v", name, out, err)
		}
	}
	out, err := reg.Get("mcp_web_weather").Handler(t.Context(), map[string]any{"region": "eu-west"})
	if err != nil || out == "" {
		t.Errorf("weather = %q, %v", out, err)
	}
	for _, name := range []string{"local", "web"} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		if err := m.Probe(ctx, name); err != nil {
			t.Errorf("Probe(%s) = %v", name, err)
		}
		cancel()
	}
	web.checkClean(t)
}

// TestManager_StartCtxCancelKeepsServersUntilClose: shutdown cancels
// the app ctx that Start received before it drains in-flight requests.
// The servers must outlive that: an in-flight call completes, the
// server stays healthy, and only Close ends it, after which calls say
// Thane is shutting down.
func TestManager_StartCtxCancelKeepsServersUntilClose(t *testing.T) {
	t.Parallel()
	helper := stdioTHelper(t, e2eTModeLegacy)
	m := NewManager([]ServerConfig{{
		Name: "local", Transport: transportStdio, Protocol: protocolLegacy,
		Command: helper.command, Args: helper.args, Env: helper.env,
		Tags: []string{"files"},
	}}, coreTLogger(t))
	m.startWait = e2eTWait
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), e2eTWait)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	appCtx, signal := context.WithCancel(t.Context())
	defer signal()
	reg := tools.NewEmptyRegistry()
	m.Start(appCtx, reg)
	sleep := reg.Get("mcp_local_sleep")
	if sleep == nil {
		t.Fatalf("mcp_local_sleep not registered; registry has %v", reg.AllToolNames())
	}
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sleep.Handler(context.WithoutCancel(t.Context()), map[string]any{"ms": 1500})
		done <- result{out, err}
	}()
	client := m.byName["local"].client
	e2eTEventually(t, "the server holding the sleep call", func() bool {
		return e2eTStatsOf(t, client).Seen[methodToolsCall] >= 1
	})

	signal() // what the SIGTERM handler does to the app ctx
	var res result
	select {
	case res = <-done:
	case <-time.After(e2eTWait):
		t.Fatal("the in-flight call did not return")
	}
	if res.err != nil || res.out != "slept 1500ms" {
		t.Fatalf("in-flight call across the signal = %q, %v; want it to complete", res.out, res.err)
	}
	if err := m.Probe(t.Context(), "local"); err != nil {
		t.Errorf("Probe after the signal = %v, want healthy until Close", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), e2eTWait)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := reg.Get("mcp_local_echo").Handler(t.Context(), map[string]any{"text": "x"})
	var unavailable tools.ErrUnavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != reasonShuttingDown {
		t.Errorf("call after Close = %v, want ErrUnavailable(%q)", err, reasonShuttingDown)
	}
}
