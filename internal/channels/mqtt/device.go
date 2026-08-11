package mqtt

import "github.com/nugget/thane-ai-agent/internal/platform/buildinfo"

// DeviceInfo is the device block of the device-based MQTT discovery
// payload — the Home Assistant device registry identity every component
// published by this instance hangs off. One block describes the whole
// device; it appears once, at the payload root.
type DeviceInfo struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	SWVersion    string   `json:"sw_version"`
	SerialNumber string   `json:"serial_number,omitempty"`
}

// OriginInfo is the origin block of the discovery payload — it names
// the software that published the discovery message so an operator can
// tell where a discovered device came from. Home Assistant asks for it
// on every discovery payload.
type OriginInfo struct {
	Name       string `json:"name"`
	SWVersion  string `json:"sw_version,omitempty"`
	SupportURL string `json:"support_url,omitempty"`
}

// SensorConfig is one component entry in the device-based discovery
// payload — a single HA sensor entity. Device identity and availability
// live at the payload root shared by every component, so a component
// carries only what is entity-specific. Platform is required by HA for
// every component; the publisher defaults an empty value to "sensor".
type SensorConfig struct {
	Platform            string `json:"platform"`
	Name                string `json:"name"`
	ObjectID            string `json:"object_id,omitempty"`
	HasEntityName       bool   `json:"has_entity_name,omitempty"`
	UniqueID            string `json:"unique_id"`
	StateTopic          string `json:"state_topic"`
	JsonAttributesTopic string `json:"json_attributes_topic,omitempty"`
	Icon                string `json:"icon,omitempty"`
	DeviceClass         string `json:"device_class,omitempty"`
	UnitOfMeasurement   string `json:"unit_of_measurement,omitempty"`
	StateClass          string `json:"state_class,omitempty"`
	ValueTemplate       string `json:"value_template,omitempty"`
	EntityCategory      string `json:"entity_category,omitempty"`
}

// availabilityEntry is one entry of the shared availability list. The
// list form is HA's current convention; payload_available defaults to
// "online" and payload_not_available to "offline", which is exactly
// what the publisher emits, so only the topic needs stating.
type availabilityEntry struct {
	Topic string `json:"topic"`
}

// deviceDiscoveryConfig is the single retained payload published to
// <discovery_prefix>/device/<device>/config — HA's device-based
// discovery format (recommended since 2024.11). It replaces the legacy
// one-retained-message-per-entity layout: the device block, origin
// block, and availability list appear once, and every entity is an
// entry in Components keyed by its stable entity suffix. Full-name keys
// are used throughout; HA documents the abbreviated forms (dev/o/cmps)
// as optional.
type deviceDiscoveryConfig struct {
	Device       DeviceInfo              `json:"device"`
	Origin       OriginInfo              `json:"origin"`
	Availability []availabilityEntry     `json:"availability"`
	Components   map[string]SensorConfig `json:"components"`
}

// NewDeviceInfo creates a DeviceInfo from the persistent instance ID
// and the human-readable device name. The instance ID is used as the
// primary HA device identifier (stable across renames) and doubles as
// the serial number; the device name appears in the HA UI.
func NewDeviceInfo(instanceID, deviceName string) DeviceInfo {
	return DeviceInfo{
		Identifiers:  []string{instanceID},
		Name:         deviceName,
		Manufacturer: "Hollow Oak",
		Model:        "Thane AI Agent",
		SWVersion:    buildinfo.Version,
		SerialNumber: instanceID,
	}
}

// NewOriginInfo creates the origin block identifying this thane build
// as the discovery publisher.
func NewOriginInfo() OriginInfo {
	return OriginInfo{
		Name:       "Thane AI Agent",
		SWVersion:  buildinfo.Version,
		SupportURL: "https://github.com/nugget/thane-ai-agent",
	}
}
