package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// HTTPConfig configures an HTTP MCP transport that communicates with a
// remote MCP server over streamable HTTP (JSON-RPC over POST).
type HTTPConfig struct {
	// URL is the MCP server endpoint.
	URL string

	// Headers are additional HTTP headers sent with every request
	// (e.g., Authorization).
	Headers map[string]string

	// Logger is the structured logger for transport diagnostics.
	Logger *slog.Logger
}

// HTTPTransport communicates with an MCP server over streamable HTTP.
// Each JSON-RPC request is sent as an HTTP POST; the response comes
// back in the response body.
type HTTPTransport struct {
	url        string
	headers    map[string]string
	httpClient *http.Client
	logger     *slog.Logger

	mu        sync.RWMutex
	sessionID string // Mcp-Session header for session affinity
}

// NewHTTPTransport creates an HTTP transport for the given config.
// The underlying HTTP client is constructed via httpkit.
func NewHTTPTransport(cfg HTTPConfig) *HTTPTransport {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	client := httpkit.NewClient(
		httpkit.WithLogger(logger),
	)

	return &HTTPTransport{
		url:        cfg.URL,
		headers:    cfg.Headers,
		httpClient: client,
		logger:     logger,
	}
}

// Send sends a JSON-RPC request via HTTP POST and returns the response.
func (t *HTTPTransport) Send(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// Apply configured headers (auth, etc.).
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	// Include session ID if we have one from a previous response.
	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session", t.sessionID)
	}
	t.mu.RUnlock()

	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request to %s: %w", t.url, err)
	}
	defer httpkit.DrainAndClose(httpResp.Body, 1<<20)

	// Capture session ID from response.
	if sid := httpResp.Header.Get("Mcp-Session"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	if httpResp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(httpResp.Body, 1<<20)
		return nil, fmt.Errorf("MCP server returned %d: %s", httpResp.StatusCode, errBody)
	}

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 10<<20)) // 10 MiB limit
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// Notify sends a JSON-RPC notification via HTTP POST. No response
// content is expected, but the HTTP response status is checked.
func (t *HTTPTransport) Notify(ctx context.Context, notif *Notification) error {
	body, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}

	t.mu.RLock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session", t.sessionID)
	}
	t.mu.RUnlock()

	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("HTTP notification to %s: %w", t.url, err)
	}
	defer httpkit.DrainAndClose(httpResp.Body, 1<<20)

	// Accept 200 and 202 (accepted) for notifications.
	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusAccepted {
		errBody := httpkit.ReadErrorBody(httpResp.Body, 1<<20)
		return fmt.Errorf("MCP server returned %d for notification: %s", httpResp.StatusCode, errBody)
	}

	return nil
}

// Close is a no-op for HTTP transports. The underlying HTTP client
// manages its own connection pool via httpkit.
func (t *HTTPTransport) Close() error {
	return nil
}
