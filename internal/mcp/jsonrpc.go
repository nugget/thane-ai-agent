package mcp

import (
	"encoding/json"
	"fmt"
)

// jsonrpcVersion is the JSON-RPC protocol version used by MCP.
const jsonrpcVersion = "2.0"

// Request is a JSON-RPC 2.0 request message.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// NewRequest creates a JSON-RPC 2.0 request with the given method and params.
func NewRequest(id int64, method string, params any) *Request {
	return &Request{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Method:  method,
		Params:  params,
	}
}

// Response is a JSON-RPC 2.0 response message. Exactly one of Result
// or Error is non-nil in a well-formed response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface for RPCError.
func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Notification is a JSON-RPC 2.0 notification (no ID, no response expected).
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// NewNotification creates a JSON-RPC 2.0 notification.
func NewNotification(method string, params any) *Notification {
	return &Notification{
		JSONRPC: jsonrpcVersion,
		Method:  method,
		Params:  params,
	}
}
