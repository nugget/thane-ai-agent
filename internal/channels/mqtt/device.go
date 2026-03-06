package mqtt

import "github.com/nugget/thane-ai-agent/internal/buildinfo"

// DeviceInfo holds the Home Assistant device registry fields shared
// across all MQTT discovery config payloads. Every sensor entity
// published by this instance references the same device block so HA
// groups them under a single device page.
type DeviceInfo struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	SWVersion    string   `json:"sw_version"`
}

// SensorConfig is the JSON payload for an HA MQTT sensor discovery
// message. It is published (retained) to the discovery topic on every
// broker (re-)connect.
type SensorConfig struct {
	Name                string     `json:"name"`
	ObjectID            string     `json:"object_id,omitempty"`
	HasEntityName       bool       `json:"has_entity_name,omitempty"`
	UniqueID            string     `json:"unique_id"`
	StateTopic          string     `json:"state_topic"`
	AvailabilityTopic   string     `json:"availability_topic"`
	JsonAttributesTopic string     `json:"json_attributes_topic,omitempty"`
	Device              DeviceInfo `json:"device"`
	Icon                string     `json:"icon,omitempty"`
	UnitOfMeasurement   string     `json:"unit_of_measurement,omitempty"`
	StateClass          string     `json:"state_class,omitempty"`
	ValueTemplate       string     `json:"value_template,omitempty"`
	EntityCategory      string     `json:"entity_category,omitempty"`
}

// NewDeviceInfo creates a DeviceInfo from the persistent instance ID
// and the human-readable device name. The instance ID is used as the
// primary HA device identifier (stable across renames); the device
// name appears in the HA UI.
func NewDeviceInfo(instanceID, deviceName string) DeviceInfo {
	return DeviceInfo{
		Identifiers:  []string{instanceID},
		Name:         deviceName,
		Manufacturer: "Hollow Oak",
		Model:        "Thane AI Agent",
		SWVersion:    buildinfo.Version,
	}
}
