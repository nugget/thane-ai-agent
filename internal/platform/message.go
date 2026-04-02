// Package platform implements the WebSocket endpoint for native platform
// provider connections (e.g. macOS app). Providers connect inward and
// register capabilities that the server can invoke via platform requests.
package platform

import "encoding/json"

// Message is the generic WebSocket message envelope used for post-auth
// communication (pong, future request/response). The auth handshake uses
// dedicated structs (authRequired, authMessage, authOK, authFailed, ping).
type Message struct {
	ID      int64           `json:"id,omitempty"`
	Type    string          `json:"type"`
	Success bool            `json:"success,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Capability describes a platform capability exposed by a connected
// provider, along with the methods it supports.
type Capability struct {
	Name    string   `json:"name"`
	Version string   `json:"version,omitempty"`
	Methods []string `json:"methods,omitempty"`
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

// registerCapabilitiesMessage is sent by the client after auth to
// declare the platform capabilities it can service.
type registerCapabilitiesMessage struct {
	ID           int64        `json:"id"`
	Type         string       `json:"type"`
	Capabilities []Capability `json:"capabilities"`
}

// platformRequestMessage is sent by the server to invoke a registered
// capability method on the provider.
type platformRequestMessage struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	Capability string          `json:"capability"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params,omitempty"`
}

// Message type constants.
const (
	typeAuthRequired = "auth_required"
	typeAuth         = "auth"
	typeAuthOK       = "auth_ok"
	typeAuthFailed   = "auth_failed"
	typePing         = "ping"
	typePong         = "pong"
	typeRegisterCaps = "register_capabilities"
	typePlatformReq  = "platform_request"
	typeResult       = "result"
)

// protocolVersion is the current platform provider protocol version.
const protocolVersion = "0.1.0"
