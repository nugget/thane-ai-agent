package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// LabelRegistryEntry represents a Home Assistant label.
type LabelRegistryEntry struct {
	LabelID     string `json:"label_id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// DeviceRegistryEntry represents a Home Assistant device registry row.
type DeviceRegistryEntry struct {
	ID                    string               `json:"id"`
	ConfigEntries         []string             `json:"config_entries"`
	ConfigEntriesSubentry map[string][]*string `json:"config_entries_subentries"`
	Connections           [][]string           `json:"connections"`
	Identifiers           [][]string           `json:"identifiers"`
	Manufacturer          string               `json:"manufacturer"`
	Model                 string               `json:"model"`
	ModelID               string               `json:"model_id"`
	Name                  string               `json:"name"`
	Labels                []string             `json:"labels"`
	SWVersion             string               `json:"sw_version"`
	HWVersion             string               `json:"hw_version"`
	SerialNumber          string               `json:"serial_number"`
	ViaDeviceID           string               `json:"via_device_id"`
	AreaID                string               `json:"area_id"`
	NameByUser            string               `json:"name_by_user"`
	EntryType             string               `json:"entry_type"`
	DisabledBy            string               `json:"disabled_by"`
	ConfigurationURL      string               `json:"configuration_url"`
	PrimaryConfigEntry    string               `json:"primary_config_entry"`
}

// ConfigValidationResult mirrors Home Assistant's validate_config response.
type ConfigValidationResult struct {
	Valid bool   `json:"valid"`
	Error string `json:"error"`
}

func (c *Client) requireWS() (*WSClient, error) {
	if c.ws == nil {
		return nil, fmt.Errorf("home assistant websocket client not configured")
	}
	return c.ws, nil
}

// GetLabelRegistry retrieves all labels from Home Assistant.
func (c *Client) GetLabelRegistry(ctx context.Context) ([]LabelRegistryEntry, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.GetLabelRegistry(ctx)
}

// GetDeviceRegistry retrieves all devices from Home Assistant.
func (c *Client) GetDeviceRegistry(ctx context.Context) ([]DeviceRegistryEntry, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.GetDeviceRegistry(ctx)
}

// GetEntityRegistryEntry retrieves the extended registry entry for an entity.
func (c *Client) GetEntityRegistryEntry(ctx context.Context, entityID string) (*EntityRegistryEntry, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.GetEntityRegistryEntry(ctx, entityID)
}

// UpdateEntityRegistryEntry updates entity metadata through the registry API.
func (c *Client) UpdateEntityRegistryEntry(ctx context.Context, entityID string, updates map[string]any) (*EntityRegistryEntry, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.UpdateEntityRegistryEntry(ctx, entityID, updates)
}

// ValidateConfig validates automation trigger/condition/action sections.
func (c *Client) ValidateConfig(ctx context.Context, sections map[string]any) (map[string]ConfigValidationResult, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.ValidateConfig(ctx, sections)
}

// GetAutomationConfigByEntityID fetches an automation's raw config by entity_id.
func (c *Client) GetAutomationConfigByEntityID(ctx context.Context, entityID string) (map[string]any, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.GetAutomationStateConfig(ctx, entityID)
}

// GetAutomationConfigByID fetches an automation's raw config by config id.
func (c *Client) GetAutomationConfigByID(ctx context.Context, id string) (map[string]any, error) {
	var cfg map[string]any
	if err := c.get(ctx, "/api/config/automation/config/"+url.PathEscape(id), &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveAutomationConfig creates or replaces an automation config object.
func (c *Client) SaveAutomationConfig(ctx context.Context, id string, cfg map[string]any) error {
	return c.post(ctx, "/api/config/automation/config/"+url.PathEscape(id), cfg, nil)
}

// DeleteAutomationConfig deletes an automation config object by id.
func (c *Client) DeleteAutomationConfig(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/config/automation/config/"+url.PathEscape(id), nil)
}

// ApplyAutomationEnabledState turns an automation on or off.
func (c *Client) ApplyAutomationEnabledState(ctx context.Context, entityID string, enabled bool) error {
	service := "turn_off"
	if enabled {
		service = "turn_on"
	}
	return c.CallService(ctx, "automation", service, map[string]any{"entity_id": entityID})
}

func (c *WSClient) call(ctx context.Context, msgType string, fields map[string]any, result any) error {
	id := c.msgID.Add(1)
	msg := map[string]any{
		"id":   id,
		"type": msgType,
	}
	for k, v := range fields {
		msg[k] = v
	}

	raw, err := c.sendAndWait(ctx, id, msg)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("unmarshal %s response: %w", msgType, err)
	}
	return nil
}

// GetDeviceRegistry retrieves the device registry via WebSocket.
func (c *WSClient) GetDeviceRegistry(ctx context.Context) ([]DeviceRegistryEntry, error) {
	var devices []DeviceRegistryEntry
	if err := c.call(ctx, "config/device_registry/list", nil, &devices); err != nil {
		return nil, fmt.Errorf("get device registry: %w", err)
	}
	return devices, nil
}

// GetLabelRegistry retrieves labels via WebSocket.
func (c *WSClient) GetLabelRegistry(ctx context.Context) ([]LabelRegistryEntry, error) {
	var labels []LabelRegistryEntry
	if err := c.call(ctx, "config/label_registry/list", nil, &labels); err != nil {
		return nil, fmt.Errorf("get label registry: %w", err)
	}
	return labels, nil
}

// GetEntityRegistryEntry retrieves an extended entity registry row.
func (c *WSClient) GetEntityRegistryEntry(ctx context.Context, entityID string) (*EntityRegistryEntry, error) {
	var entry EntityRegistryEntry
	if err := c.call(ctx, "config/entity_registry/get", map[string]any{"entity_id": entityID}, &entry); err != nil {
		return nil, fmt.Errorf("get entity registry entry: %w", err)
	}
	return &entry, nil
}

// UpdateEntityRegistryEntry updates an entity registry row.
func (c *WSClient) UpdateEntityRegistryEntry(ctx context.Context, entityID string, updates map[string]any) (*EntityRegistryEntry, error) {
	fields := map[string]any{"entity_id": entityID}
	for k, v := range updates {
		fields[k] = v
	}

	var entry EntityRegistryEntry
	if err := c.call(ctx, "config/entity_registry/update", fields, &entry); err != nil {
		return nil, fmt.Errorf("update entity registry entry: %w", err)
	}
	return &entry, nil
}

// ValidateConfig validates trigger/condition/action sections.
func (c *WSClient) ValidateConfig(ctx context.Context, sections map[string]any) (map[string]ConfigValidationResult, error) {
	result := make(map[string]ConfigValidationResult)
	if err := c.call(ctx, "validate_config", sections, &result); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return result, nil
}

// GetAutomationStateConfig retrieves an automation's raw config by entity_id.
func (c *WSClient) GetAutomationStateConfig(ctx context.Context, entityID string) (map[string]any, error) {
	var result struct {
		Config map[string]any `json:"config"`
	}
	if err := c.call(ctx, "automation/config", map[string]any{"entity_id": entityID}, &result); err != nil {
		return nil, fmt.Errorf("get automation config: %w", err)
	}
	return result.Config, nil
}
