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
// boot land on the shared registry that capability-tag resolution,
// tag_inspect, the manifest tool counts and delegate model routing
// read. Servers that are down keep reconnecting in the background.
// Tools that first appear later, and changed definitions of boot-time
// tools, reach runs through the dynamic tool overlay installed in
// initServers. Every run goes through the agent loop, which applies the
// overlay, so they reach the main loop and any delegate whose tag scope
// covers them.
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
	// watcher reports status and never acts on it. Every configured
	// server is a dependency: while one is down or not yet connected,
	// /health reports degraded and system_health shows it as failed.
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
// Config validation already rejects a cwd that cannot, with the same
// resolver; the check here guards callers that skipped validation.
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
// tool override names. Resolution keeps tags that have no tools, so the
// tags of a server that is down at boot are still known to tag
// resolution and tag_inspect. That does not make a tag discoverable:
// the tag_activate menu and the manifest talent list only menu
// (trailhead) and protected tags, so a new leaf tag such as "game" is
// reached through a trailhead's next_tags, core: true, or channel and
// loop tags. cfg is never mutated.
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

// capabilityTagExcludes returns, per capability tag, the tool names the
// operator removed with capability_tags.<tag>.exclude. Startup tag
// resolution drops these names from the static tag index; the dynamic
// tool overlay drops them from its tag additions with the same sets, so
// an excluded tool stays out of the tag when it first appears after
// startup or when its definition changes. Tags with no excludes are
// omitted; nil means nothing is excluded.
func capabilityTagExcludes(cfg *config.Config) map[string]map[string]struct{} {
	var out map[string]map[string]struct{}
	for tag, tagCfg := range cfg.CapabilityTags {
		for _, name := range tagCfg.Exclude {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if out == nil {
				out = make(map[string]map[string]struct{})
			}
			if out[tag] == nil {
				out[tag] = make(map[string]struct{})
			}
			out[tag][name] = struct{}{}
		}
	}
	return out
}
