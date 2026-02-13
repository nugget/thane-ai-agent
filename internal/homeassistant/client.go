// Package homeassistant provides a client for the Home Assistant API.
package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// Client is a Home Assistant REST API client.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	watcher    readyChecker // set via SetWatcher for health status
}

// readyChecker is satisfied by connwatch.Watcher. Defined here to avoid
// importing connwatch directly, keeping the dependency one-directional.
type readyChecker interface {
	IsReady() bool
}

// SetWatcher sets the connection watcher for health status queries.
func (c *Client) SetWatcher(w readyChecker) {
	c.watcher = w
}

// IsReady reports whether Home Assistant is currently reachable.
// Returns true if no watcher is configured (backward compatible).
func (c *Client) IsReady() bool {
	if c.watcher == nil {
		return true
	}
	return c.watcher.IsReady()
}

// NewClient creates a new Home Assistant client.
//
// Issue #53: Go net.Dial intermittently fails on macOS with "no route to host"
// for LAN targets due to ARP table race conditions. Retry with a short delay
// allows the ARP entry to refresh before the second attempt.
func NewClient(baseURL, token string, logger *slog.Logger) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(30*time.Second),
			httpkit.WithRetry(3, 2*time.Second),
			httpkit.WithLogger(logger),
		),
	}
}

// State represents an entity state from Home Assistant.
type State struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
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

// Area represents a Home Assistant area.
type Area struct {
	AreaID  string   `json:"area_id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

// GetAreas retrieves all areas from the area registry.
func (c *Client) GetAreas(ctx context.Context) ([]Area, error) {
	var areas []Area
	if err := c.get(ctx, "/api/config/area_registry/list", &areas); err != nil {
		return nil, err
	}
	return areas, nil
}

// EntityRegistryEntry represents an entity from the registry with area info.
type EntityRegistryEntry struct {
	EntityID     string `json:"entity_id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	AreaID       string `json:"area_id"`
	DeviceID     string `json:"device_id"`
	Platform     string `json:"platform"`
	DisabledBy   string `json:"disabled_by"`
}

// IsDisabled reports whether the entity is disabled in Home Assistant.
func (e EntityRegistryEntry) IsDisabled() bool {
	return e.DisabledBy != ""
}

// GetEntityRegistry retrieves the entity registry.
func (c *Client) GetEntityRegistry(ctx context.Context) ([]EntityRegistryEntry, error) {
	var entries []EntityRegistryEntry
	if err := c.get(ctx, "/api/config/entity_registry/list", &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// EntityInfo combines state and registry info for an entity.
type EntityInfo struct {
	EntityID     string
	FriendlyName string
	AreaID       string
	Domain       string
	State        string
}

// GetEntities retrieves entities, optionally filtered by domain.
func (c *Client) GetEntities(ctx context.Context, domain string) ([]EntityInfo, error) {
	states, err := c.GetStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("get states: %w", err)
	}

	var entities []EntityInfo
	for _, s := range states {
		parts := splitEntityID(s.EntityID)
		if len(parts) != 2 {
			continue
		}
		entityDomain := parts[0]

		if domain != "" && entityDomain != domain {
			continue
		}

		friendlyName := ""
		if fn, ok := s.Attributes["friendly_name"].(string); ok {
			friendlyName = fn
		}

		entities = append(entities, EntityInfo{
			EntityID:     s.EntityID,
			FriendlyName: friendlyName,
			Domain:       entityDomain,
			State:        s.State,
		})
	}

	return entities, nil
}

func splitEntityID(entityID string) []string {
	for i, c := range entityID {
		if c == '.' {
			return []string{entityID[:i], entityID[i+1:]}
		}
	}
	return nil
}

// get performs a GET request to the HA API.
func (c *Client) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	// Drain and close to ensure connection reuse even when result is nil.
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode != http.StatusOK {
		body := httpkit.ReadErrorBody(resp.Body, 512)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, body)
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
	var reqBody []byte
	if data != nil {
		var err error
		reqBody, err = json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal data: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	// Drain and close to ensure connection reuse even when result is nil.
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode != http.StatusOK {
		body := httpkit.ReadErrorBody(resp.Body, 512)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, body)
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}
