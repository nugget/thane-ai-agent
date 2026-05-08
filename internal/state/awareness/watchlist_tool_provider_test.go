package awareness

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

func setupWatchlistProvider(t *testing.T) (*WatchlistTools, *WatchlistStore, *[]string) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	var registered []string
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:        store,
		TagRegistrar: func(tag string) { registered = append(registered, tag) },
	})
	return p, store, &registered
}

func TestWatchlistTools_NameAndToolList(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	if got := p.Name(); got != "awareness.watchlist" {
		t.Errorf("Name() = %q, want awareness.watchlist", got)
	}

	got := p.Tools()
	if len(got) != 3 {
		t.Fatalf("Tools() returned %d tools, want 3", len(got))
	}

	names := make([]string, 0, len(got))
	for _, tool := range got {
		names = append(names, tool.Name)
		if tool.Handler == nil {
			t.Errorf("tool %q has nil handler; provider contract requires non-nil", tool.Name)
		}
	}
	want := []string{"add_context_entity", "list_context_entities", "remove_context_entity"}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("tool names = %v, want %v", names, want)
	}
}

func TestWatchlistTools_RegisterProviderAddsThreeTools(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	reg := tools.NewEmptyRegistry()
	reg.RegisterProvider(p)

	for _, name := range []string{"add_context_entity", "list_context_entities", "remove_context_entity"} {
		if reg.Get(name) == nil {
			t.Errorf("%s should be registered", name)
		}
	}
}

func TestAddContextEntity_MissingEntityID(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddContextEntity(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing entity_id")
	}
	if !strings.Contains(err.Error(), "entity_id is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "entity_id is required")
	}
}

func TestAddContextEntity_Success(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	result, err := p.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.temperature",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "sensor.temperature") {
		t.Errorf("result = %q, want to contain entity_id", result)
	}
	if !strings.Contains(result, "watching") {
		t.Errorf("result = %q, want to contain 'watching'", result)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sensor.temperature" {
		t.Errorf("store.List() = %v, want [sensor.temperature]", ids)
	}
}

func TestAddContextEntity_WithScopesTTLAndHistory(t *testing.T) {
	p, store, registered := setupWatchlistProvider(t)

	result, err := p.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id":   "weather.home",
		"tags":        []any{"battery_focus", "battery_focus"},
		"history":     []any{60, 3600},
		"forecast":    "hourly",
		"ttl_seconds": 120,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "expires in 120s") {
		t.Fatalf("result = %q, want TTL text", result)
	}

	if !slices.Equal(*registered, []string{"battery_focus"}) {
		t.Fatalf("registered tags = %v, want [battery_focus]", *registered)
	}

	subs, err := store.ListByTag("battery_focus")
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("ListByTag len = %d, want 1", len(subs))
	}
	if !slices.Equal(subs[0].History, []int{60, 3600}) {
		t.Fatalf("history = %v, want [60 3600]", subs[0].History)
	}
	if subs[0].Forecast != "hourly" {
		t.Fatalf("forecast = %q, want hourly", subs[0].Forecast)
	}
	if subs[0].ExpiresAt == nil {
		t.Fatal("expected subscription expiration")
	}
}

func TestAddContextEntity_InvalidForecast(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id": "weather.home",
		"forecast":  "monthly",
	})
	if err == nil {
		t.Fatal("expected error for invalid forecast")
	}
	if !strings.Contains(err.Error(), "forecast must be one of") {
		t.Fatalf("error = %q, want forecast validation", err.Error())
	}
}

func TestAddContextEntity_ForecastRequiresWeatherEntity(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.outdoor_temperature",
		"forecast":  "daily",
	})
	if err == nil {
		t.Fatal("expected error for forecast on non-weather entity")
	}
	if !strings.Contains(err.Error(), "weather.*") {
		t.Fatalf("error = %q, want weather entity guidance", err.Error())
	}
}

func TestAddContextEntity_ForecastNoneClearsExistingOption(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if _, err := p.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id": "weather.home",
		"forecast":  "daily",
	}); err != nil {
		t.Fatalf("add forecast: %v", err)
	}
	result, err := p.handleAddContextEntity(context.Background(), map[string]any{
		"entity_id": "weather.home",
		"forecast":  "none",
	})
	if err != nil {
		t.Fatalf("clear forecast: %v", err)
	}
	if !strings.Contains(result, "forecast: none") {
		t.Fatalf("result = %q, want forecast clearing note", result)
	}

	subs, err := store.ListUntaggedSubscriptions()
	if err != nil {
		t.Fatalf("ListUntaggedSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("len(subs) = %d, want 1", len(subs))
	}
	if subs[0].Forecast != "" {
		t.Fatalf("forecast = %q, want cleared", subs[0].Forecast)
	}
}

