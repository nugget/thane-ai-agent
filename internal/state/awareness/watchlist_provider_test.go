package awareness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	_ "modernc.org/sqlite"
)

// fakeHA implements StateGetter for testing.
type fakeHA struct {
	states           map[string]*homeassistant.State
	history          map[string][]homeassistant.State
	forecasts        map[string][]map[string]any
	forecastRequests []string
	err              error // returned for any entity not in states
	histErr          error
	forecastErr      error
}

func (f *fakeHA) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	if s, ok := f.states[entityID]; ok {
		return s, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return nil, errors.New("entity not found")
}

func (f *fakeHA) GetStateHistory(_ context.Context, entityID string, _ time.Time, _ time.Time) ([]homeassistant.State, error) {
	if f.histErr != nil {
		return nil, f.histErr
	}
	history := f.history[entityID]
	return append([]homeassistant.State(nil), history...), nil
}

func (f *fakeHA) GetWeatherForecasts(_ context.Context, entityID, forecastType string) ([]map[string]any, error) {
	f.forecastRequests = append(f.forecastRequests, entityID+":"+forecastType)
	if f.forecastErr != nil {
		return nil, f.forecastErr
	}
	return append([]map[string]any(nil), f.forecasts[entityID]...), nil
}

func setupTestProvider(t *testing.T, ha StateGetter) (*WatchlistProvider, *WatchlistStore) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWatchlistStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	p := NewWatchlistProvider(store, ha, slog.Default())
	return p, store
}

func TestProvider_EmptyWatchlist(t *testing.T) {
	p, _ := setupTestProvider(t, &fakeHA{})

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for empty watchlist, got %q", got)
	}
}

