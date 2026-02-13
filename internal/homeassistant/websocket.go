// Package homeassistant provides clients for the Home Assistant API.
package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// WSClient manages a WebSocket connection to Home Assistant.
type WSClient struct {
	baseURL string
	token   string
	conn    *websocket.Conn
	connMu  sync.Mutex
	msgID   atomic.Int64

	// Response channels keyed by message ID
	pending   map[int64]chan wsResponse
	pendingMu sync.Mutex

	// Event channel for subscribed events
	events chan Event

	// Subscriptions to restore on reconnect
	subscriptions   []string
	subscriptionsMu sync.Mutex

	logger *slog.Logger
}

// Event represents a Home Assistant event received via WebSocket.
type Event struct {
	Type      string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
	Origin    string          `json:"origin"`
	TimeFired time.Time       `json:"time_fired"`
}

// StateChangedData represents the data payload for state_changed events.
type StateChangedData struct {
	EntityID string `json:"entity_id"`
	OldState *State `json:"old_state"`
	NewState *State `json:"new_state"`
}

// wsMessage is the generic WebSocket message format.
type wsMessage struct {
	ID      int64           `json:"id,omitempty"`
	Type    string          `json:"type"`
	Success bool            `json:"success,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Event   *Event          `json:"event,omitempty"`
	Error   *wsError        `json:"error,omitempty"`
}

type wsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wsResponse wraps the result with success/error info for the response channel.
type wsResponse struct {
	Success bool
	Result  json.RawMessage
	Error   *wsError
}

// NewWSClient creates a new WebSocket client for Home Assistant.
func NewWSClient(baseURL, token string, logger *slog.Logger) *WSClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &WSClient{
		baseURL:       baseURL,
		token:         token,
		pending:       make(map[int64]chan wsResponse),
		events:        make(chan Event, 100),
		subscriptions: make([]string, 0),
		logger:        logger,
	}
}

// Connect establishes the WebSocket connection and authenticates.
func (c *WSClient) Connect(ctx context.Context) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	// Parse base URL and convert to WebSocket URL
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/api/websocket"

	c.logger.Info("connecting to Home Assistant WebSocket", "url", u.String())

	// Use custom dialer with larger buffer for big responses (entity registry can be huge)
	dialer := websocket.Dialer{
		ReadBufferSize:  1024 * 1024, // 1MB
		WriteBufferSize: 64 * 1024,   // 64KB
	}

	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}

	// Set read limit for large responses (HA can have 12000+ entities)
	conn.SetReadLimit(100 * 1024 * 1024) // 100MB max message size

	c.conn = conn

	// Read auth_required message
	var authReq wsMessage
	if err := conn.ReadJSON(&authReq); err != nil {
		conn.Close()
		return fmt.Errorf("read auth_required: %w", err)
	}
	if authReq.Type != "auth_required" {
		conn.Close()
		return fmt.Errorf("expected auth_required, got %s", authReq.Type)
	}

	// Send auth
	authMsg := map[string]string{
		"type":         "auth",
		"access_token": c.token,
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return fmt.Errorf("send auth: %w", err)
	}

	// Read auth response
	var authResp wsMessage
	if err := conn.ReadJSON(&authResp); err != nil {
		conn.Close()
		return fmt.Errorf("read auth response: %w", err)
	}

	if authResp.Type == "auth_invalid" {
		conn.Close()
		return fmt.Errorf("authentication failed")
	}
	if authResp.Type != "auth_ok" {
		conn.Close()
		return fmt.Errorf("unexpected auth response: %s", authResp.Type)
	}

	c.logger.Info("WebSocket authenticated")

	// Start read loop
	go c.readLoop()

	// Restore subscriptions
	c.restoreSubscriptions()

	return nil
}

// Close closes the WebSocket connection.
func (c *WSClient) Close() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Reconnect closes the existing connection (if any) and re-establishes
// the WebSocket, authenticating and restoring all prior subscriptions.
// Safe to call from any goroutine. Intended to be called from a
// connwatch OnReady callback when Home Assistant becomes reachable again.
func (c *WSClient) Reconnect(ctx context.Context) error {
	c.logger.Info("reconnecting WebSocket")

	// Close the old connection. Ignore errors — it may already be dead.
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()

	// Connect handles auth, starts readLoop, and calls restoreSubscriptions.
	return c.Connect(ctx)
}

// Events returns the channel for receiving subscribed events.
func (c *WSClient) Events() <-chan Event {
	return c.events
}

// Subscribe subscribes to a Home Assistant event type.
func (c *WSClient) Subscribe(ctx context.Context, eventType string) error {
	id := c.msgID.Add(1)

	msg := map[string]any{
		"id":         id,
		"type":       "subscribe_events",
		"event_type": eventType,
	}

	_, err := c.sendAndWait(ctx, id, msg)
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", eventType, err)
	}

	// If we got here without error, subscription succeeded
	// (errors are returned by sendAndWait if the response indicates failure)

	// Track subscription for reconnect
	c.subscriptionsMu.Lock()
	c.subscriptions = append(c.subscriptions, eventType)
	c.subscriptionsMu.Unlock()

	c.logger.Info("subscribed to events", "event_type", eventType)
	return nil
}

// GetAreaRegistry retrieves the area registry.
func (c *WSClient) GetAreaRegistry(ctx context.Context) ([]Area, error) {
	id := c.msgID.Add(1)
	msg := map[string]any{
		"id":   id,
		"type": "config/area_registry/list",
	}

	resp, err := c.sendAndWait(ctx, id, msg)
	if err != nil {
		return nil, fmt.Errorf("get area registry: %w", err)
	}

	var areas []Area
	if err := json.Unmarshal(resp, &areas); err != nil {
		return nil, fmt.Errorf("unmarshal areas: %w", err)
	}

	return areas, nil
}

// GetEntityRegistry retrieves the entity registry via WebSocket.
func (c *WSClient) GetEntityRegistryWS(ctx context.Context) ([]EntityRegistryEntry, error) {
	id := c.msgID.Add(1)
	msg := map[string]any{
		"id":   id,
		"type": "config/entity_registry/list",
	}

	resp, err := c.sendAndWait(ctx, id, msg)
	if err != nil {
		return nil, fmt.Errorf("get entity registry: %w", err)
	}

	var entries []EntityRegistryEntry
	if err := json.Unmarshal(resp, &entries); err != nil {
		return nil, fmt.Errorf("unmarshal entities: %w", err)
	}

	return entries, nil
}

// sendAndWait sends a message and waits for the response.
func (c *WSClient) sendAndWait(ctx context.Context, id int64, msg any) (json.RawMessage, error) {
	// Create response channel
	respCh := make(chan wsResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Send message
	c.connMu.Lock()
	err := c.conn.WriteJSON(msg)
	c.connMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	// Wait for response
	select {
	case resp := <-respCh:
		if !resp.Success {
			if resp.Error != nil {
				return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}
			return nil, fmt.Errorf("request failed")
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response")
	}
}

// readLoop continuously reads messages from the WebSocket.
func (c *WSClient) readLoop() {
	for {
		var msg wsMessage

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			return
		}

		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.logger.Info("WebSocket closed normally")
				return
			}
			c.logger.Error("WebSocket read error, connection lost", "error", err)
			// Reconnection is handled by connwatch: when the HA service
			// becomes reachable again, the OnReady callback calls Reconnect().
			return
		}

		switch msg.Type {
		case "result":
			// Response to a request
			c.pendingMu.Lock()
			if ch, ok := c.pending[msg.ID]; ok {
				ch <- wsResponse{
					Success: msg.Success,
					Result:  msg.Result,
					Error:   msg.Error,
				}
			}
			c.pendingMu.Unlock()

		case "event":
			// Subscribed event
			if msg.Event != nil {
				select {
				case c.events <- *msg.Event:
				default:
					c.logger.Warn("event channel full, dropping event", "type", msg.Event.Type)
				}
			}

		case "pong":
			// Ping/pong keepalive, ignore

		default:
			c.logger.Debug("unhandled WebSocket message type", "type", msg.Type)
		}
	}
}

// restoreSubscriptions re-subscribes to all tracked event types.
// It clears the subscription list first because Subscribe() appends to it;
// without clearing, each reconnect would duplicate every entry.
func (c *WSClient) restoreSubscriptions() {
	c.subscriptionsMu.Lock()
	subs := make([]string, len(c.subscriptions))
	copy(subs, c.subscriptions)
	c.subscriptions = c.subscriptions[:0] // clear to prevent duplicates
	c.subscriptionsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, eventType := range subs {
		if err := c.Subscribe(ctx, eventType); err != nil {
			c.logger.Error("failed to restore subscription", "event_type", eventType, "error", err)
		}
	}
}
