package mcp

import "context"

// Transport is the interface for MCP server communication.
// Implementations handle the details of sending JSON-RPC requests and
// receiving responses over a specific transport (stdio or HTTP).
type Transport interface {
	// Send sends a JSON-RPC request and returns the response.
	// The transport handles framing, encoding, and correlation.
	Send(ctx context.Context, req *Request) (*Response, error)

	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, notif *Notification) error

	// Close shuts down the transport and releases resources.
	// For stdio transports this terminates the subprocess.
	Close() error
}
