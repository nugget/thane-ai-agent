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

// Default reconnect backoff schedule. The supervisor retries forever
// (until its context is cancelled) — Home Assistant is core infrastructure,
// so a transient outage must never leave the socket permanently dead.
const (
	defaultWSBackoffInitial    = 2 * time.Second
	defaultWSBackoffMax        = 60 * time.Second
	defaultWSBackoffMultiplier = 2.0

	// defaultWSHandshakeTimeout bounds the WS opening handshake and the
	// subsequent HA auth exchange. DialContext only covers the dial itself,
	// so without this a connection that is accepted but never completes the
	// auth_required/auth_ok exchange would block Connect — and the whole
	// supervisor — forever.
	defaultWSHandshakeTimeout = 15 * time.Second
)

// WSClient manages a self-healing WebSocket connection to Home Assistant.
//
// Once Start is called, the client owns its own connection lifecycle: it
// dials with exponential backoff, restores its subscriptions on every
// (re)connect, and reconnects automatically whenever the connection drops
// — whether the drop is a network error or a server-initiated close (e.g.
// HA restarting). Reconnection does NOT depend on any external health
// watcher; connwatch's NotifyReachable is only an optional fast-path nudge.
type WSClient struct {
	baseURL string
	token   string
	conn    *websocket.Conn
	connMu  sync.Mutex // guards the conn pointer
	writeMu sync.Mutex // serializes writes; gorilla forbids concurrent writers
	msgID   atomic.Int64

	// connected reflects whether an authenticated connection is live. It
	// is the source of truth for IsConnected and gates the reconnect nudge.
	connected atomic.Bool

	// Response channels keyed by message ID.
	pending   map[int64]chan wsResponse
	pendingMu sync.Mutex

	// Event channel for subscribed events. Created once and reused across
	// reconnects so downstream consumers (e.g. the state watcher) keep a
	// stable channel.
	events chan Event

	// desired is the set of event types we want subscribed. It is sticky:
	// recorded on Subscribe regardless of connection state and re-applied
	// on every (re)connect, so a subscription survives a failed attempt.
	desired   map[string]struct{}
	desiredMu sync.Mutex

	// Supervisor plumbing.
	startOnce sync.Once
	lost      chan struct{} // readLoop signals genuine connection loss
	retryNow  chan struct{} // NotifyReachable shortcuts the backoff wait

	backoffInitial    time.Duration
	backoffMax        time.Duration
	backoffMultiplier float64
	handshakeTimeout  time.Duration

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
		baseURL:           baseURL,
		token:             token,
		pending:           make(map[int64]chan wsResponse),
		events:            make(chan Event, 100),
		desired:           make(map[string]struct{}),
		lost:              make(chan struct{}, 1),
		retryNow:          make(chan struct{}, 1),
		backoffInitial:    defaultWSBackoffInitial,
		backoffMax:        defaultWSBackoffMax,
		backoffMultiplier: defaultWSBackoffMultiplier,
		handshakeTimeout:  defaultWSHandshakeTimeout,
		logger:            logger,
	}
}

// SetReconnectBackoff overrides the reconnect backoff schedule. Intended
// for tests and tuning; call before Start.
func (c *WSClient) SetReconnectBackoff(initial, max time.Duration, multiplier float64) {
	if initial > 0 {
		c.backoffInitial = initial
	}
	if max > 0 {
		c.backoffMax = max
	}
	if multiplier >= 1 {
		c.backoffMultiplier = multiplier
	}
}

// SetHandshakeTimeout overrides how long the WS opening handshake and HA
// auth exchange may take before the attempt is abandoned (and retried by the
// supervisor). Intended for tests and tuning; call before Start.
func (c *WSClient) SetHandshakeTimeout(d time.Duration) {
	if d > 0 {
		c.handshakeTimeout = d
	}
}

// IsConnected reports whether an authenticated connection is currently live.
func (c *WSClient) IsConnected() bool { return c.connected.Load() }

// Start launches the connection supervisor. It is idempotent: only the
// first call starts the goroutine. The supervisor runs until ctx is
// cancelled, at which point it closes the connection and exits.
func (c *WSClient) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		go c.supervise(ctx)
	})
}