func TestParseWatchlistTagArgs_IgnoresWhitespaceOnlyTags(t *testing.T) {
	tags, err := parseWatchlistTagArgs([]any{"battery_focus", "   ", "\t", " interactive "})
	if err != nil {
		t.Fatalf("parseWatchlistTagArgs: %v", err)
	}

	if !slices.Equal(tags, []string{"battery_focus", "interactive"}) {
		t.Fatalf("parseWatchlistTagArgs() = %v, want [battery_focus interactive]", tags)
	}
}

func TestListContextEntities_ReturnsScopedSubscriptions(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if err := store.Add("sensor.always_on"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.AddWithOptions("sensor.battery", []string{"battery_focus"}, []int{600}, 300, ""); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if err := store.AddWithOptions("weather.home", []string{"weather_focus"}, nil, 0, "daily"); err != nil {
		t.Fatalf("AddWithOptions weather: %v", err)
	}

	raw, err := p.handleListContextEntities(context.Background(), map[string]any{
		"tag": "weather_focus",
	})
	if err != nil {
		t.Fatalf("handleListContextEntities: %v", err)
	}

	var payload struct {
		Count int `json:"count"`
		Items []struct {
			EntityID      string `json:"entity_id"`
			Scope         string `json:"scope"`
			AlwaysVisible bool   `json:"always_visible"`
			Forecast      string `json:"forecast"`
			ExpiresDelta  string `json:"expires_delta"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Count != 1 {
		t.Fatalf("count = %d, want 1", payload.Count)
	}
	if payload.Items[0].EntityID != "weather.home" || payload.Items[0].Scope != "weather_focus" {
		t.Fatalf("item = %+v, want weather.home/weather_focus", payload.Items[0])
	}
	if payload.Items[0].AlwaysVisible {
		t.Fatal("tagged subscription should not be always visible")
	}
	if payload.Items[0].Forecast != "daily" {
		t.Fatalf("forecast = %q, want daily", payload.Items[0].Forecast)
	}

	raw, err = p.handleListContextEntities(context.Background(), map[string]any{
		"tag": "battery_focus",
	})
	if err != nil {
		t.Fatalf("handleListContextEntities battery_focus: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal battery payload: %v", err)
	}
	if payload.Count != 1 {
		t.Fatalf("battery count = %d, want 1", payload.Count)
	}
	if payload.Items[0].EntityID != "sensor.battery" || payload.Items[0].Scope != "battery_focus" {
		t.Fatalf("battery item = %+v, want sensor.battery/battery_focus", payload.Items[0])
	}
	if payload.Items[0].ExpiresDelta == "" {
		t.Fatal("expected expires_delta for TTL-backed subscription")
	}
}

func TestRemoveContextEntity_MissingEntityID(t *testing.T) {
	p, _, _ := setupWatchlistProvider(t)

	_, err := p.handleRemoveContextEntity(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing entity_id")
	}
	if !strings.Contains(err.Error(), "entity_id is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "entity_id is required")
	}
}

func TestRemoveContextEntity_Success(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if err := store.Add("sensor.temperature"); err != nil {
		t.Fatalf("add: %v", err)
	}

	result, err := p.handleRemoveContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.temperature",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "sensor.temperature") {
		t.Errorf("result = %q, want to contain entity_id", result)
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("store.List() = %v, want empty", ids)
	}
}

func TestRemoveContextEntity_ScopedRemovalKeepsOtherSubscriptions(t *testing.T) {
	p, store, _ := setupWatchlistProvider(t)

	if err := store.Add("sensor.battery"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.AddWithOptions("sensor.battery", []string{"battery_focus"}, nil, 0, ""); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	result, err := p.handleRemoveContextEntity(context.Background(), map[string]any{
		"entity_id": "sensor.battery",
		"tags":      []any{"battery_focus"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "scopes [battery_focus]") {
		t.Fatalf("result = %q, want scoped-removal text", result)
	}

	untagged, err := store.ListUntagged()
	if err != nil {
		t.Fatalf("ListUntagged: %v", err)
	}
	if !slices.Equal(untagged, []string{"sensor.battery"}) {
		t.Fatalf("ListUntagged() = %v, want [sensor.battery]", untagged)
	}

	tagged, err := store.ListByTag("battery_focus")
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}
	if len(tagged) != 0 {
		t.Fatalf("ListByTag(battery_focus) = %v, want empty", tagged)
	}
}
