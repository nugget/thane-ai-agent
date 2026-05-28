package awareness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// fakeTrendClient implements TrendHistoryClient for ha_history tests.
// It records the start time the tool requested so lookback clamping can
// be asserted, and serves separate lean / attributes-enabled history.
type fakeTrendClient struct {
	current     *homeassistant.State
	currentErr  error
	history     []homeassistant.State // GetStateHistory (lean)
	historyAttr []homeassistant.State // GetStateHistoryWithAttributes
	historyErr  error
	entities    []homeassistant.EntityInfo // GetEntities (suggestion fallback)

	gotStart time.Time
}

func (f *fakeTrendClient) GetState(_ context.Context, _ string) (*homeassistant.State, error) {
	return f.current, f.currentErr
}
func (f *fakeTrendClient) GetStateHistory(_ context.Context, _ string, start, _ time.Time) ([]homeassistant.State, error) {
	f.gotStart = start
	return f.history, f.historyErr
}
func (f *fakeTrendClient) GetStateHistoryWithAttributes(_ context.Context, _ string, start, _ time.Time) ([]homeassistant.State, error) {
	f.gotStart = start
	return f.historyAttr, f.historyErr
}
func (f *fakeTrendClient) GetEntities(_ context.Context, _ string) ([]homeassistant.EntityInfo, error) {
	return f.entities, nil
}

func histState(id, state string, attrs map[string]any, ago time.Duration) homeassistant.State {
	at := testNow.Add(-ago)
	return homeassistant.State{EntityID: id, State: state, Attributes: attrs, LastChanged: at, LastUpdated: at}
}

func histStateP(id, state string, attrs map[string]any, ago time.Duration) *homeassistant.State {
	s := histState(id, state, attrs, ago)
	return &s
}

func decodeTrend(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return payload
}

func TestComputeEntityTrend_Numeric(t *testing.T) {
	client := &fakeTrendClient{
		current: histStateP("sensor.office_temp", "72.5",
			map[string]any{"device_class": "temperature", "unit_of_measurement": "°F"}, 0),
		history: []homeassistant.State{
			histState("sensor.office_temp", "70.1", nil, 3*time.Hour),
			histState("sensor.office_temp", "71.4", nil, 2*time.Hour),
			histState("sensor.office_temp", "72.0", nil, 1*time.Hour),
		},
	}
	out, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "sensor.office_temp"}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	p := decodeTrend(t, out)
	if p["kind"] != "numeric" {
		t.Fatalf("kind = %#v, want numeric\n%s", p["kind"], out)
	}
	if p["trend"] != "rising" {
		t.Errorf("trend = %#v, want rising", p["trend"])
	}
	if p["entity_id"] != "sensor.office_temp" {
		t.Errorf("entity_id = %#v", p["entity_id"])
	}
	if p["unit"] != "°F" {
		t.Errorf("unit = %#v, want °F", p["unit"])
	}
	// Numeric summary values are rendered as formatted strings.
	if p["min_value"] != "70.1" {
		t.Errorf("min_value = %#v, want \"70.1\"", p["min_value"])
	}
	if p["max_value"] != "72.5" {
		t.Errorf("max_value = %#v, want \"72.5\" (current is the latest sample)", p["max_value"])
	}
}

func TestComputeEntityTrend_Discrete(t *testing.T) {
	client := &fakeTrendClient{
		current: histStateP("binary_sensor.front_door", "off", map[string]any{"device_class": "door"}, 0),
		history: []homeassistant.State{
			histState("binary_sensor.front_door", "off", nil, 3*time.Hour),
			histState("binary_sensor.front_door", "on", nil, 2*time.Hour),
			histState("binary_sensor.front_door", "off", nil, 1*time.Hour),
		},
	}
	out, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "binary_sensor.front_door"}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	p := decodeTrend(t, out)
	if p["kind"] != "discrete" {
		t.Fatalf("kind = %#v, want discrete\n%s", p["kind"], out)
	}
	cc, ok := p["change_count"].(float64)
	if !ok || cc < 1 {
		t.Errorf("change_count = %#v, want >= 1", p["change_count"])
	}
	if _, ok := p["recent_states"]; !ok {
		t.Errorf("expected recent_states in discrete summary:\n%s", out)
	}
}

func TestComputeEntityTrend_EmptyWindow(t *testing.T) {
	client := &fakeTrendClient{
		current: histStateP("sensor.office_temp", "72.5", nil, 0),
		history: nil, // recorder returned nothing in the window
	}
	out, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "sensor.office_temp"}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	p := decodeTrend(t, out)
	if p["available"] != false || p["reason"] != "no_history" {
		t.Errorf("expected available=false reason=no_history, got %#v", p)
	}
}

func TestComputeEntityTrend_NonRecorderEntity(t *testing.T) {
	// A non-recorded entity: current state resolves, but the recorder has
	// no history at all. Must surface the explicit no-history marker, not
	// an error or a bogus zero-change summary.
	client := &fakeTrendClient{
		current: histStateP("sensor.uptime_text", "online", nil, 0),
		history: []homeassistant.State{},
	}
	out, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "sensor.uptime_text"}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	p := decodeTrend(t, out)
	if p["reason"] != "no_history" {
		t.Errorf("reason = %#v, want no_history\n%s", p["reason"], out)
	}
}

