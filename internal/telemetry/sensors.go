package telemetry

import (
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
)

// SensorBuilder constructs Home Assistant MQTT sensor definitions for
// telemetry metrics. It uses topic and device info from the MQTT
// publisher to produce correctly namespaced discovery payloads.
type SensorBuilder struct {
	InstanceID        string
	Prefix            string // HA object_id prefix, e.g. "aimee_thane_"
	StateTopicFn      func(string) string
	AttributesTopicFn func(string) string
	AvailabilityTopic string
	Device            mqtt.DeviceInfo
}

// sensorSpec describes a single telemetry sensor for registration.
type sensorSpec struct {
	suffix   string // entity suffix in topic path
	name     string // human-readable HA name
	icon     string // MDI icon
	unit     string // unit_of_measurement (empty = none)
	class    string // state_class (empty = none)
	category string // entity_category (empty = none)
	hasAttrs bool   // whether this sensor publishes JSON attributes
}

// StaticSensors returns all telemetry sensor definitions to register
// with the MQTT publisher via [mqtt.Publisher.RegisterSensors]. These
// are the fixed sensors — per-loop sensors are registered dynamically
// as loops appear.
func (b *SensorBuilder) StaticSensors() []mqtt.DynamicSensor {
	specs := []sensorSpec{
		// System health — DB sizes.
		{suffix: "db_main_size", name: "DB Main Size", icon: "mdi:database", unit: "B", class: "measurement", category: "diagnostic"},
		{suffix: "db_logs_size", name: "DB Logs Size", icon: "mdi:database", unit: "B", class: "measurement", category: "diagnostic"},
		{suffix: "db_usage_size", name: "DB Usage Size", icon: "mdi:database", unit: "B", class: "measurement", category: "diagnostic"},
		{suffix: "db_attachments_size", name: "DB Attachments Size", icon: "mdi:database", unit: "B", class: "measurement", category: "diagnostic"},

		// Token usage (24h).
		{suffix: "tokens_24h_input", name: "Tokens 24h Input", icon: "mdi:arrow-down", unit: "tokens", class: "measurement"},
		{suffix: "tokens_24h_output", name: "Tokens 24h Output", icon: "mdi:arrow-up", unit: "tokens", class: "measurement"},
		{suffix: "tokens_24h_cost", name: "Tokens 24h Cost", icon: "mdi:currency-usd", unit: "USD", class: "measurement", hasAttrs: true},

		// Sessions & context.
		{suffix: "active_sessions", name: "Active Sessions", icon: "mdi:message-text-outline", unit: "sessions", class: "measurement"},
		{suffix: "context_utilization", name: "Context Utilization", icon: "mdi:gauge", unit: "%", class: "measurement"},

		// Request performance.
		{suffix: "requests_24h", name: "Requests 24h", icon: "mdi:counter", unit: "requests", class: "measurement"},
		{suffix: "errors_24h", name: "Errors 24h", icon: "mdi:alert-circle", unit: "errors", class: "measurement"},
		{suffix: "request_latency_p50", name: "Latency P50", icon: "mdi:timer-outline", unit: "ms", class: "measurement"},
		{suffix: "request_latency_p95", name: "Latency P95", icon: "mdi:timer-alert-outline", unit: "ms", class: "measurement"},

		// Loop aggregates.
		{suffix: "loops_active", name: "Loops Active", icon: "mdi:cog-play", unit: "loops", class: "measurement"},
		{suffix: "loops_sleeping", name: "Loops Sleeping", icon: "mdi:cog-pause", unit: "loops", class: "measurement"},
		{suffix: "loops_errored", name: "Loops Errored", icon: "mdi:cog-off", unit: "loops", class: "measurement"},
		{suffix: "loops_total", name: "Loops Total", icon: "mdi:cog", unit: "loops", class: "measurement"},

		// Attachment store.
		{suffix: "attachments_total", name: "Attachments Total", icon: "mdi:paperclip", unit: "files", class: "measurement"},
		{suffix: "attachments_total_bytes", name: "Attachments Size", icon: "mdi:paperclip", unit: "B", class: "measurement"},
		{suffix: "attachments_unique_files", name: "Attachments Unique", icon: "mdi:file-check", unit: "files", class: "measurement"},
	}

	sensors := make([]mqtt.DynamicSensor, 0, len(specs))
	for _, s := range specs {
		sensors = append(sensors, b.buildSensor(s))
	}
	return sensors
}

// LoopSensors returns sensor definitions for a single named loop.
// Two sensors per loop: state (enum) and iterations (measurement).
func (b *SensorBuilder) LoopSensors(loopName string) []mqtt.DynamicSensor {
	stateSuffix := "loop_" + loopName + "_state"
	iterSuffix := "loop_" + loopName + "_iterations"

	return []mqtt.DynamicSensor{
		b.buildSensor(sensorSpec{
			suffix:   stateSuffix,
			name:     "Loop " + loopName + " State",
			icon:     "mdi:cog-outline",
			category: "diagnostic",
		}),
		b.buildSensor(sensorSpec{
			suffix: iterSuffix,
			name:   "Loop " + loopName + " Iterations",
			icon:   "mdi:counter",
			unit:   "iterations",
			class:  "measurement",
		}),
	}
}

// buildSensor constructs a DynamicSensor from a spec.
func (b *SensorBuilder) buildSensor(s sensorSpec) mqtt.DynamicSensor {
	cfg := mqtt.SensorConfig{
		Name:              s.name,
		ObjectID:          b.Prefix + s.suffix,
		HasEntityName:     true,
		UniqueID:          b.InstanceID + "_" + s.suffix,
		StateTopic:        b.StateTopicFn(s.suffix),
		AvailabilityTopic: b.AvailabilityTopic,
		Device:            b.Device,
		Icon:              s.icon,
	}
	if s.unit != "" {
		cfg.UnitOfMeasurement = s.unit
	}
	if s.class != "" {
		cfg.StateClass = s.class
	}
	if s.category != "" {
		cfg.EntityCategory = s.category
	}
	if s.hasAttrs {
		cfg.JsonAttributesTopic = b.AttributesTopicFn(s.suffix)
	}
	return mqtt.DynamicSensor{
		EntitySuffix: s.suffix,
		Config:       cfg,
	}
}
