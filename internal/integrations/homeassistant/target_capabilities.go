package homeassistant

import (
	"context"
	"fmt"
)

// GetTriggersForTarget returns the purpose-specific trigger identifiers
// (domain.trigger_name) applicable to entities of the given target —
// the same vocabulary the 2026.7 automation editor offers a human for
// that target. The target uses service-call target structure
// (entity_id/device_id/area_id/floor_id/label_id).
func (c *Client) GetTriggersForTarget(ctx context.Context, target map[string]any) ([]string, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.itemsForTarget(ctx, "get_triggers_for_target", target)
}

// GetConditionsForTarget returns the purpose-specific condition
// identifiers applicable to entities of the given target.
func (c *Client) GetConditionsForTarget(ctx context.Context, target map[string]any) ([]string, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.itemsForTarget(ctx, "get_conditions_for_target", target)
}

// GetServicesForTarget returns the service identifiers applicable to
// entities of the given target — what can actually be done to them.
func (c *Client) GetServicesForTarget(ctx context.Context, target map[string]any) ([]string, error) {
	ws, err := c.requireWS()
	if err != nil {
		return nil, err
	}
	return ws.itemsForTarget(ctx, "get_services_for_target", target)
}

// itemsForTarget implements the shared shape of the three
// *_for_target discovery commands: identical request structure,
// identical string-array result.
func (c *WSClient) itemsForTarget(ctx context.Context, msgType string, target map[string]any) ([]string, error) {
	var result []string
	if err := c.call(ctx, msgType, map[string]any{"target": target}, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", msgType, err)
	}
	return result, nil
}
