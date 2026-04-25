package companion

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// pingInterval is how often the server sends a ping to each provider.
	pingInterval = 30 * time.Second

	// pongWait is the maximum time to wait for a pong (or any message)
	// before considering the connection dead. 1.5x the ping interval.
	pongWait = 45 * time.Second

	// writeWait is the maximum time to wait for a write to complete.
	writeWait = 10 * time.Second

	// authTimeout is the maximum time allowed for the auth handshake.
	authTimeout = 10 * time.Second

	// maxMessageSize is the maximum size of a single WebSocket message
	// frame. Limits memory usage from oversized client payloads.
	maxMessageSize = 64 * 1024 // 64 KiB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// CheckOrigin allows all origins because companion apps are native
	// desktop apps, not browsers. Authentication is token-based. If browser
	// clients are added in the future, this should validate Origin against
	// an allowlist.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Handler is the HTTP handler for companion app WebSocket connections.
type Handler struct {
	tokenIndex map[string]string // token → account name
	registry   *Registry
	logger     *slog.Logger
}

// NewHandler creates a new companion WebSocket handler. The tokenIndex
// maps authentication tokens to account names (built by
// config.CompanionConfig.TokenIndex). If registry is nil, a default
// Registry is created.
func NewHandler(tokenIndex map[string]string, registry *Registry, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if registry == nil {
		registry = NewRegistry(logger)
	}
	return &Handler{
		tokenIndex: tokenIndex,
		registry:   registry,
		logger:     logger,
	}
}

// ServeHTTP upgrades the connection to WebSocket, performs the auth
// handshake, and runs the heartbeat and read loops for the provider.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("companion websocket upgrade failed", "error", err)
		return
	}
	conn.SetReadLimit(maxMessageSize)

	provider, err := h.authenticate(conn, defaultRequestType(r.URL.Path))
	if err != nil {
		h.logger.Warn("companion auth failed",
			"error", err,
			"remote_addr", r.RemoteAddr,
		)
		conn.Close()
		return
	}

	// Register the provider before confirming to the client, so the
	// registry is consistent by the time the client reads auth_ok.
	h.registry.Add(provider)
	defer func() {
		h.registry.Remove(provider.ID)
		close(provider.done)
		conn.Close()
	}()

	if err := writeJSONWithDeadline(conn, writeWait, authOK{
		Type:       typeAuthOK,
		ProviderID: provider.ID,
		Account:    provider.Account,
	}); err != nil {
		return
	}

	// Start heartbeat goroutine.
	go h.heartbeat(provider)

	// Run the read loop (blocks until connection closes).
	h.readLoop(provider)
}

// writeJSONWithDeadline sets a write deadline and sends msg as JSON.
func writeJSONWithDeadline(conn *websocket.Conn, deadline time.Duration, msg any) error {
	if err := conn.SetWriteDeadline(time.Now().Add(deadline)); err != nil {
		return err
	}
	return conn.WriteJSON(msg)
}

// authenticate performs the auth handshake on a newly upgraded connection.
func (h *Handler) authenticate(conn *websocket.Conn, requestType string) (*Provider, error) {
	// Step 1: Send auth_required.
	if err := writeJSONWithDeadline(conn, writeWait, authRequired{
		Type:    typeAuthRequired,
		Version: protocolVersion,
	}); err != nil {
		return nil, err
	}

	// Step 2: Read auth message.
	if err := conn.SetReadDeadline(time.Now().Add(authTimeout)); err != nil {
		return nil, err
	}
	var msg authMessage
	if err := conn.ReadJSON(&msg); err != nil {
		return nil, err
	}

	if msg.Type != typeAuth {
		// Best-effort error response — ignore write errors.
		_ = writeJSONWithDeadline(conn, writeWait, authFailed{
			Type:    typeAuthFailed,
			Message: "expected auth message",
		})
		return nil, &authError{"expected auth message, got " + msg.Type}
	}

	// Step 3: Validate token and resolve account.
	account, ok := h.tokenIndex[msg.Token]
	if !ok {
		_ = writeJSONWithDeadline(conn, writeWait, authFailed{
			Type:    typeAuthFailed,
			Message: "invalid token",
		})
		return nil, &authError{"invalid token"}
	}

	switch msg.Protocol {
	case "companion":
		requestType = typeCompanionReq
	case "platform":
		requestType = typeLegacyPlatformReq
	}

	// Build the provider — auth_ok is sent by ServeHTTP after
	// the provider is registered, ensuring registry consistency.
	providerID := generateProviderID()
	return &Provider{
		ID:          providerID,
		Account:     account,
		ClientName:  msg.ClientName,
		ClientID:    msg.ClientID,
		Conn:        conn,
		ConnectedAt: time.Now(),
		requestType: requestType,
		done:        make(chan struct{}),
	}, nil
}

