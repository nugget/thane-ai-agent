// Package homeassistant provides a client for the Home Assistant API.
package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
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

// IsDisabled returns true if the entity is disabled.
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

// get performs a GET request to the HA API using curl.
// We use curl as a workaround for an intermittent Go net.Dial issue on macOS
// where TCP connections to LAN hosts fail with "no route to host" despite
// the network being reachable. curl from the same process works fine.
func (c *Client) get(_ context.Context, path string, result any) error {
	body, err := c.curl("GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// post performs a POST request to the HA API using curl.
func (c *Client) post(_ context.Context, path string, data any, result any) error {
	var reqBody []byte
	if data != nil {
		var err error
		reqBody, err = json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal data: %w", err)
		}
	}

	body, err := c.curl("POST", c.baseURL+path, reqBody)
	if err != nil {
		return err
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// curl executes an HTTP request via the curl command.
func (c *Client) curl(method, url string, body []byte) ([]byte, error) {
	args := []string{
		"-s", "--max-time", "30",
		"-w", "\n%{http_code}",
		"-X", method,
		"-H", "Authorization: Bearer " + c.token,
		"-H", "Content-Type: application/json",
	}
	if body != nil {
		args = append(args, "-d", string(body))
	}
	args = append(args, url)

	cmd := exec.Command("curl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("curl %s %s failed: %w (stderr: %s)", method, url, err, stderr.String())
	}

	// Parse status code from the last line (added via -w)
	out := stdout.Bytes()
	lastNewline := bytes.LastIndex(out, []byte("\n"))
	if lastNewline < 0 {
		return nil, fmt.Errorf("unexpected curl output format")
	}
	statusCode := string(bytes.TrimSpace(out[lastNewline:]))
	respBody := out[:lastNewline]

	if statusCode != "200" {
		return nil, fmt.Errorf("API error %s: %s", statusCode, string(respBody))
	}

	return respBody, nil
}

// Unused but kept for potential future use with native Go HTTP.
var _ = (*http.Client)(nil)
var _ = io.Discard
