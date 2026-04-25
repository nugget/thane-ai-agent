package mqtt

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

func TestLoadOrCreateInstanceID_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	id, err := LoadOrCreateInstanceID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateInstanceID() error = %v", err)
	}
	if id == "" {
		t.Fatal("LoadOrCreateInstanceID() returned empty string")
	}

	// Verify the file was written.
	data, err := os.ReadFile(filepath.Join(dir, "instance_id"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != id {
		t.Errorf("file content = %q, want %q", got, id)
	}
}

func TestLoadOrCreateInstanceID_ReturnsExisting(t *testing.T) {
	dir := t.TempDir()

	// Create the first time.
	first, err := LoadOrCreateInstanceID(dir)
	if err != nil {
		t.Fatalf("first call error = %v", err)
	}

	// Second call should return the same value.
	second, err := LoadOrCreateInstanceID(dir)
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}
	if second != first {
		t.Errorf("second = %q, want %q (should be stable)", second, first)
	}
}

func TestLoadOrCreateInstanceID_UUIDFormat(t *testing.T) {
	dir := t.TempDir()

	id, err := LoadOrCreateInstanceID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateInstanceID() error = %v", err)
	}

	// UUIDv7 format: 8-4-4-4-12 hex digits.
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("id %q does not look like a UUID (expected 5 dash-separated parts)", id)
	}
}

func TestNewDeviceInfo(t *testing.T) {
	info := NewDeviceInfo("test-instance-id", "test-device")
	if info.Name != "test-device" {
		t.Errorf("Name = %q, want %q", info.Name, "test-device")
	}
	if len(info.Identifiers) != 1 || info.Identifiers[0] != "test-instance-id" {
		t.Errorf("Identifiers = %v, want [test-instance-id]", info.Identifiers)
	}
	if info.Manufacturer != "Hollow Oak" {
		t.Errorf("Manufacturer = %q, want %q", info.Manufacturer, "Hollow Oak")
	}
	if info.Model != "Thane AI Agent" {
		t.Errorf("Model = %q, want %q", info.Model, "Thane AI Agent")
	}
}

func TestPublisher_TopicPaths(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:          "mqtt://localhost:1883",
		DeviceName:      "aimee-thane",
		DiscoveryPrefix: "homeassistant",
	}
	p := New(cfg, "test-id", NewDailyTokens(time.UTC), nil, nil)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"baseTopic", p.baseTopic(), "thane/aimee-thane"},
		{"AvailabilityTopic", p.AvailabilityTopic(), "thane/aimee-thane/availability"},
		{"StateTopic uptime", p.StateTopic("uptime"), "thane/aimee-thane/uptime/state"},
		{"discoveryTopic sensor uptime", p.discoveryTopic("sensor", "uptime"), "homeassistant/sensor/aimee-thane/uptime/config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestPublisher_SensorDefinitions(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:             "mqtt://localhost:1883",
		DeviceName:         "test-thane",
		DiscoveryPrefix:    "homeassistant",
		PublishIntervalSec: 60,
	}
	p := New(cfg, "instance-123", NewDailyTokens(time.UTC), nil, nil)

	defs := p.sensorDefinitions()

	expectedEntities := []string{
		"uptime", "version",
		"tokens_today", "last_request", "default_model",
	}

	if len(defs) != len(expectedEntities) {
		t.Fatalf("got %d sensor definitions, want %d", len(defs), len(expectedEntities))
	}

	// Expected short names (no device name prefix — issue #164).
	expectedNames := map[string]string{
		"uptime":        "Uptime",
		"version":       "Version",
		"tokens_today":  "Tokens Today",
		"last_request":  "Last Request",
		"default_model": "Default Model",
	}

	entitySet := make(map[string]bool)
	for _, d := range defs {
		entitySet[d.entitySuffix] = true

		// Sensor Name must NOT contain the device name (causes HA
		// double-prefix entity IDs like sensor.foo_foo_uptime).
		if strings.Contains(d.config.Name, cfg.DeviceName) {
			t.Errorf("sensor %s: Name %q contains device name %q (double-prefix bug #164)",
				d.entitySuffix, d.config.Name, cfg.DeviceName)
		}

		// Verify the expected short name.
		if want, ok := expectedNames[d.entitySuffix]; ok {
			if d.config.Name != want {
				t.Errorf("sensor %s: Name = %q, want %q",
					d.entitySuffix, d.config.Name, want)
			}
		}

		// Every sensor should reference the availability topic.
		wantAvail := "thane/test-thane/availability"
		if d.config.AvailabilityTopic != wantAvail {
			t.Errorf("sensor %s: AvailabilityTopic = %q, want %q",
				d.entitySuffix, d.config.AvailabilityTopic, wantAvail)
		}

		// Every sensor should have a unique ID based on instance ID.
		if !strings.HasPrefix(d.config.UniqueID, "instance-123_") {
			t.Errorf("sensor %s: UniqueID = %q, should start with %q",
				d.entitySuffix, d.config.UniqueID, "instance-123_")
		}

		// ObjectID must include device name prefix so HA derives
		// entity IDs like sensor.test_thane_uptime (not sensor.uptime).
		wantObjID := p.ObjectIDPrefix() + d.entitySuffix
		if d.config.ObjectID != wantObjID {
			t.Errorf("sensor %s: ObjectID = %q, want %q",
				d.entitySuffix, d.config.ObjectID, wantObjID)
		}

		// HasEntityName must be true so HA treats the sensor Name as
		// relative to the device name (avoids double-prefix #207).
		if !d.config.HasEntityName {
			t.Errorf("sensor %s: HasEntityName = false, want true",
				d.entitySuffix)
		}

		// Every sensor should reference the device.
		if len(d.config.Device.Identifiers) == 0 {
			t.Errorf("sensor %s: Device.Identifiers is empty", d.entitySuffix)
		}
	}

	for _, name := range expectedEntities {
		if !entitySet[name] {
			t.Errorf("missing sensor definition for %q", name)
		}
	}
}