// supervise keeps the connection alive: connect (with backoff), wait for
// loss or shutdown, repeat. It is the single owner of the reconnect loop.
func (c *WSClient) supervise(ctx context.Context) {
	for {
		if err := c.connectWithBackoff(ctx); err != nil {
			// Only ctx cancellation ends connectWithBackoff.
			c.Close()
			return
		}
		select {
		case <-ctx.Done():
			c.Close()
			return
		case <-c.lost:
			c.logger.Warn("HA WebSocket connection lost; reconnecting")
		}
	}
}

// connectWithBackoff retries Connect until it succeeds or ctx is cancelled.
// The delay grows geometrically up to backoffMax; a NotifyReachable nudge
// resets it and retries immediately.
func (c *WSClient) connectWithBackoff(ctx context.Context) error {
	delay := c.backoffInitial
	for attempt := 1; ; attempt++ {
		if err := c.Connect(ctx); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			c.logger.Warn("HA WebSocket connect failed; will retry",
				"attempt", attempt, "delay", delay, "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.retryNow:
			delay = c.backoffInitial
		case <-time.After(delay):
			delay = time.Duration(float64(delay) * c.backoffMultiplier)
			if delay > c.backoffMax {
				delay = c.backoffMax
			}
		}
	}
}

// NotifyReachable hints that Home Assistant may be reachable again (e.g.
// from a connwatch OnReady on the REST probe). If the socket is already
// connected it is a no-op; otherwise it shortcuts the supervisor's backoff
// so recovery is immediate rather than waiting out the current delay.
func (c *WSClient) NotifyReachable() {
	if c.connected.Load() {
		return
	}
	select {
	case c.retryNow <- struct{}{}:
	default:
	}
}

// Connect establishes the WebSocket connection and authenticates.
//
// connMu is held only while swapping the conn pointer, not for the full
// duration of the handshake. This avoids deadlocking with sendAndWait
// (called by applyDesiredSubscriptions → sendSubscribe) which also takes
// connMu.
func (c *WSClient) Connect(ctx context.Context) error {
	// Tear down any stale connection and mark the client disconnected for
	// the duration of the (re)connect, so IsConnected reflects reality and
	// NotifyReachable can still nudge a retry while we dial and handshake.
	c.connMu.Lock()
	old := c.conn
	c.conn = nil
	c.connMu.Unlock()
	c.connected.Store(false)
	if old != nil {
		old.Close()
	}

	// Parse base URL and convert to WebSocket URL.
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

	// Use custom dialer with larger buffer for big responses (entity registry can be huge).
	dialer := websocket.Dialer{
		ReadBufferSize:   1024 * 1024, // 1MB
		WriteBufferSize:  64 * 1024,   // 64KB
		HandshakeTimeout: c.handshakeTimeout,
	}

	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}

	// Set read limit for large responses (HA can have 12000+ entities).
	conn.SetReadLimit(100 * 1024 * 1024) // 100MB max message size

	// Bound the auth handshake itself: the dialer's HandshakeTimeout covers
	// only the WS upgrade, so a connection that upgrades but then never
	// sends auth_required/auth_ok would block these reads forever. Cleared
	// right after auth_ok (below), before the long-lived readLoop starts.
	handshakeDeadline := time.Now().Add(c.handshakeTimeout)
	_ = conn.SetReadDeadline(handshakeDeadline)
	_ = conn.SetWriteDeadline(handshakeDeadline)

	// Auth handshake uses the local conn directly — no concurrent readers yet.

	// Read auth_required message.
	var authReq wsMessage
	if err := conn.ReadJSON(&authReq); err != nil {
		conn.Close()
		return fmt.Errorf("read auth_required: %w", err)
	}
	if authReq.Type != "auth_required" {
		conn.Close()
		return fmt.Errorf("expected auth_required, got %s", authReq.Type)
	}

	// Send auth.
	authMsg := map[string]string{
		"type":         "auth",
		"access_token": c.token,
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return fmt.Errorf("send auth: %w", err)
	}

	// Read auth response.
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

	// Clear the handshake deadlines before the long-lived readLoop takes
	// over: an idle but healthy socket must not inherit a fixed deadline,
	// or it would trip it and force a needless reconnect.
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})

	// Publish the authenticated connection so sendAndWait can use it.
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	c.connected.Store(true)

	// Start read loop.
	go c.readLoop(conn)

	// (Re)apply desired subscriptions on the fresh connection.
	c.applyDesiredSubscriptions()

	return nil
}

