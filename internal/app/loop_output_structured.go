package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
)

// structuredOutputBinding is one loop-declared structured payload output
// resolved to the entity it publishes to. It travels from spec hydration
// into the generated tool handler so the handler needs no access to the
// loop spec.
type structuredOutputBinding struct {
	// EntitySuffix is the ref's suffix: the MQTT topic segment and the
	// tail of the Home Assistant object ID.
	EntitySuffix string
	// Label is the human-readable entity name, taken from the output
	// declaration so an operator can tell two complications apart in
	// the Home Assistant UI.
	Label string
	// Target is the resolved slot contract.
	Target outputtargets.Target
}

// structuredOutputSnapshot records the most recent successful publish for
// one binding.
type structuredOutputSnapshot struct {
	Payload outputtargets.Payload `json:"payload"`
	At      time.Time             `json:"at"`
}

// structuredOutputSink publishes validated slot payloads to the surface
// that renders them. It is an interface so the tool builder can be tested
// without a broker, and so a second sink (a different transport, a
// different rendering surface) can be added without touching the tool
// generation path.
type structuredOutputSink interface {
	// Publish sends one complete payload, registering the entity if the
	// sink has not seen it before.
	Publish(ctx context.Context, binding structuredOutputBinding, payload outputtargets.Payload) error
	// EntityID returns the identifier the payload lands on, for tool
	// descriptions and model-facing context. It must be answerable
	// before the first publish.
	EntityID(entitySuffix string) string
	// Last returns the most recent successful publish, if this process
	// has made one.
	Last(entitySuffix string) (structuredOutputSnapshot, bool)
}

// mqttStructuredOutputSink publishes payloads as Home Assistant MQTT
// discovery sensors: the primary slot becomes the sensor state and the
// remaining slots become its JSON attributes.
//
// The publisher is resolved on each call rather than captured at
// construction because loop hydration can run before the MQTT server
// wiring assigns App.mqttPub. A payload published before the broker
// connection is up fails loudly — the alternative, silently buffering
// it, would leave a complication showing stale data with nothing in the
// logs to explain why.
type mqttStructuredOutputSink struct {
	app *App

	mu   sync.Mutex
	last map[string]structuredOutputSnapshot
	// now is a clock seam for tests; nil uses time.Now.
	now func() time.Time
}

// structuredOutputSink returns the process-wide sink, or nil when MQTT
// is not configured. A nil return is what makes hydration reject a
// structured payload declaration with a configuration error instead of
// registering a tool that could never publish.
func (a *App) structuredOutputSink() structuredOutputSink {
	if a == nil || a.cfg == nil || !a.cfg.MQTT.Configured() {
		return nil
	}
	a.structuredOutputsOnce.Do(func() {
		a.structuredOutputs = &mqttStructuredOutputSink{
			app:  a,
			last: make(map[string]structuredOutputSnapshot),
		}
	})
	return a.structuredOutputs
}

func (s *mqttStructuredOutputSink) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// EntityID derives the Home Assistant entity ID from the configured
// device name, so it is answerable before the publisher exists and
// before the first payload is sent.
func (s *mqttStructuredOutputSink) EntityID(entitySuffix string) string {
	if s == nil || s.app == nil || s.app.cfg == nil {
		return ""
	}
	return "sensor." + mqtt.ObjectIDPrefix(s.app.cfg.MQTT.DeviceName) + entitySuffix
}

func (s *mqttStructuredOutputSink) Last(entitySuffix string) (structuredOutputSnapshot, bool) {
	if s == nil {
		return structuredOutputSnapshot{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.last[entitySuffix]
	return snapshot, ok
}

func (s *mqttStructuredOutputSink) Publish(ctx context.Context, binding structuredOutputBinding, payload outputtargets.Payload) error {
	if s == nil || s.app == nil {
		return fmt.Errorf("structured output sink is not configured")
	}
	publisher := s.app.mqttPub
	if publisher == nil {
		return fmt.Errorf("MQTT publishing is not running yet, so %s cannot be updated; retry on a later iteration", s.EntityID(binding.EntitySuffix))
	}

	if err := publisher.EnsureSensor(ctx, s.sensorConfig(publisher, binding)); err != nil {
		return fmt.Errorf("register %s with the MQTT broker: %w", s.EntityID(binding.EntitySuffix), err)
	}

	var attributes []byte
	if len(payload.Attributes) > 0 {
		encoded, err := json.Marshal(payload.Attributes)
		if err != nil {
			return fmt.Errorf("marshal structured output attributes: %w", err)
		}
		attributes = encoded
	} else {
		// An explicit empty object clears attributes left over from a
		// previous richer payload. Sending nothing would retain them,
		// and the tool contract promises omitted slots are cleared.
		attributes = []byte("{}")
	}

	if err := publisher.PublishDynamicState(ctx, binding.EntitySuffix, payload.State, attributes); err != nil {
		return fmt.Errorf("publish %s: %w", s.EntityID(binding.EntitySuffix), err)
	}

	s.mu.Lock()
	s.last[binding.EntitySuffix] = structuredOutputSnapshot{Payload: payload, At: s.clock()}
	s.mu.Unlock()
	return nil
}

// sensorConfig builds the discovery payload for a binding. It is rebuilt
// on every publish rather than cached because EnsureSensor replaces by
// suffix, which keeps a renamed or retargeted output from stranding a
// stale discovery config in Home Assistant.
func (s *mqttStructuredOutputSink) sensorConfig(publisher *mqtt.Publisher, binding structuredOutputBinding) mqtt.DynamicSensor {
	return mqtt.DynamicSensor{
		EntitySuffix: binding.EntitySuffix,
		Config: mqtt.SensorConfig{
			Name:                binding.Label,
			ObjectID:            publisher.ObjectIDPrefix() + binding.EntitySuffix,
			HasEntityName:       true,
			UniqueID:            s.app.mqttInstanceID + "_" + binding.EntitySuffix,
			StateTopic:          publisher.StateTopic(binding.EntitySuffix),
			JsonAttributesTopic: publisher.AttributesTopic(binding.EntitySuffix),
			AvailabilityTopic:   publisher.AvailabilityTopic(),
			Device:              publisher.Device(),
			Icon:                binding.Target.Icon,
		},
	}
}
