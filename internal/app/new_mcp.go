package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/integrations/mcp"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

// mcpCloseTimeout bounds stopping every MCP server on shutdown.
const mcpCloseTimeout = 10 * time.Second

// initMCP starts the configured MCP servers. It runs inside
// initChannels, before delegate creation and long before
// finalizeCapabilityTags, so the tools of servers that are ready at
// boot land on the shared registry where delegates and the
// capability-tag snapshot see them. Servers that are down keep
// reconnecting in the background; their tools reach the main loop
// through the dynamic tool overlay installed in initServers.
func (a *App) initMCP(s *newState) error {
	if len(a.cfg.MCP.Servers) == 0 {
		return nil
	}
	servers, err := mcpServerConfigs(a.cfg.MCP.Servers, s.resolver)
	if err != nil {
		return err
	}

	mgr := mcp.NewManager(servers, a.logger)
	mgr.Start(s.ctx, a.loop.Tools())
	a.mcpManager = mgr
	a.onCloseErr("mcp", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), mcpCloseTimeout)
		defer cancel()
		return mgr.Close(ctx)
	})

	// Health only: each server's own supervisor reconnects, so the
	// watcher reports status and never acts on it.
	for _, srv := range servers {
		name := srv.Name
		a.connMgr.Watch(s.ctx, connwatch.WatcherConfig{
			Name:    "mcp-" + name,
			Probe:   func(ctx context.Context) error { return mgr.Probe(ctx, name) },
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  a.logger,
		})
	}
	return nil
}

// mcpServerConfigs maps validated config onto mcp.ServerConfig. A stdio
// cwd is expanded like other configured paths and must end up absolute.
func mcpServerConfigs(in []config.MCPServerConfig, resolver *paths.Resolver) ([]mcp.ServerConfig, error) {
	out := make([]mcp.ServerConfig, 0, len(in))
	for _, srv := range in {
		var dir string
		if srv.Transport == "stdio" && strings.TrimSpace(srv.Cwd) != "" {
			dir = resolvePath(strings.TrimSpace(srv.Cwd), resolver)
			if !filepath.IsAbs(dir) {
				return nil, fmt.Errorf("mcp.servers[%s].cwd %q must resolve to an absolute path (got %q)", srv.Name, srv.Cwd, dir)
			}
		}
		out = append(out, mcp.ServerConfig{
			Name:           srv.Name,
			Transport:      srv.Transport,
			Protocol:       srv.Protocol,
			Command:        srv.Command,
			Args:           append([]string(nil), srv.Args...),
			Env:            append([]string(nil), srv.Env...),
			Dir:            dir,
			URL:            srv.URL,
			Headers:        srv.Headers,
			ConnectTimeout: srv.ConnectTimeout,
			CallTimeout:    srv.CallTimeout,
			Include:        append([]string(nil), srv.IncludeTools...),
			Exclude:        append([]string(nil), srv.ExcludeTools...),
			Tags:           append([]string(nil), srv.Tags...),
			ToolOverrides:  mcpToolOverrides(srv.Tools),
		})
	}
	return out, nil
}

func mcpToolOverrides(cfg map[string]config.MCPToolConfig) map[string]mcp.ToolOverride {
	if len(cfg) == 0 {
		return nil
	}
	out := make(map[string]mcp.ToolOverride, len(cfg))
	for name, toolCfg := range cfg {
		out[name] = mcp.ToolOverride{
			Enabled:     toolCfg.Enabled,
			Tags:        append([]string(nil), toolCfg.Tags...),
			Description: toolCfg.Description,
		}
	}
	return out
}

// mcpCapabilityTagOverrides returns a new map: a copy of the configured
// capability tags plus an empty entry for every tag an MCP server or
// tool override names. Resolution keeps tags that have no tools, so a
// server that is down at boot still has its tags in the tag_activate
// manifest and tag_inspect. cfg is never mutated.
func mcpCapabilityTagOverrides(cfg *config.Config) map[string]config.CapabilityTagConfig {
	out := make(map[string]config.CapabilityTagConfig, len(cfg.CapabilityTags))
	for tag, tagCfg := range cfg.CapabilityTags {
		out[tag] = tagCfg
	}
	add := func(tags []string) {
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			if _, ok := out[tag]; !ok {
				out[tag] = config.CapabilityTagConfig{}
			}
		}
	}
	for _, srv := range cfg.MCP.Servers {
		add(srv.Tags)
		for _, toolCfg := range srv.Tools {
			add(toolCfg.Tags)
		}
	}
	return out
}
