package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
)

// mockMQTT captures published sensor states for verification.
type mockMQTT struct {
	mu      sync.Mutex
	states  map[string]string // entity → state value
	attrs   map[string][]byte // entity → attribute JSON
	sensors []mqtt.DynamicSensor
}

func newMockMQTT() *mockMQTT {
	return &mockMQTT{
		states: make(map[string]string),
		attrs:  make(map[string][]byte),
	}
}

func (m *mockMQTT) PublishDynamicState(_ context.Context, entity, state string, attrJSON []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[entity] = state
	if len(attrJSON) > 0 {
		m.attrs[entity] = attrJSON
	}
	return nil
}

func (m *mockMQTT) RegisterSensors(sensors []mqtt.DynamicSensor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sensors = append(m.sensors, sensors...)
}

func testBuilder() *SensorBuilder {
	return &SensorBuilder{
		InstanceID:        "test-instance",
		Prefix:            "thane_test_",
		StateTopicFn:      func(s string) string { return "thane/test/" + s + "/state" },
		AttributesTopicFn: func(s string) string { return "thane/test/" + s + "/attributes" },
		AvailabilityTopic: "thane/test/availability",
		Device:            mqtt.DeviceInfo{Name: "test"},
	}
}

func TestPublish_PublishesAllSensors(t *testing.T) {
	mqttMock := newMockMQTT()
	builder := testBuilder()

	// Create a collector with known fixed metrics.
	c := NewCollector(Sources{
		DBPaths: map[string]string{},
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	pub := NewPublisher(c, mqttMock, builder, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := pub.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify key sensors were published.
	expectedEntities := []string{
		"db_main_size", "db_logs_size", "db_usage_size", "db_attachments_size",
		"tokens_24h_input", "tokens_24h_output", "tokens_24h_cost",
		"active_sessions", "context_utilization",
		"requests_24h", "errors_24h", "request_latency_p50", "request_latency_p95",
		"loops_active", "loops_sleeping", "loops_errored", "loops_total",
		"attachments_total", "attachments_total_bytes", "attachments_unique_files",
	}

	mqttMock.mu.Lock()
	defer mqttMock.mu.Unlock()

	for _, entity := range expectedEntities {
		if _, ok := mqttMock.states[entity]; !ok {
			t.Errorf("missing published state for %q", entity)
		}
	}
}

func TestPublish_LoopDetails(t *testing.T) {
	mqttMock := newMockMQTT()
	builder := testBuilder()

	// Use a collector with sessions that return loop details.
	c := NewCollector(Sources{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	pub := NewPublisher(c, mqttMock, builder, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// Manually set up metrics with loop details by doing a publish
	// then checking. Since Collect returns empty loops, let's verify
	// the zero case first, then simulate loop data.
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify loops_total published as "0".
	mqttMock.mu.Lock()
	if mqttMock.states["loops_total"] != "0" {
		t.Errorf("loops_total = %q, want \"0\"", mqttMock.states["loops_total"])
	}
	mqttMock.mu.Unlock()
}

func TestPublish_DynamicLoopRegistration(t *testing.T) {
	mqttMock := newMockMQTT()
	builder := testBuilder()

	c := NewCollector(Sources{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	pub := NewPublisher(c, mqttMock, builder, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// Simulate publishing loop details directly.
	var errs int
	pub.publishLoopDetails(context.Background(), []LoopMetric{
		{Name: "mqtt_publisher", State: "sleeping", Iterations: 42},
		{Name: "email_poll", State: "processing", Iterations: 7},
	}, &errs)

	if errs != 0 {
		t.Errorf("publish errors = %d, want 0", errs)
	}

	// Verify dynamic sensors were registered.
	mqttMock.mu.Lock()
	sensorCount := len(mqttMock.sensors)
	loopState := mqttMock.states["loop_mqtt_publisher_state"]
	loopIter := mqttMock.states["loop_mqtt_publisher_iterations"]
	mqttMock.mu.Unlock()

	if sensorCount != 4 { // 2 loops × 2 sensors each
		t.Errorf("registered sensors = %d, want 4", sensorCount)
	}
	if loopState != "sleeping" {
		t.Errorf("loop state = %q, want sleeping", loopState)
	}
	if loopIter != strconv.Itoa(42) {
		t.Errorf("loop iterations = %q, want 42", loopIter)
	}

	// Second publish should NOT re-register.
	pub.publishLoopDetails(context.Background(), []LoopMetric{
		{Name: "mqtt_publisher", State: "processing", Iterations: 43},
	}, &errs)

	mqttMock.mu.Lock()
	sensorCountAfter := len(mqttMock.sensors)
	mqttMock.mu.Unlock()

	if sensorCountAfter != 4 { // should not grow
		t.Errorf("sensors after re-publish = %d, want 4 (no duplicates)", sensorCountAfter)
	}
}

func TestStaticSensors_Count(t *testing.T) {
	builder := testBuilder()
	sensors := builder.StaticSensors()

	// 4 DB + 3 tokens + 2 sessions + 4 request + 4 loops + 3 attachments = 20
	if len(sensors) != 20 {
		t.Errorf("StaticSensors count = %d, want 20", len(sensors))
	}

	// Verify each has required fields.
	for _, s := range sensors {
		if s.EntitySuffix == "" {
			t.Error("sensor has empty EntitySuffix")
		}
		if s.Config.UniqueID == "" {
			t.Error("sensor has empty UniqueID")
		}
		if s.Config.StateTopic == "" {
			t.Error("sensor has empty StateTopic")
		}
	}
}

func TestLoopSensors(t *testing.T) {
	builder := testBuilder()
	sensors := builder.LoopSensors("email_poll")

	if len(sensors) != 2 {
		t.Fatalf("LoopSensors count = %d, want 2", len(sensors))
	}

	if sensors[0].EntitySuffix != "loop_email_poll_state" {
		t.Errorf("sensor[0].EntitySuffix = %q", sensors[0].EntitySuffix)
	}
	if sensors[1].EntitySuffix != "loop_email_poll_iterations" {
		t.Errorf("sensor[1].EntitySuffix = %q", sensors[1].EntitySuffix)
	}
}
