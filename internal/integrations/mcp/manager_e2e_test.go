package mcp

import (
	"context"
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
