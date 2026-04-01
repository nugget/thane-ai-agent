// Package platform implements the WebSocket endpoint for native platform
// provider connections (e.g. macOS app). Providers connect inward and
// register capabilities that the server can invoke via platform requests.
package platform

import "encoding/json"

// Message is the generic WebSocket message envelope. All messages between
// server and client use this structure, with the Type field determining
// the semantics.
type Message struct {
	ID      int64           `json:"id,omitempty"`
	Type    string          `json:"type"`
	Success bool            `json:"success,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error carries structured error information in message responses.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// authRequired is the first message the server sends after WebSocket upgrade.
type authRequired struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// authMessage is the client's response to authRequired.
type authMessage struct {
	Type       string `json:"type"`
	Token      string `json:"token"`
	ClientName string `json:"client_name"`
	ClientID   string `json:"client_id"`
}

// authOK confirms successful authentication, assigns a provider ID, and
// echoes back the server-resolved account name.
type authOK struct {
	Type       string `json:"type"`
	ProviderID string `json:"provider_id"`
	Account    string `json:"account"`
}

// authFailed is sent when the client provides invalid credentials.
type authFailed struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ping is the server-initiated heartbeat message.
type ping struct {
	Type string `json:"type"`
}

// Message type constants.
const (
	typeAuthRequired = "auth_required"
	typeAuth         = "auth"
	typeAuthOK       = "auth_ok"
	typeAuthFailed   = "auth_failed"
	typePing         = "ping"
	typePong         = "pong"
)

// protocolVersion is the current platform provider protocol version.
const protocolVersion = "0.1.0"