// Close closes the WebSocket connection. The readLoop goroutine exits
// when the underlying read returns an error from the closed connection.
// Safe to call multiple times. Close marks the client disconnected but
// does not stop the supervisor — cancel the Start context for that.
func (c *WSClient) Close() error {
	c.connected.Store(false)

	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()

	if conn != nil {
		return conn.Close()
	}
	return nil
}

// Events returns the channel for receiving subscribed events.
func (c *WSClient) Events() <-chan Event {
	return c.events
}

// Subscribe records a desired subscription and applies it immediately if
// connected. The intent is sticky: it is re-applied on every (re)connect,
// so a subscription survives a connection that drops or a send that fails.
// When called while disconnected it returns nil — the intent is recorded
// and the supervisor will apply it on the next connect.
func (c *WSClient) Subscribe(ctx context.Context, eventType string) error {
	c.desiredMu.Lock()
	c.desired[eventType] = struct{}{}
	c.desiredMu.Unlock()

	if !c.connected.Load() {
		c.logger.Debug("subscription intent recorded; will apply on connect",
			"event_type", eventType)
		return nil
	}
	return c.sendSubscribe(ctx, eventType)
}

// sendSubscribe sends a single subscribe_events request and waits for the
// ack. It does not touch the desired set.
func (c *WSClient) sendSubscribe(ctx context.Context, eventType string) error {
	id := c.msgID.Add(1)
	msg := map[string]any{
		"id":         id,
		"type":       "subscribe_events",
		"event_type": eventType,
	}
	if _, err := c.sendAndWait(ctx, id, msg); err != nil {
		return fmt.Errorf("subscribe to %s: %w", eventType, err)
	}
	c.logger.Info("subscribed to events", "event_type", eventType)
	return nil
}

// applyDesiredSubscriptions re-subscribes to every desired event type on a
// freshly established connection. Uses a fresh context so it is not bound
// to a dial context that may already be cancelled.
func (c *WSClient) applyDesiredSubscriptions() {
	c.desiredMu.Lock()
	subs := make([]string, 0, len(c.desired))
	for eventType := range c.desired {
		subs = append(subs, eventType)
	}
	c.desiredMu.Unlock()

	if len(subs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, eventType := range subs {
		if err := c.sendSubscribe(ctx, eventType); err != nil {
			c.logger.Error("failed to apply subscription",
				"event_type", eventType, "error", err)
		}
	}
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

// GetEntityRegistryWS retrieves the entity registry via WebSocket.
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
	// Create response channel.
	respCh := make(chan wsResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Send message. Writes are serialized through writeMu: gorilla forbids
	// concurrent writers, and applyDesiredSubscriptions, live Subscribe
	// calls, and request methods can all send concurrently.
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("send message: connection closed")
	}
	c.writeMu.Lock()
	err := conn.WriteJSON(msg)
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	// Wait for response.
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

// readLoop continuously reads messages from the given connection. It owns
// exactly one connection; when that connection ends it returns, and (for a
// genuine loss) signals the supervisor to reconnect.
func (c *WSClient) readLoop(conn *websocket.Conn) {
	defer c.logger.Debug("readLoop exited")

	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			// Distinguish an intentional close/replacement (Close() nils
			// c.conn, or Connect() swapped in a new one) from a genuine
			// loss. Only a genuine loss of the *current* connection should
			// trigger a reconnect.
			c.connMu.Lock()
			current := c.conn
			c.connMu.Unlock()
			if current == nil || current != conn {
				c.logger.Info("HA WebSocket connection closed")
				return
			}

			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.logger.Info("HA WebSocket closed by server; will reconnect", "error", err)
			} else {
				c.logger.Error("HA WebSocket read error; connection lost", "error", err)
			}
			c.connected.Store(false)
			c.signalLost()
			return
		}

		switch msg.Type {
		case "result":
			// Response to a request.
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
			// Subscribed event.
			if msg.Event != nil {
				select {
				case c.events <- *msg.Event:
				default:
					c.logger.Warn("event channel full, dropping event", "type", msg.Event.Type)
				}
			}

		case "pong":
			// Ping/pong keepalive, ignore.

		default:
			c.logger.Debug("unhandled WebSocket message type", "type", msg.Type)
		}
	}
}

// signalLost notifies the supervisor of a connection loss without blocking.
// The buffered channel coalesces multiple signals into one reconnect.
func (c *WSClient) signalLost() {
	select {
	case c.lost <- struct{}{}:
	default:
	}
}