func defaultRequestType(path string) string {
	if path == "/v1/platform/ws" {
		return typeLegacyPlatformReq
	}
	return typeCompanionReq
}

// heartbeat sends periodic ping messages to the provider.
func (h *Handler) heartbeat(p *Provider) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := p.writeJSON(ping{Type: typePing}); err != nil {
				h.logger.Debug("companion ping failed",
					"provider_id", p.ID,
					"error", err,
				)
				return
			}
		case <-p.done:
			return
		}
	}
}

// readLoop reads messages from the provider until the connection closes.
// Any incoming message resets the read deadline, so pongs (and future
// message types) keep the connection alive.
func (h *Handler) readLoop(p *Provider) {
	if err := p.Conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		return
	}

	for {
		_, payload, err := p.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
			) {
				h.logger.Warn("companion app connection lost",
					"provider_id", p.ID,
					"error", err,
				)
			} else {
				h.logger.Debug("companion app disconnected",
					"provider_id", p.ID,
				)
			}
			return
		}

		var envelope struct {
			ID   int64  `json:"id,omitempty"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			h.logger.Warn("companion message decode failed",
				"provider_id", p.ID,
				"error", err,
			)
			continue
		}

		// Any valid message resets the read deadline.
		if err := p.Conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			return
		}

		switch envelope.Type {
		case typePong:
			// Heartbeat response — deadline already reset above.
		case typeRegisterCaps:
			h.handleRegisterCapabilities(p, envelope.ID, payload)
		case typeResult:
			h.handleResult(p, payload)
		default:
			h.logger.Debug("companion message received (unhandled)",
				"provider_id", p.ID,
				"type", envelope.Type,
			)
		}
	}
}

func (h *Handler) handleRegisterCapabilities(p *Provider, id int64, payload []byte) {
	var msg registerCapabilitiesMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		h.logger.Warn("companion capabilities decode failed",
			"provider_id", p.ID,
			"error", err,
		)
		h.writeErrorResult(p, id, "invalid_payload", "failed to decode capability registration")
		return
	}
	if id == 0 {
		h.logger.Warn("companion capabilities missing correlation id",
			"provider_id", p.ID,
		)
		return
	}
	if msg.ID != 0 && msg.ID != id {
		h.logger.Warn("companion capabilities id mismatch",
			"provider_id", p.ID,
			"envelope_id", id,
			"message_id", msg.ID,
		)
		h.writeErrorResult(p, id, "invalid_payload", "capability registration id mismatch")
		return
	}

	if err := h.registry.RegisterCapabilities(p.ID, msg.Capabilities); err != nil {
		h.logger.Warn("companion capability registration failed",
			"provider_id", p.ID,
			"error", err,
		)
		h.writeErrorResult(p, id, "provider_not_found", err.Error())
		return
	}

	if err := p.writeJSON(Message{
		ID:      id,
		Type:    typeResult,
		Success: true,
	}); err != nil {
		h.logger.Debug("companion capability ack failed",
			"provider_id", p.ID,
			"error", err,
		)
		return
	}

	h.logger.Info("companion capabilities registered",
		"provider_id", p.ID,
		"account", p.Account,
		"count", len(msg.Capabilities),
	)
}

func (h *Handler) handleResult(p *Provider, payload []byte) {
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		h.logger.Warn("companion result decode failed",
			"provider_id", p.ID,
			"error", err,
		)
		return
	}

	if msg.ID == 0 {
		h.logger.Debug("companion result missing id",
			"provider_id", p.ID,
		)
		return
	}

	if !h.registry.ResolveResult(p.ID, msg) {
		h.logger.Debug("companion result had no pending waiter",
			"provider_id", p.ID,
			"id", msg.ID,
		)
	}
}

func (h *Handler) writeErrorResult(p *Provider, id int64, code, message string) {
	if id == 0 {
		return
	}
	if err := p.writeJSON(Message{
		ID:   id,
		Type: typeResult,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}); err != nil {
		h.logger.Debug("companion error result write failed",
			"provider_id", p.ID,
			"error", err,
		)
	}
}

// authError is a sentinel error type for authentication failures.
type authError struct {
	reason string
}

func (e *authError) Error() string {
	return "companion auth: " + e.reason
}