func TestProvider_SingleEntity(t *testing.T) {
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"sensor.office_temperature": {
				EntityID:    "sensor.office_temperature",
				State:       "72.4",
				LastChanged: time.Date(2025, 1, 15, 16, 30, 0, 0, time.UTC),
				Attributes: map[string]any{
					"friendly_name":       "Office Temperature",
					"unit_of_measurement": "°F",
				},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.Add("sensor.office_temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if !strings.Contains(got, "### Watched Entities") {
		t.Error("missing header")
	}
	if !strings.Contains(got, `"name":"Office Temperature"`) {
		t.Error("missing friendly_name in JSON")
	}
	if !strings.Contains(got, `"entity":"sensor.office_temperature"`) {
		t.Error("missing entity_id in JSON")
	}
	if !strings.Contains(got, `"state":"72.4"`) {
		t.Error("missing state in JSON")
	}
	if !strings.Contains(got, `"unit":"°F"`) {
		t.Error("missing unit in JSON")
	}
}

func TestProvider_EntityFetchFailure(t *testing.T) {
	ha := &fakeHA{
		err: errors.New("connection refused"),
	}

	p, store := setupTestProvider(t, ha)
	if err := store.Add("sensor.broken"); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	// Fetch errors must use the same JSON availability schema as
	// sentinel-state entities so the model sees one stable shape
	// regardless of whether the failure was upstream or local.
	payload := decodeWatchlistPayload(t, got)
	if payload["entity"] != "sensor.broken" {
		t.Errorf("entity = %v, want sensor.broken", payload["entity"])
	}
	if payload["available"] != false {
		t.Errorf("available = %v, want false", payload["available"])
	}
	if payload["reason"] != "fetch_error" {
		t.Errorf("reason = %v, want fetch_error", payload["reason"])
	}
	if _, hasState := payload["state"]; hasState {
		t.Error("state field must be omitted on fetch_error so the model cannot misread a stale value")
	}
}

func TestProvider_MultipleEntities(t *testing.T) {
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"sensor.temperature": {
				EntityID:    "sensor.temperature",
				State:       "68",
				LastChanged: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
				Attributes: map[string]any{
					"friendly_name":       "Temperature",
					"unit_of_measurement": "°F",
				},
			},
			"binary_sensor.door": {
				EntityID:    "binary_sensor.door",
				State:       "off",
				LastChanged: time.Date(2025, 1, 15, 8, 0, 0, 0, time.UTC),
				Attributes: map[string]any{
					"friendly_name": "Front Door",
				},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.Add("sensor.temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.Add("binary_sensor.door"); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if !strings.Contains(got, `"name":"Temperature"`) {
		t.Error("missing first entity")
	}
	if !strings.Contains(got, `"name":"Front Door"`) {
		t.Error("missing second entity")
	}
	if !strings.Contains(got, `"state":"68"`) {
		t.Error("missing temperature state")
	}
	if !strings.Contains(got, `"state":"off"`) {
		t.Error("missing door state")
	}
}

func TestProvider_NoFriendlyName(t *testing.T) {
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"sensor.raw": {
				EntityID:    "sensor.raw",
				State:       "42",
				LastChanged: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
				Attributes:  map[string]any{},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.Add("sensor.raw"); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	// When no friendly_name, name is omitted from JSON.
	if !strings.Contains(got, `"entity":"sensor.raw"`) {
		t.Error("missing entity_id in JSON")
	}
	if !strings.Contains(got, `"state":"42"`) {
		t.Error("missing state value in JSON")
	}
	if strings.Contains(got, `"name"`) {
		t.Error("name should be omitted when no friendly_name")
	}
}

func TestProvider_IncludesNumericHistorySummaries(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"sensor.office_temperature": {
				EntityID:    "sensor.office_temperature",
				State:       "72.4",
				LastChanged: now,
				Attributes: map[string]any{
					"friendly_name":       "Office Temperature",
					"unit_of_measurement": "°F",
					"device_class":        "temperature",
				},
			},
		},
		history: map[string][]homeassistant.State{
			"sensor.office_temperature": {
				{EntityID: "sensor.office_temperature", State: "70.1", LastChanged: now.Add(-26 * time.Hour)},
				{EntityID: "sensor.office_temperature", State: "71.0", LastChanged: now.Add(-23 * time.Hour)},
				{EntityID: "sensor.office_temperature", State: "71.8", LastChanged: now.Add(-6 * time.Hour)},
				{EntityID: "sensor.office_temperature", State: "72.0", LastChanged: now.Add(-90 * time.Minute)},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.AddWithOptions("sensor.office_temperature", nil, []int{24 * 60 * 60}, 0, ""); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	payload := decodeWatchlistPayload(t, got)
	history, ok := payload["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history = %#v, want one summary", payload["history"])
	}
	summary, ok := history[0].(map[string]any)
	if !ok {
		t.Fatalf("history[0] = %#v, want object", history[0])
	}
	if summary["kind"] != "numeric" {
		t.Fatalf("kind = %#v, want numeric", summary["kind"])
	}
	if summary["lookback"] != "-86400s" {
		t.Fatalf("lookback = %#v, want -86400s", summary["lookback"])
	}
	if summary["trend"] != "rising" {
		t.Fatalf("trend = %#v, want rising", summary["trend"])
	}
	if summary["value_delta"] != "+2.3" {
		t.Fatalf("value_delta = %#v, want +2.3", summary["value_delta"])
	}
	if summary["sample_count"] != float64(5) {
		t.Fatalf("sample_count = %#v, want 5", summary["sample_count"])
	}
}

func TestProvider_IncludesWeatherForecastWhenSubscribed(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"weather.home": {
				EntityID:    "weather.home",
				State:       "cloudy",
				LastChanged: now.Add(-5 * time.Minute),
				Attributes: map[string]any{
					"friendly_name":      "Home Forecast",
					"temperature":        70.0,
					"temperature_unit":   "°F",
					"wind_speed_unit":    "mph",
					"precipitation_unit": "in",
				},
			},
		},
		forecasts: map[string][]map[string]any{
			"weather.home": {
				{
					"datetime":                  now.Add(2 * time.Hour).Format(time.RFC3339),
					"condition":                 "rainy",
					"temperature":               78.0,
					"templow":                   64.0,
					"precipitation_probability": 80.0,
					"wind_speed":                12.0,
				},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.AddWithOptions("weather.home", nil, nil, 0, "hourly"); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if len(ha.forecastRequests) != 1 || ha.forecastRequests[0] != "weather.home:hourly" {
		t.Fatalf("forecastRequests = %v, want [weather.home:hourly]", ha.forecastRequests)
	}
	payload := decodeWatchlistPayload(t, got)
	if payload["forecast_type"] != "hourly" {
		t.Fatalf("forecast_type = %#v, want hourly", payload["forecast_type"])
	}
	forecast, ok := payload["forecast"].([]any)
	if !ok || len(forecast) != 1 {
		t.Fatalf("forecast = %#v, want one entry", payload["forecast"])
	}
	entry, ok := forecast[0].(map[string]any)
	if !ok {
		t.Fatalf("forecast[0] = %#v, want object", forecast[0])
	}
	if entry["condition"] != "rainy" {
		t.Fatalf("condition = %#v, want rainy", entry["condition"])
	}
	if entry["high"] != "78 °F" {
		t.Fatalf("high = %#v, want 78 °F", entry["high"])
	}
	if entry["precipitation_probability"] != float64(80) {
		t.Fatalf("precipitation_probability = %#v, want 80", entry["precipitation_probability"])
	}
}

func TestProvider_ForecastFetchFailureSurfacesUnavailableMarker(t *testing.T) {
	// When the forecast fetch errors, the model must see an explicit
	// "asked but unavailable" marker — not silently fall back to current
	// conditions only. Pre-fix, the failure was logged operator-side and
	// the original state was returned, so the model couldn't tell the
	// difference between an unsubscribed entity and one whose forecast
	// just failed to load.
	now := time.Now().UTC().Round(time.Second)
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"weather.home": {
				EntityID:    "weather.home",
				State:       "cloudy",
				LastChanged: now.Add(-5 * time.Minute),
				Attributes: map[string]any{
					"friendly_name":    "Home Forecast",
					"temperature":      70.0,
					"temperature_unit": "°F",
				},
			},
		},
		forecastErr: errors.New("upstream 503"),
	}

	p, store := setupTestProvider(t, ha)
	if err := store.AddWithOptions("weather.home", nil, nil, 0, "daily"); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	if len(ha.forecastRequests) != 1 || ha.forecastRequests[0] != "weather.home:daily" {
		t.Fatalf("forecastRequests = %v, want [weather.home:daily]", ha.forecastRequests)
	}

	payload := decodeWatchlistPayload(t, got)
	if payload["forecast_type"] != "daily" {
		t.Errorf("forecast_type = %#v, want daily (the requested type must remain visible to the model)", payload["forecast_type"])
	}
	if payload["forecast_unavailable"] != true {
		t.Errorf("forecast_unavailable = %#v, want true", payload["forecast_unavailable"])
	}
	if _, present := payload["forecast"]; present {
		t.Errorf("forecast array should be absent on fetch failure, got %v", payload["forecast"])
	}
	// Sanity: current-conditions data still passes through.
	if payload["temperature"] != "70 °F" {
		t.Errorf("temperature = %#v, want 70 °F (current conditions should still render)", payload["temperature"])
	}
}

func TestProvider_IncludesDiscreteHistorySummaries(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"binary_sensor.front_door": {
				EntityID:    "binary_sensor.front_door",
				State:       "off",
				LastChanged: now,
				Attributes: map[string]any{
					"friendly_name": "Front Door",
				},
			},
		},
		history: map[string][]homeassistant.State{
			"binary_sensor.front_door": {
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: now.Add(-25 * time.Hour)},
				{EntityID: "binary_sensor.front_door", State: "on", LastChanged: now.Add(-20 * time.Hour)},
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: now.Add(-2 * time.Hour)},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.AddWithOptions("binary_sensor.front_door", nil, []int{24 * 60 * 60}, 0, ""); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	payload := decodeWatchlistPayload(t, got)
	history, ok := payload["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history = %#v, want one summary", payload["history"])
	}
	summary, ok := history[0].(map[string]any)
	if !ok {
		t.Fatalf("history[0] = %#v, want object", history[0])
	}
	if summary["kind"] != "discrete" {
		t.Fatalf("kind = %#v, want discrete", summary["kind"])
	}
	if summary["lookback"] != "-86400s" {
		t.Fatalf("lookback = %#v, want -86400s", summary["lookback"])
	}
	if summary["change_count"] != float64(2) {
		t.Fatalf("change_count = %#v, want 2", summary["change_count"])
	}
	if summary["end_state"] != "off" {
		t.Fatalf("end_state = %#v, want off", summary["end_state"])
	}
	if summary["recent_states_truncated"] != false {
		t.Fatalf("recent_states_truncated = %#v, want false", summary["recent_states_truncated"])
	}
}

