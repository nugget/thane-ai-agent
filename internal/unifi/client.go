package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// ClientStation represents a wireless client from the UniFi controller
// API. Only fields relevant to room presence detection are included.
type ClientStation struct {
	MAC            string `json:"mac"`
	Hostname       string `json:"hostname"`
	LastUplinkName string `json:"last_uplink_name"` // AP name
	Signal         int    `json:"signal"`           // RSSI in dBm
	LastSeen       int64  `json:"last_seen"`        // Unix timestamp
}

// Client is a UniFi Network controller API client. It implements the
// DeviceLocator interface for room-level presence detection.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewClient creates a UniFi API client. The URL should include the
// scheme and host (e.g., "https://192.168.1.1"). Authentication uses
// the X-API-KEY header. TLS verification is disabled because UniFi
// controllers typically use self-signed certificates.
func NewClient(baseURL, apiKey string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(15*time.Second),
			httpkit.WithRetry(2, 2*time.Second),
			httpkit.WithTLSInsecureSkipVerify(),
			httpkit.WithLogger(logger),
		),
		logger: logger,
	}
}

// GetClientStations retrieves all wireless client station data from
// the default site. Returns a slice of client stations with their MAC,
// AP association, signal strength, and last seen time.
func (c *Client) GetClientStations(ctx context.Context) ([]ClientStation, error) {
	const path = "/proxy/network/api/s/default/stat/sta"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode != http.StatusOK {
		body := httpkit.ReadErrorBody(resp.Body, 512)
		return nil, fmt.Errorf("UniFi API error %d: %s", resp.StatusCode, body)
	}

	var envelope struct {
		Data []ClientStation `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return envelope.Data, nil
}

// Ping checks if the UniFi controller is reachable by requesting the
// site health endpoint. Used by connwatch for health monitoring.
func (c *Client) Ping(ctx context.Context) error {
	const path = "/proxy/network/api/s/default/stat/health"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-API-KEY", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("UniFi API status %d", resp.StatusCode)
	}
	return nil
}

// LocateDevices implements DeviceLocator by querying the UniFi station
// list and converting to DeviceLocation structs.
func (c *Client) LocateDevices(ctx context.Context) ([]DeviceLocation, error) {
	stations, err := c.GetClientStations(ctx)
	if err != nil {
		return nil, err
	}

	locations := make([]DeviceLocation, len(stations))
	for i, s := range stations {
		locations[i] = DeviceLocation{
			MAC:      s.MAC,
			APName:   s.LastUplinkName,
			Signal:   s.Signal,
			LastSeen: s.LastSeen,
		}
	}
	return locations, nil
}