func TestPublisher_SetMessageHandler(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:             "mqtt://localhost:1883",
		DeviceName:         "test-thane",
		DiscoveryPrefix:    "homeassistant",
		PublishIntervalSec: 60,
		Subscriptions: []config.SubscriptionConfig{
			{Topic: "homeassistant/+/+/state"},
			{Topic: "frigate/events"},
		},
	}
	p := New(cfg, "test-id-1234", NewDailyTokens(time.UTC), nil, nil)

	var called bool
	var gotTopic string
	var gotPayload []byte
	p.SetMessageHandler(func(topic string, payload []byte) {
		called = true
		gotTopic = topic
		gotPayload = payload
	})

	if p.handler == nil {
		t.Fatal("handler should be set after SetMessageHandler")
	}

	p.handler("test/topic", []byte("hello"))
	if !called {
		t.Error("custom handler was not called")
	}
	if gotTopic != "test/topic" {
		t.Errorf("topic = %q, want %q", gotTopic, "test/topic")
	}
	if string(gotPayload) != "hello" {
		t.Errorf("payload = %q, want %q", gotPayload, "hello")
	}
}

func TestIsMQTTNoConnectionError(t *testing.T) {
	err := fmt.Errorf("publish failed: %w", errors.New("no connection available"))
	if !isMQTTNoConnectionError(err) {
		t.Fatal("isMQTTNoConnectionError() = false, want true for wrapped connection error")
	}

	if isMQTTNoConnectionError(errors.New("different failure")) {
		t.Fatal("isMQTTNoConnectionError() = true, want false for unrelated error")
	}
}

func TestPublisher_RegisterSensors(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:             "mqtt://localhost:1883",
		DeviceName:         "test-thane",
		DiscoveryPrefix:    "homeassistant",
		PublishIntervalSec: 60,
	}
	p := New(cfg, "instance-123", NewDailyTokens(time.UTC), nil, nil)

	// No dynamic sensors initially.
	staticCount := len(p.sensorDefinitions())

	p.RegisterSensors([]DynamicSensor{
		{
			EntitySuffix: "nugget_ap",
			Config: SensorConfig{
				Name:     "Nugget AP",
				UniqueID: "instance-123_nugget_ap",
			},
		},
		{
			EntitySuffix: "dan_ap",
			Config: SensorConfig{
				Name:     "Dan AP",
				UniqueID: "instance-123_dan_ap",
			},
		},
	})

	p.mu.Lock()
	dynCount := len(p.dynamicSensors)
	p.mu.Unlock()

	if dynCount != 2 {
		t.Errorf("dynamicSensors count = %d, want 2", dynCount)
	}

	// Static sensors should be unaffected.
	if got := len(p.sensorDefinitions()); got != staticCount {
		t.Errorf("static sensor count changed: got %d, want %d", got, staticCount)
	}
}

