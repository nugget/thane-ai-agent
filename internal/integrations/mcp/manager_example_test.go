package mcp

import (
	"context"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// ExampleNewManager shows the startup wiring: build the manager from
// configuration, start it before capability tags are resolved so the
// tools of servers that are ready land on the shared registry, use it
// as the loop's dynamic tool source for tools that appear later, and
// close it on shutdown.
func ExampleNewManager() {
	ctx := context.Background()
	reg := tools.NewEmptyRegistry()

	mgr := NewManager([]ServerConfig{
		{
			Name:      "files",
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/srv/shared"},
			Tags:      []string{"files"},
		},
		{
			Name:      "game",
			Transport: "http",
			URL:       "http://localhost:11212/mcp",
			Protocol:  "legacy",
			Tags:      []string{"game"},
		},
	}, slog.Default())
	mgr.Start(ctx, reg)

	// Per run: tools that are not on the shared registry.
	late, tagAdditions := mgr.Snapshot()
	_, _ = late, tagAdditions

	// connwatch health probe for one server.
	_ = mgr.Probe(ctx, "files")

	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = mgr.Close(closeCtx)
}
