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
	_ "modernc.org/sqlite"
)

// fakeHA implements StateGetter for testing.
type fakeHA struct {
	states  map[string]*homeassistant.State
	history map[string][]homeassistant.State
	err     error // returned for any entity not in states
	histErr error
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

	got, err := p.GetContext(context.Background(), "")
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

	got, err := p.GetContext(context.Background(), "")
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

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if !strings.Contains(got, "sensor.broken") {
		t.Error("missing entity_id for failed fetch")
	}
	if !strings.Contains(got, "unavailable") {
		t.Error("failed entity should show as unavailable")
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

	got, err := p.GetContext(context.Background(), "")
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

	got, err := p.GetContext(context.Background(), "")
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
	if err := store.AddWithOptions("sensor.office_temperature", nil, []int{24 * 60 * 60}, 0); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.GetContext(context.Background(), "")
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
	if err := store.AddWithOptions("binary_sensor.front_door", nil, []int{24 * 60 * 60}, 0); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	got, err := p.GetContext(context.Background(), "")
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
