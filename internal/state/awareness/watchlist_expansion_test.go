package awareness

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// expansionRegistryClient is a small HARegistryClient fixture whose
// entities populate a couple of areas, for exercising the author-time
// target-expansion preview on add_entity_subscription /
// list_entity_subscriptions.
func expansionRegistryClient() *fakeDeviceClient {
	return &fakeDeviceClient{
		areas: []homeassistant.Area{
			{AreaID: "office", Name: "Office"},
			{AreaID: "kitchen", Name: "Kitchen"},
			{AreaID: "entry", Name: "Entry"},
		},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "light.office_main", AreaID: "office"},
			{EntityID: "sensor.office_temp", AreaID: "office"},
			{EntityID: "switch.office_fan", AreaID: "office"},
			{EntityID: "light.kitchen_main", AreaID: "kitchen"},
			{EntityID: "binary_sensor.front_door", AreaID: "entry"},
			{EntityID: "binary_sensor.back_door", AreaID: "entry"},
		},
	}
}

func setupWatchlistProviderWithRegistry(t *testing.T, client HARegistryClient) (*WatchlistTools, *WatchlistStore) {
	t.Helper()
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	p := NewWatchlistTools(WatchlistToolsConfig{
		Store:    store,
		Registry: client,
	})
	return p, store
}

func TestAddEntitySubscription_AreaTargetPreviewsExpansion(t *testing.T) {
	p, _ := setupWatchlistProviderWithRegistry(t, expansionRegistryClient())

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "area:office",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Currently matches 3 entities") {
		t.Errorf("result = %q, want a 3-entity expansion count", result)
	}
	// The sample lists concrete members so a typo is obvious.
	for _, id := range []string{"light.office_main", "sensor.office_temp", "switch.office_fan"} {
		if !strings.Contains(result, id) {
			t.Errorf("result = %q, want it to sample %s", result, id)
		}
	}
}

func TestAddEntitySubscription_ZeroMemberTargetFlagged(t *testing.T) {
	p, store := setupWatchlistProviderWithRegistry(t, expansionRegistryClient())

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "area:atrium", // no such area — likely a typo
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "matches no entities right now") {
		t.Errorf("result = %q, want a zero-member flag", result)
	}
	// Flagged, not rejected — the subscription is still recorded so it can
	// pick up members later (the point of a registry-tracking target).
	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "area:atrium" {
		t.Errorf("store.List() = %v, want [area:atrium] (flag, don't reject)", ids)
	}
}

func TestAddEntitySubscription_GlobTargetPreviewsExpansion(t *testing.T) {
	p, _ := setupWatchlistProviderWithRegistry(t, expansionRegistryClient())

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "binary_sensor.*door*",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Currently matches 2 entities") {
		t.Errorf("result = %q, want a 2-entity glob expansion", result)
	}
	if !strings.Contains(result, "binary_sensor.back_door") || !strings.Contains(result, "binary_sensor.front_door") {
		t.Errorf("result = %q, want both door sensors in the sample", result)
	}
}

func TestAddEntitySubscription_ConcreteEntityHasNoExpansion(t *testing.T) {
	p, _ := setupWatchlistProviderWithRegistry(t, expansionRegistryClient())

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "light.office_main",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "Currently matches") || strings.Contains(result, "matches no entities") {
		t.Errorf("result = %q, want no expansion clause for a concrete entity", result)
	}
}

func TestAddEntitySubscription_NoRegistryClientNoPreview(t *testing.T) {
	// Store-only provider (no Registry): a glob target still subscribes
	// cleanly, just without an expansion preview.
	p, store, _ := setupWatchlistProvider(t)

	result, err := p.handleAddEntitySubscription(context.Background(), map[string]any{
		"entity_id": "binary_sensor.*door*",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "Currently matches") {
		t.Errorf("result = %q, want no expansion clause without a registry client", result)
	}
	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "binary_sensor.*door*" {
		t.Errorf("store.List() = %v, want the glob subscription recorded", ids)
	}
}

func TestListEntitySubscriptions_IncludesExpansion(t *testing.T) {
	p, _ := setupWatchlistProviderWithRegistry(t, expansionRegistryClient())
	ctx := context.Background()

	for _, id := range []string{"area:office", "area:atrium", "sensor.office_temp"} {
		if _, err := p.handleAddEntitySubscription(ctx, map[string]any{"entity_id": id}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}

	result, err := p.handleListEntitySubscriptions(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got struct {
		Items []struct {
			EntityID  string `json:"entity_id"`
			Expansion *struct {
				Count  int      `json:"count"`
				Sample []string `json:"sample"`
				Note   string   `json:"note"`
			} `json:"expansion"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, result)
	}

	byID := make(map[string]int, len(got.Items))
	for i, item := range got.Items {
		byID[item.EntityID] = i
	}

	office := got.Items[byID["area:office"]]
	if office.Expansion == nil || office.Expansion.Count != 3 {
		t.Errorf("area:office expansion = %#v, want count 3", office.Expansion)
	}
	if office.Expansion != nil && len(office.Expansion.Sample) == 0 {
		t.Errorf("area:office expansion should carry a sample: %#v", office.Expansion)
	}

	atrium := got.Items[byID["area:atrium"]]
	if atrium.Expansion == nil || atrium.Expansion.Count != 0 || atrium.Expansion.Note == "" {
		t.Errorf("area:atrium expansion = %#v, want count 0 with a note", atrium.Expansion)
	}

	// A concrete entity carries no expansion — it is its own membership.
	concrete := got.Items[byID["sensor.office_temp"]]
	if concrete.Expansion != nil {
		t.Errorf("concrete entity should have no expansion: %#v", concrete.Expansion)
	}
}