func TestComputeEntityTrend_Attribute(t *testing.T) {
	client := &fakeTrendClient{
		current: histStateP("climate.office", "heat",
			map[string]any{"current_temperature": 72.5, "unit_of_measurement": "°F"}, 0),
		// Lean history must NOT be consulted for an attribute trend.
		history: []homeassistant.State{histState("climate.office", "heat", nil, 1*time.Hour)},
		historyAttr: []homeassistant.State{
			histState("climate.office", "heat", map[string]any{"current_temperature": 68.0}, 3*time.Hour),
			histState("climate.office", "heat", map[string]any{"current_temperature": 70.0}, 2*time.Hour),
			histState("climate.office", "heat", map[string]any{"current_temperature": 71.5}, 1*time.Hour),
		},
	}
	out, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "climate.office", Attribute: "current_temperature"}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	p := decodeTrend(t, out)
	if p["attribute"] != "current_temperature" {
		t.Errorf("attribute = %#v, want current_temperature", p["attribute"])
	}
	if p["kind"] != "numeric" {
		t.Fatalf("kind = %#v, want numeric (attribute is numeric)\n%s", p["kind"], out)
	}
	if p["trend"] != "rising" {
		t.Errorf("trend = %#v, want rising", p["trend"])
	}
	if p["min_value"] != "68" {
		t.Errorf("min_value = %#v, want \"68\" (from the attribute series)", p["min_value"])
	}
}

// TestComputeEntityTrend_AttributeAbsentOnCurrent is the #1018 regression:
// when the recorder has the attribute but the current state no longer
// exposes it (e.g. a climate entity turned off), the unprojected current
// state must NOT be folded into the projected numeric series — the trend
// stays numeric over the recorded attribute values rather than collapsing
// to a discrete summary.
func TestComputeEntityTrend_AttributeAbsentOnCurrent(t *testing.T) {
	client := &fakeTrendClient{
		// Current state dropped current_temperature (entity is "off").
		current: histStateP("climate.office", "off", map[string]any{"unit_of_measurement": "°F"}, 0),
		historyAttr: []homeassistant.State{
			histState("climate.office", "heat", map[string]any{"current_temperature": 68.0}, 3*time.Hour),
			histState("climate.office", "heat", map[string]any{"current_temperature": 70.0}, 2*time.Hour),
			histState("climate.office", "heat", map[string]any{"current_temperature": 71.5}, 1*time.Hour),
		},
	}
	out, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "climate.office", Attribute: "current_temperature"}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	p := decodeTrend(t, out)
	if p["kind"] != "numeric" {
		t.Fatalf("kind = %#v, want numeric (current lacks the attribute; must not poison to discrete)\n%s", p["kind"], out)
	}
	if p["max_value"] != "71.5" {
		t.Errorf("max_value = %#v, want \"71.5\" (the off-state current must not be appended)", p["max_value"])
	}
}

func TestComputeEntityTrend_LookbackClamped(t *testing.T) {
	client := &fakeTrendClient{
		current: histStateP("sensor.office_temp", "72.5", nil, 0),
	}
	_, err := ComputeEntityTrend(context.Background(), client,
		TrendRequest{EntityID: "sensor.office_temp", LookbackSeconds: 999999999}, testNow)
	if err != nil {
		t.Fatalf("ComputeEntityTrend: %v", err)
	}
	// The requested window must be clamped to maxTrendLookback before the
	// recorder query, not passed through verbatim.
	wantStart := testNow.Add(-maxTrendLookback * time.Second)
	if !client.gotStart.Equal(wantStart) {
		t.Errorf("history start = %v, want clamped %v", client.gotStart, wantStart)
	}
}

func TestComputeEntityTrend_RequiresEntity(t *testing.T) {
	client := &fakeTrendClient{}
	if _, err := ComputeEntityTrend(context.Background(), client, TrendRequest{}, testNow); err == nil {
		t.Error("expected error when entity_id is empty")
	}
}

func TestComputeEntityTrend_CurrentStateError(t *testing.T) {
	client := &fakeTrendClient{currentErr: errors.New("entity not found")}
	if _, err := ComputeEntityTrend(context.Background(), client, TrendRequest{EntityID: "sensor.ghost"}, testNow); err == nil {
		t.Error("expected error when current state cannot be fetched")
	}
}

func TestHandleHistory_UnknownEntitySuggests(t *testing.T) {
	client := &fakeTrendClient{
		currentErr: &homeassistant.APIError{StatusCode: 404},
		entities: []homeassistant.EntityInfo{
			{EntityID: "sensor.office_temperature", FriendlyName: "Office Temperature", Domain: "sensor"},
		},
	}
	tool := NewEntityTrendTools(EntityTrendToolsConfig{Client: client})

	out, err := tool.handleHistory(context.Background(), map[string]any{"entity_id": "sensor.office_temperatur"})
	if err != nil {
		t.Fatalf("handleHistory: %v", err)
	}
	payload := decodeTrend(t, out)
	if payload["found"] != false {
		t.Errorf("found = %v, want false", payload["found"])
	}
	if payload["reason"] != "not_found" {
		t.Errorf("reason = %v, want not_found", payload["reason"])
	}
}
