package homeassistant

import "context"

// ServiceField describes one input a service accepts, as reported by
// the /api/services catalog. Description and Example carry the pieces
// a caller needs to fill the field correctly without guessing.
type ServiceField struct {
	// Name is the field's human-readable label from the catalog;
	// empty when the integration didn't provide one.
	Name string `json:"name,omitempty"`

	// Description explains what the field does, verbatim from the
	// integration's service definition.
	Description string `json:"description,omitempty"`

	// Required reports whether the service rejects calls that omit
	// this field.
	Required bool `json:"required,omitempty"`

	// Example is the catalog's sample value, useful for inferring the
	// expected shape when the description alone is ambiguous.
	Example any `json:"example,omitempty"`
}

// ServiceDescription is one service's entry in the catalog. A non-nil
// Target means the service accepts a target block (entity/device/area/
// floor/label selectors) in addition to — or instead of — explicit
// per-field addressing.
type ServiceDescription struct {
	// Name is the service's human-readable label.
	Name string `json:"name,omitempty"`

	// Description explains what the service does.
	Description string `json:"description,omitempty"`

	// Fields maps field key → schema for the service's inputs.
	Fields map[string]ServiceField `json:"fields,omitempty"`

	// Target is the raw target-selector block when the service is
	// target-addressable; nil when it takes no target.
	Target map[string]any `json:"target,omitempty"`
}

// ServiceDomain groups one domain's callable services, as returned by
// the /api/services catalog.
type ServiceDomain struct {
	Domain   string                        `json:"domain"`
	Services map[string]ServiceDescription `json:"services"`
}

// GetServices retrieves the service catalog: every callable service by
// domain, with field schemas and target support. This is the discovery
// surface behind ha_list_services — how a caller learns what
// CallService can actually do, instead of guessing names and burning a
// failed call to find out.
func (c *Client) GetServices(ctx context.Context) ([]ServiceDomain, error) {
	var domains []ServiceDomain
	if err := c.get(ctx, "/api/services", &domains); err != nil {
		return nil, err
	}
	return domains, nil
}