func TestPublisher_AttributesTopic(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:          "mqtt://localhost:1883",
		DeviceName:      "aimee-thane",
		DiscoveryPrefix: "homeassistant",
	}
	p := New(cfg, "test-id", NewDailyTokens(time.UTC), nil, nil)

	got := p.AttributesTopic("nugget_ap")
	want := "thane/aimee-thane/nugget_ap/attributes"
	if got != want {
		t.Errorf("AttributesTopic() = %q, want %q", got, want)
	}
}

func TestPublisher_DeviceGetter(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:          "mqtt://localhost:1883",
		DeviceName:      "test-device",
		DiscoveryPrefix: "homeassistant",
	}
	p := New(cfg, "instance-abc", NewDailyTokens(time.UTC), nil, nil)

	dev := p.Device()
	if dev.Name != "test-device" {
		t.Errorf("Device().Name = %q, want %q", dev.Name, "test-device")
	}
	if len(dev.Identifiers) != 1 || dev.Identifiers[0] != "instance-abc" {
		t.Errorf("Device().Identifiers = %v, want [instance-abc]", dev.Identifiers)
	}
}

func TestPublisher_ObjectIDPrefix(t *testing.T) {
	tests := []struct {
		deviceName string
		want       string
	}{
		{"aimee-thane", "aimee_thane_"},
		{"simple", "simple_"},
		{"multi-hyphen-name", "multi_hyphen_name_"},
	}
	for _, tt := range tests {
		t.Run(tt.deviceName, func(t *testing.T) {
			p := New(config.MQTTConfig{
				Broker:     "mqtt://localhost:1883",
				DeviceName: tt.deviceName,
			}, "id", NewDailyTokens(time.UTC), nil, nil)
			if got := p.ObjectIDPrefix(); got != tt.want {
				t.Errorf("ObjectIDPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSensorConfig_JsonAttributesTopic(t *testing.T) {
	// With JsonAttributesTopic set.
	cfg := SensorConfig{
		Name:                "Test",
		UniqueID:            "test_1",
		StateTopic:          "thane/test/state",
		AvailabilityTopic:   "thane/test/availability",
		JsonAttributesTopic: "thane/test/attributes",
		Device:              DeviceInfo{Identifiers: []string{"id"}, Name: "d"},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if !strings.Contains(string(data), `"json_attributes_topic"`) {
		t.Errorf("expected json_attributes_topic in JSON:\n%s", data)
	}

	// Without JsonAttributesTopic — omitempty should exclude it.
	cfg.JsonAttributesTopic = ""
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if strings.Contains(string(data), `"json_attributes_topic"`) {
		t.Errorf("json_attributes_topic should be omitted when empty:\n%s", data)
	}
}

func TestPublisher_BuildClientConfig_HandlerRegistered(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:     "mqtt://localhost:1883",
		DeviceName: "test-thane",
		Subscriptions: []config.SubscriptionConfig{
			{Topic: "test/topic"},
		},
	}

	var handlerCalled bool
	p := New(cfg, "instance-123", NewDailyTokens(time.UTC), nil, nil)
	p.SetMessageHandler(func(topic string, payload []byte) {
		handlerCalled = true
	})

	brokerURL, err := url.Parse(cfg.Broker)
	if err != nil {
		t.Fatalf("parse broker URL: %v", err)
	}

	pahoCfg := p.buildClientConfig(brokerURL)

	// The handler must be registered on pahoCfg.OnPublishReceived so it
	// persists across autopaho reconnects. Previously this was done via
	// cm.AddOnPublishReceived() which silently no-oped when c.cli was nil.
	if len(pahoCfg.OnPublishReceived) == 0 {
		t.Fatal("OnPublishReceived is empty; handler was not registered on the config")
	}

	// Verify the registered handler actually routes to our MessageHandler.
	pr := paho.PublishReceived{
		Packet: &paho.Publish{
			Topic:   "test/topic",
			Payload: []byte("hello"),
		},
	}
	if _, err := pahoCfg.OnPublishReceived[0](pr); err != nil {
		t.Fatalf("OnPublishReceived handler returned error: %v", err)
	}
	if !handlerCalled {
		t.Error("OnPublishReceived handler did not invoke the MessageHandler")
	}
}

func TestPublisher_BuildClientConfig_NoHandlerWithoutSubs(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:     "mqtt://localhost:1883",
		DeviceName: "test-thane",
		// No subscriptions.
	}
	p := New(cfg, "instance-123", NewDailyTokens(time.UTC), nil, nil)

	brokerURL, err := url.Parse(cfg.Broker)
	if err != nil {
		t.Fatalf("parse broker URL: %v", err)
	}

	pahoCfg := p.buildClientConfig(brokerURL)

	if len(pahoCfg.OnPublishReceived) != 0 {
		t.Errorf("OnPublishReceived should be empty without subscriptions, got %d handlers",
			len(pahoCfg.OnPublishReceived))
	}
}

func TestPublisher_BuildClientConfig_DynamicTopicsTriggersHandler(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:     "mqtt://localhost:1883",
		DeviceName: "test-thane",
		// No config subscriptions, but dynamic topics set.
	}
	p := New(cfg, "instance-123", NewDailyTokens(time.UTC), nil, nil)
	p.SetDynamicTopics(func() []string { return []string{"dynamic/topic"} })

	brokerURL, err := url.Parse(cfg.Broker)
	if err != nil {
		t.Fatalf("parse broker URL: %v", err)
	}

	pahoCfg := p.buildClientConfig(brokerURL)

	if len(pahoCfg.OnPublishReceived) == 0 {
		t.Fatal("OnPublishReceived should be registered when dynamicTopics is set")
	}
}

func TestMQTTConfig_Configured(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.MQTTConfig
		want bool
	}{
		{"both set", config.MQTTConfig{Broker: "mqtt://localhost", DeviceName: "thane"}, true},
		{"missing broker", config.MQTTConfig{DeviceName: "thane"}, false},
		{"missing device_name", config.MQTTConfig{Broker: "mqtt://localhost"}, false},
		{"empty", config.MQTTConfig{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPublisher_CollectSubscribeTopics(t *testing.T) {
	tests := []struct {
		name          string
		configTopics  []config.SubscriptionConfig
		dynamicTopics []string
		want          []string
	}{
		{
			name: "config only",
			configTopics: []config.SubscriptionConfig{
				{Topic: "a/b"},
				{Topic: "c/d"},
			},
			want: []string{"a/b", "c/d"},
		},
		{
			name:          "dynamic only",
			dynamicTopics: []string{"x/y", "z/w"},
			want:          []string{"x/y", "z/w"},
		},
		{
			name: "config and dynamic merged",
			configTopics: []config.SubscriptionConfig{
				{Topic: "a/b"},
			},
			dynamicTopics: []string{"c/d"},
			want:          []string{"a/b", "c/d"},
		},
		{
			name: "duplicates removed",
			configTopics: []config.SubscriptionConfig{
				{Topic: "a/b"},
				{Topic: "a/b"},
			},
			dynamicTopics: []string{"a/b", "c/d"},
			want:          []string{"a/b", "c/d"},
		},
		{
			name: "empty",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Publisher{
				cfg: config.MQTTConfig{
					Subscriptions: tt.configTopics,
				},
			}
			if tt.dynamicTopics != nil {
				dynTopics := tt.dynamicTopics // capture
				p.dynamicTopics = func() []string { return dynTopics }
			}

			got := p.collectSubscribeTopics()
			if len(got) != len(tt.want) {
				t.Fatalf("collectSubscribeTopics() = %v, want %v", got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("topic[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}
