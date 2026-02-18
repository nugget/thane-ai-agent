package watchlist

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	_ "modernc.org/sqlite"
)

// fakeHA implements StateGetter for testing.
type fakeHA struct {
	states map[string]*homeassistant.State
	err    error // returned for any entity not in states
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

func setupTestProvider(t *testing.T, ha StateGetter) (*Provider, *Store) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	p := NewProvider(store, ha, slog.Default())
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
	if !strings.Contains(got, "Office Temperature") {
		t.Error("missing friendly_name")
	}
	if !strings.Contains(got, "sensor.office_temperature") {
		t.Error("missing entity_id")
	}
	if !strings.Contains(got, "72.4 °F") {
		t.Error("missing state with unit")
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

	if !strings.Contains(got, "Temperature") {
		t.Error("missing first entity")
	}
	if !strings.Contains(got, "Front Door") {
		t.Error("missing second entity")
	}
	if !strings.Contains(got, "68 °F") {
		t.Error("missing temperature state with unit")
	}
	if !strings.Contains(got, "off") {
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

	// When no friendly_name, entity_id is used as the display name.
	// It should appear as both the name and the parenthetical ID.
	if !strings.Contains(got, "sensor.raw") {
		t.Error("missing entity_id fallback as display name")
	}
	if !strings.Contains(got, "42") {
		t.Error("missing state value")
	}
}