func TestProvider_DiscreteHistoryUsesDeviceClassLabels(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"binary_sensor.front_door": {
				EntityID:    "binary_sensor.front_door",
				State:       "off",
				LastChanged: now,
				Attributes: map[string]any{
					"friendly_name": "Front Door",
					"device_class":  "door",
				},
			},
		},
		history: map[string][]homeassistant.State{
			"binary_sensor.front_door": {
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: now.Add(-25 * time.Hour)},
				{EntityID: "binary_sensor.front_door", State: "on", LastChanged: now.Add(-20 * time.Hour)},
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: now.Add(-2 * time.Hour)},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.AddWithOptions("binary_sensor.front_door", nil, []int{24 * 60 * 60}, 0, ""); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	payload := decodeWatchlistPayload(t, got)
	if payload["state"] != "closed" {
		t.Errorf("current state = %v, want closed", payload["state"])
	}

	history, ok := payload["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history = %#v, want one summary", payload["history"])
	}
	summary := history[0].(map[string]any)
	if summary["start_state"] != "closed" {
		t.Errorf("start_state = %v, want closed", summary["start_state"])
	}
	if summary["end_state"] != "closed" {
		t.Errorf("end_state = %v, want closed", summary["end_state"])
	}
	recent, ok := summary["recent_states"].([]any)
	if !ok {
		t.Fatalf("recent_states = %#v", summary["recent_states"])
	}
	want := []string{"closed", "open", "closed"}
	if len(recent) != len(want) {
		t.Fatalf("recent_states len = %d, want %d (%v)", len(recent), len(want), recent)
	}
	for i, w := range want {
		if recent[i] != w {
			t.Errorf("recent_states[%d] = %v, want %q", i, recent[i], w)
		}
	}
}

func decodeWatchlistPayload(t *testing.T, got string) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) < 3 {
		t.Fatalf("unexpected watchlist context: %q", got)
	}

	payloadLine := strings.TrimSpace(lines[len(lines)-1])
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadLine), &payload); err != nil {
		t.Fatalf("unmarshal payload %q: %v", payloadLine, err)
	}
	return payload
}
