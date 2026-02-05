// Package homeassistant provides a client for the Home Assistant API.
package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a Home Assistant REST API client.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new Home Assistant client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// State represents an entity state from Home Assistant.
type State struct {
	EntityID    string                 `json:"entity_id"`
	State       string                 `json:"state"`
	Attributes  map[string]any         `json:"attributes"`
	LastChanged time.Time              `json:"last_changed"`
	LastUpdated time.Time              `json:"last_updated"`
}

// APIStatus represents the HA API status response.
type APIStatus struct {
	Message string `json:"message"`
}

// Config represents basic HA configuration.
type Config struct {
	LocationName string  `json:"location_name"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	Elevation    int     `json:"elevation"`
	UnitSystem   struct {
		Length      string `json:"length"`
		Mass        string `json:"mass"`
		Temperature string `json:"temperature"`
		Volume      string `json:"volume"`
	} `json:"unit_system"`
	TimeZone string `json:"time_zone"`
	Version  string `json:"version"`
}

// Ping checks if the API is reachable.
func (c *Client) Ping(ctx context.Context) error {
	var status APIStatus
	if err := c.get(ctx, "/api/", &status); err != nil {
		return err
	}
	if status.Message != "API running." {
		return fmt.Errorf("unexpected API status: %s", status.Message)
	}
	return nil
}

// GetConfig retrieves the Home Assistant configuration.
func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := c.get(ctx, "/api/config", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetStates retrieves all entity states.
func (c *Client) GetStates(ctx context.Context) ([]State, error) {
	var states []State
	if err := c.get(ctx, "/api/states", &states); err != nil {
		return nil, err
	}
	return states, nil
}

// GetState retrieves a single entity state.
func (c *Client) GetState(ctx context.Context, entityID string) (*State, error) {
	var state State
	if err := c.get(ctx, "/api/states/"+entityID, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// CallService calls a Home Assistant service.
func (c *Client) CallService(ctx context.Context, domain, service string, data map[string]any) error {
	path := fmt.Sprintf("/api/services/%s/%s", domain, service)
	return c.post(ctx, path, data, nil)
}

// get performs a GET request to the HA API.
func (c *Client) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// post performs a POST request to the HA API.
func (c *Client) post(ctx context.Context, path string, data any, result any) error {
	var body io.Reader
	if data != nil {
		jsonData, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal data: %w", err)
		}
		body = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}
