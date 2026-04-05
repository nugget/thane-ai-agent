package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// sanitizeRe matches characters that are not lowercase alphanumeric or underscore.
var sanitizeRe = regexp.MustCompile(`[^a-z0-9_]`)

// ToolOverride carries operator-supplied metadata for a bridged MCP tool.
type ToolOverride struct {
	Enabled     *bool
	Tags        []string
	Description string
}

// BridgeOptions control how MCP tools are bridged into Thane's registry.
type BridgeOptions struct {
	Include       []string
	Exclude       []string
	DefaultTags   []string
	ToolOverrides map[string]ToolOverride
}

// BridgeTools discovers tools from an MCP client and registers them on
// the given tool registry. Tool names are namespaced as
// "mcp_{serverName}_{toolName}" to avoid collisions with native tools.
//
// The include and exclude lists control which MCP tools are bridged:
//   - If include is non-empty, only tools whose MCP names appear in it are registered.
//   - If exclude is non-empty, tools whose MCP names appear in it are skipped.
//   - If both are empty, all tools are registered.
//
// BridgeTools returns the number of tools registered.
func BridgeTools(ctx context.Context, client *Client, serverName string, registry *tools.Registry, opts BridgeOptions, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}

	mcpTools, err := client.ListTools(ctx)
	if err != nil {
		return 0, fmt.Errorf("list tools from %s: %w", serverName, err)
	}

	includeSet := toSet(opts.Include)
	excludeSet := toSet(opts.Exclude)

	count := 0
	for _, td := range mcpTools {
		override := opts.ToolOverrides[td.Name]
		if override.Enabled != nil && !*override.Enabled {
			continue
		}
		if len(includeSet) > 0 {
			if !includeSet[td.Name] {
				continue
			}
		} else if excludeSet[td.Name] {
			continue
		}

		name := ToolName(serverName, td.Name)
		registry.Register(bridgeTool(client, serverName, name, td, opts.DefaultTags, override))
		count++

		logger.Debug("bridged MCP tool",
			"mcp_name", td.Name,
			"thane_name", name,
			"server", serverName,
		)
	}

	return count, nil
}

// ToolName generates a namespaced Thane tool name from an MCP server
// name and tool name. Both components are sanitized to contain only
// lowercase alphanumeric characters and underscores.
func ToolName(serverName, mcpToolName string) string {
	server := sanitize(serverName)
	tool := sanitize(mcpToolName)
	return fmt.Sprintf("mcp_%s_%s", server, tool)
}

// bridgeTool creates a Thane tool that proxies calls to an MCP server.
func bridgeTool(client *Client, serverName, name string, td ToolDefinition, defaultTags []string, override ToolOverride) *tools.Tool {
	// Capture the original MCP tool name for the call.
	mcpName := td.Name
	description := td.Description
	if desc := strings.TrimSpace(override.Description); desc != "" {
		description = desc
	}
	tags := defaultTags
	if len(override.Tags) > 0 {
		tags = override.Tags
	}

	return &tools.Tool{
		Name:        name,
		Description: description,
		Parameters:  td.InputSchema,
		CanonicalID: fmt.Sprintf("mcp:%s/%s", sanitize(serverName), sanitize(mcpName)),
		Source:      "mcp",
		Origin:      serverName,
		DefaultTags: append([]string(nil), tags...),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return client.CallTool(ctx, mcpName, args)
		},
	}
}

// sanitize converts a name to lowercase and replaces non-alphanumeric
// characters (except underscore) with underscores. Consecutive
// underscores are collapsed and leading/trailing underscores are trimmed.
func sanitize(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "-", "_")
	s = sanitizeRe.ReplaceAllString(s, "_")

	// Collapse consecutive underscores.
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}

	return strings.Trim(s, "_")
}

// toSet converts a string slice to a set for O(1) lookups.
func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}
