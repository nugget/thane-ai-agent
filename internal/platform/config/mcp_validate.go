package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

// MCP server defaults and protocol values (see MCPServerConfig).
const (
	mcpProtocolAuto          = "auto"
	mcpProtocolModern        = "modern"
	mcpProtocolLegacy        = "legacy"
	defaultMCPConnectTimeout = 30 * time.Second
	defaultMCPCallTimeout    = 2 * time.Minute
)

// validateMCP checks the MCP server configuration for consistency. It
// rejects only settings that can never work; recoverable oddities
// (reserved header names, env entries without '=') are dropped with a
// warning when the servers start, so configs that booted before keep
// booting.
func (c *Config) validateMCP() error {
	names := make(map[string]bool, len(c.MCP.Servers))
	sanitized := make(map[string]string, len(c.MCP.Servers))
	for i, srv := range c.MCP.Servers {
		if srv.Name == "" {
			return fmt.Errorf("mcp.servers[%d].name must not be empty", i)
		}
		if names[srv.Name] {
			return fmt.Errorf("mcp.servers[%d].name %q is a duplicate", i, srv.Name)
		}
		names[srv.Name] = true

		// Tool names are mcp_<sanitized server>_<sanitized tool>, so
		// the sanitized server name must be non-empty and unique.
		key := sanitizeMCPName(srv.Name)
		if key == "" {
			return fmt.Errorf("mcp.servers[%d].name %q must contain at least one letter or digit (it prefixes the bridged tool names)", i, srv.Name)
		}
		if other, dup := sanitized[key]; dup {
			return fmt.Errorf("mcp.servers[%d].name %q and %q both become %q in tool names; rename one of them", i, srv.Name, other, key)
		}
		sanitized[key] = srv.Name

		switch srv.Transport {
		case "stdio":
			if srv.Command == "" {
				return fmt.Errorf("mcp.servers[%d] (%s): stdio transport requires a command", i, srv.Name)
			}
			if cwd := strings.TrimSpace(srv.Cwd); cwd != "" && !filepath.IsAbs(c.resolveMCPCwd(cwd)) {
				return fmt.Errorf("mcp.servers[%d] (%s): cwd %q must resolve to an absolute path (use an absolute path, ~/..., or a configured root prefix such as kb:)", i, srv.Name, srv.Cwd)
			}
		case "http":
			if srv.URL == "" {
				return fmt.Errorf("mcp.servers[%d] (%s): http transport requires a url", i, srv.Name)
			}
			u, err := url.Parse(srv.URL)
			if err != nil {
				// *url.Error repeats the whole URL, userinfo and query
				// included; report only the reason.
				var urlErr *url.Error
				if errors.As(err, &urlErr) {
					err = urlErr.Err
				}
				return fmt.Errorf("mcp.servers[%d] (%s): url does not parse: %w", i, srv.Name, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("mcp.servers[%d] (%s): url scheme %q invalid (expected http or https)", i, srv.Name, u.Scheme)
			}
			if u.Host == "" {
				return fmt.Errorf("mcp.servers[%d] (%s): url must include a host", i, srv.Name)
			}
		default:
			return fmt.Errorf("mcp.servers[%d] (%s): transport %q invalid (expected stdio or http)", i, srv.Name, srv.Transport)
		}

		switch srv.Protocol {
		case "", mcpProtocolAuto, mcpProtocolModern, mcpProtocolLegacy:
		default:
			return fmt.Errorf("mcp.servers[%d] (%s): protocol %q invalid (expected auto, modern or legacy)", i, srv.Name, srv.Protocol)
		}
		if srv.ConnectTimeout < 0 {
			return fmt.Errorf("mcp.servers[%d] (%s): connect_timeout %s must be non-negative", i, srv.Name, srv.ConnectTimeout)
		}
		if srv.CallTimeout < 0 {
			return fmt.Errorf("mcp.servers[%d] (%s): call_timeout %s must be non-negative", i, srv.Name, srv.CallTimeout)
		}

		if len(srv.IncludeTools) > 0 && len(srv.ExcludeTools) > 0 {
			return fmt.Errorf("mcp.servers[%d] (%s): cannot set both include_tools and exclude_tools", i, srv.Name)
		}
	}
	return nil
}

// resolveMCPCwd expands a stdio cwd the way the running app does: a
// root prefix through the resolver built from Paths (the app builds the
// same one at startup), then a leading ~. A result that is still not
// absolute would stop Thane from starting, so validation rejects it.
func (c *Config) resolveMCPCwd(cwd string) string {
	dir, _ := paths.New(c.Paths).Resolve(cwd) // a nil resolver returns cwd
	return paths.ExpandHome(dir)
}

// sanitizeMCPName mirrors the MCP bridge's name sanitizer (package mcp
// cannot be imported here): lowercase, every rune outside [a-z0-9_]
// becomes '_', runs of '_' collapse, and leading/trailing '_' are
// trimmed. A parity test in package mcp keeps the two in step.
func sanitizeMCPName(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
