package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
)

// protocolVersion is the MCP protocol version we advertise during initialization.
const protocolVersion = "2024-11-05"

// ToolDefinition is an MCP tool as returned by tools/list.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ContentBlock is a single content item in a tools/call response.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// callToolResult is the result payload of a tools/call response.
type callToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// toolsListResult is the result payload of a tools/list response.
type toolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// serverInfo is returned in the initialize response.
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// serverCapabilities describes what an MCP server supports.
type serverCapabilities struct {
	Tools *struct{} `json:"tools,omitempty"`
}

// initializeResult is the full initialize response result.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      serverInfo         `json:"serverInfo"`
	Capabilities    serverCapabilities `json:"capabilities"`
}

// Client connects to a single MCP server and provides typed access to
// the MCP protocol operations (initialize, tools/list, tools/call).
type Client struct {
	name      string
	transport Transport
	logger    *slog.Logger
	nextID    atomic.Int64

	mu          sync.RWMutex
	initialized bool
	serverName  string
	serverVer   string
	tools       []ToolDefinition
}

// NewClient creates an MCP client for the given server. The transport
// determines how messages are delivered (stdio or HTTP).
func NewClient(name string, transport Transport, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{
		name:      name,
		transport: transport,
		logger:    logger.With("mcp_server", name),
	}
	c.nextID.Store(0)
	return c
}

// Name returns the server name this client is connected to.
func (c *Client) Name() string {
	return c.name
}

// Initialize performs the MCP handshake: sends an initialize request
// and then the notifications/initialized notification.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "thane",
			"version": buildinfo.Version,
		},
	}

	resp, err := c.send(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	var result initializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("unmarshal initialize result: %w", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.serverName = result.ServerInfo.Name
	c.serverVer = result.ServerInfo.Version
	c.mu.Unlock()

	c.logger.Info("MCP server initialized",
		"server_name", result.ServerInfo.Name,
		"server_version", result.ServerInfo.Version,
		"protocol_version", result.ProtocolVersion,
	)

	// Send the initialized notification to complete the handshake.
	if err := c.transport.Notify(ctx, NewNotification("notifications/initialized", nil)); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	return nil
}

// ListTools calls tools/list and returns the available tool definitions.
// Results are cached; subsequent calls return the cached list.
func (c *Client) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	c.mu.RLock()
	if c.tools != nil {
		defer c.mu.RUnlock()
		return c.tools, nil
	}
	c.mu.RUnlock()

	resp, err := c.send(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tools/list result: %w", err)
	}

	c.mu.Lock()
	c.tools = result.Tools
	c.mu.Unlock()

	c.logger.Info("discovered MCP tools", "count", len(result.Tools))
	return result.Tools, nil
}

// CallTool invokes a tool by name with the given arguments. The result
// is extracted from the response content blocks as a single string.
// Non-text content blocks are described inline (e.g., "[image]").
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}

	resp, err := c.send(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("tools/call %s: %w", name, err)
	}

	var result callToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("unmarshal tools/call result: %w", err)
	}

	text := extractText(result.Content)

	if result.IsError {
		return "", fmt.Errorf("MCP tool %s returned error: %s", name, text)
	}

	return text, nil
}

// Ping checks whether the MCP server is responsive. Used by connwatch
// for health monitoring.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.send(ctx, "ping", nil)
	return err
}

// Close shuts down the client and its transport.
func (c *Client) Close() error {
	c.logger.Info("closing MCP client")
	return c.transport.Close()
}

// send issues a JSON-RPC request and checks for protocol-level errors.
func (c *Client) send(ctx context.Context, method string, params any) (*Response, error) {
	id := c.nextID.Add(1)
	req := NewRequest(id, method, params)

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, resp.Error
	}

	return resp, nil
}

// extractText joins all text content blocks into a single string.
// Non-text blocks are represented as inline markers.
func extractText(blocks []ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		case "resource":
			parts = append(parts, "[resource]")
		default:
			parts = append(parts, fmt.Sprintf("[%s]", b.Type))
		}
	}
	return strings.Join(parts, "\n")
}
