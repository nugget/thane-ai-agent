package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

type fakeHAServer struct {
	t        *testing.T
	server   *httptest.Server
	upgrader websocket.Upgrader

	mu           sync.Mutex
	states       []homeassistant.State
	configs      map[string]map[string]any
	areas        []map[string]any
	labels       []map[string]any
	categories   map[string][]map[string]any
	devices      []map[string]any
	entityRows   []map[string]any
	entityByID   map[string]map[string]any
	logbook      []map[string]any
	serviceCalls []string
	validations  map[string]homeassistant.ConfigValidationResult
}

func newFakeHAServer(t *testing.T) *fakeHAServer {
	t.Helper()

	f := &fakeHAServer{
		t:          t,
		upgrader:   websocket.Upgrader{},
		configs:    make(map[string]map[string]any),
		categories: make(map[string][]map[string]any),
		entityByID: make(map[string]map[string]any),
		validations: map[string]homeassistant.ConfigValidationResult{
			"triggers":   {Valid: true},
			"conditions": {Valid: true},
			"actions":    {Valid: true},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/websocket", f.handleWebSocket)
	mux.HandleFunc("/api/states", f.handleStates)
	mux.HandleFunc("/api/states/", f.handleState)
	mux.HandleFunc("/api/config/automation/config/", f.handleAutomationConfig)
	mux.HandleFunc("/api/services/", f.handleServiceCall)

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeHAServer) registry(t *testing.T) *Registry {
	t.Helper()

	client := homeassistant.NewClient(f.server.URL, "test-token", nil)
	ws := homeassistant.NewWSClient(f.server.URL, "test-token", nil)
	client.UseWSClient(ws)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("connect websocket: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	return NewRegistry(client, nil)
}

func (f *fakeHAServer) handleStates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(f.t, w, f.states)
}

func (f *fakeHAServer) handleState(w http.ResponseWriter, r *http.Request) {
	entityID := strings.TrimPrefix(r.URL.Path, "/api/states/")
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, state := range f.states {
		if state.EntityID == entityID {
			writeJSON(f.t, w, state)
			return
		}
	}
	http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
}

func (f *fakeHAServer) handleAutomationConfig(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/config/automation/config/")

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		cfg, ok := f.configs[id]
		if !ok {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(f.t, w, cfg)
	case http.MethodPost:
		var cfg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			f.t.Fatalf("decode config: %v", err)
		}
		f.configs[id] = cfg
		f.ensureAutomationStateLocked(id, cfg)
		w.WriteHeader(http.StatusOK)
		writeJSON(f.t, w, map[string]any{"result": "ok"})
	case http.MethodDelete:
		delete(f.configs, id)
		w.WriteHeader(http.StatusOK)
		writeJSON(f.t, w, map[string]any{"result": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *fakeHAServer) handleServiceCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		f.t.Fatalf("decode service payload: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.serviceCalls = append(f.serviceCalls, strings.TrimPrefix(r.URL.Path, "/api/services/"))
	entityID, _ := payload["entity_id"].(string)
	switch {
	case strings.HasSuffix(r.URL.Path, "/turn_off"):
		f.setAutomationStateLocked(entityID, "off")
	case strings.HasSuffix(r.URL.Path, "/turn_on"):
		f.setAutomationStateLocked(entityID, "on")
	}

	w.WriteHeader(http.StatusOK)
	writeJSON(f.t, w, map[string]any{"result": "ok"})
}

func (f *fakeHAServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		f.t.Fatalf("upgrade websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "auth_required"}); err != nil {
		f.t.Fatalf("write auth_required: %v", err)
	}

	var auth map[string]any
	if err := conn.ReadJSON(&auth); err != nil {
		f.t.Fatalf("read auth: %v", err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "auth_ok"}); err != nil {
		f.t.Fatalf("write auth_ok: %v", err)
	}

	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}

		id, _ := msg["id"].(float64)
		msgType, _ := msg["type"].(string)
		result, ok := f.wsResult(msgType, msg)
		if !ok {
			_ = conn.WriteJSON(map[string]any{
				"id":      id,
				"type":    "result",
				"success": false,
				"error": map[string]any{
					"code":    "not_found",
					"message": "not found",
				},
			})
			continue
		}

		if err := conn.WriteJSON(map[string]any{
			"id":      id,
			"type":    "result",
			"success": true,
			"result":  result,
		}); err != nil {
			return
		}
	}
}

func (f *fakeHAServer) wsResult(msgType string, msg map[string]any) (any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch msgType {
	case "validate_config":
		return f.validations, true
	case "config/area_registry/list":
		return f.areas, true
	case "config/label_registry/list":
		return f.labels, true
	case "config/category_registry/list":
		scope, _ := msg["scope"].(string)
		return f.categories[scope], true
	case "config/device_registry/list":
		return f.devices, true
	case "config/entity_registry/list":
		return f.entityRows, true
	case "config/entity_registry/get":
		entityID, _ := msg["entity_id"].(string)
		row, ok := f.entityByID[entityID]
		return row, ok
	case "config/entity_registry/update":
		entityID, _ := msg["entity_id"].(string)
		row, ok := f.entityByID[entityID]
		if !ok {
			return nil, false
		}
		updated := cloneMap(row)
		for key, value := range msg {
			if key == "id" || key == "type" || key == "entity_id" {
				continue
			}
			updated[key] = value
		}
		if newEntityID, ok := msg["new_entity_id"].(string); ok && newEntityID != "" {
			updated["entity_id"] = newEntityID
			for i, state := range f.states {
				if state.EntityID == entityID {
					f.states[i].EntityID = newEntityID
				}
			}
			delete(f.entityByID, entityID)
			entityID = newEntityID
		}
		f.entityByID[entityID] = updated
		for i, row := range f.entityRows {
			if row["entity_id"] == msg["entity_id"] {
				f.entityRows[i] = updated
			}
		}
		return updated, true
	case "automation/config":
		entityID, _ := msg["entity_id"].(string)
		for _, state := range f.states {
			if state.EntityID != entityID {
				continue
			}
			id, _ := state.Attributes["id"].(string)
			cfg, ok := f.configs[id]
			if !ok {
				return nil, false
			}
			return map[string]any{"config": cfg}, true
		}
		return nil, false
	case "logbook/get_events":
		entityIDs := stringSliceFromAny(msg["entity_ids"])
		if len(entityIDs) == 0 {
			return f.logbook, true
		}
		allowed := make(map[string]struct{}, len(entityIDs))
		for _, entityID := range entityIDs {
			allowed[entityID] = struct{}{}
		}
		filtered := make([]map[string]any, 0, len(f.logbook))
		for _, event := range f.logbook {
			entityID, _ := event["entity_id"].(string)
			if _, ok := allowed[entityID]; !ok {
				continue
			}
			filtered = append(filtered, cloneMap(event))
		}
		return filtered, true
	default:
		return nil, false
	}
}

func TestHAAutomationCreateValidateOnly(t *testing.T) {
	fake := newFakeHAServer(t)
	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_automation_create", `{
		"config": {
			"alias": "Door Lock Battery Level Critical",
			"triggers": [{"trigger":"numeric_state","entity_id":"sensor.frontdoor_battery_level","below":35}],
			"actions": [{"action":"mqtt.publish","data":{"topic":"thane/test"}}]
		},
		"metadata": {
			"area_id": "area_entry",
			"label_ids": ["label_critical"],
			"category_id": "maintenance"
		},
		"validate_only": true
	}`)
	if err != nil {
		t.Fatalf("ha_automation_create validate_only failed: %v", err)
	}

	var got struct {
		ID         string                                          `json:"id"`
		Metadata   map[string]any                                  `json:"metadata"`
		Validation map[string]homeassistant.ConfigValidationResult `json:"validation"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if got.ID != "door-lock-battery-level-critical" {
		t.Fatalf("id = %q, want %q", got.ID, "door-lock-battery-level-critical")
	}
	if labels, ok := got.Metadata["labels"].([]any); !ok || len(labels) != 1 || labels[0] != "label_critical" {
		t.Fatalf("metadata.labels = %#v, want [label_critical]", got.Metadata["labels"])
	}
	categories, ok := got.Metadata["categories"].(map[string]any)
	if !ok || categories["automation"] != "maintenance" {
		t.Fatalf("metadata.categories = %#v, want automation=maintenance", got.Metadata["categories"])
	}
	if !got.Validation["triggers"].Valid || !got.Validation["actions"].Valid {
		t.Fatalf("validation = %#v, want valid trigger/action results", got.Validation)
	}
}

func TestHAAutomationGetIncludesRegistryMetadata(t *testing.T) {
	fake := newFakeHAServer(t)
	lastTriggered := time.Now().Add(-90 * time.Second).UTC().Format(time.RFC3339)
	fake.states = []homeassistant.State{
		{
			EntityID: "automation.door_lock_battery_level_critical",
			State:    "on",
			Attributes: map[string]any{
				"id":             "door-lock-battery-level-critical",
				"friendly_name":  "Door Lock Battery Level Critical",
				"last_triggered": lastTriggered,
			},
		},
	}
	fake.configs["door-lock-battery-level-critical"] = map[string]any{
		"alias":       "Door Lock Battery Level Critical",
		"description": "Fire if any of the door deadbolt batteries is below 35%",
		"triggers":    []any{map[string]any{"trigger": "numeric_state"}},
		"actions":     []any{map[string]any{"action": "mqtt.publish"}},
	}
	fake.areas = []map[string]any{
		{"area_id": "area_entry", "name": "Garage Entry", "aliases": []string{}, "labels": []string{}},
	}
	fake.labels = []map[string]any{
		{"label_id": "label_critical", "name": "Critical", "icon": "mdi:alert", "color": "red"},
	}
	fake.categories["automation"] = []map[string]any{
		{"category_id": "cat_maintenance", "name": "Maintenance", "icon": "mdi:wrench"},
	}
	entry := map[string]any{
		"id":              "entity_row_1",
		"entity_id":       "automation.door_lock_battery_level_critical",
		"name":            "Door Lock Battery Level Critical",
		"area_id":         "area_entry",
		"labels":          []string{"label_critical"},
		"icon":            "mdi:battery-alert",
		"aliases":         []string{"door battery critical"},
		"categories":      map[string]any{"automation": "cat_maintenance"},
		"has_entity_name": true,
	}
	fake.entityRows = []map[string]any{entry}
	fake.entityByID["automation.door_lock_battery_level_critical"] = entry

	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_automation_get", `{"id":"door-lock-battery-level-critical"}`)
	if err != nil {
		t.Fatalf("ha_automation_get failed: %v", err)
	}

	var got haAutomationView
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if got.Alias != "Door Lock Battery Level Critical" {
		t.Fatalf("alias = %q", got.Alias)
	}
	if got.Metadata == nil {
		t.Fatal("metadata is nil")
	}
	if got.Metadata.AreaName != "Garage Entry" {
		t.Fatalf("area_name = %q, want %q", got.Metadata.AreaName, "Garage Entry")
	}
	if len(got.Metadata.Labels) != 1 || got.Metadata.Labels[0].Name != "Critical" {
		t.Fatalf("labels = %#v, want resolved Critical label", got.Metadata.Labels)
	}
	if got.Metadata.CategoryID != "cat_maintenance" {
		t.Fatalf("category_id = %q, want %q", got.Metadata.CategoryID, "cat_maintenance")
	}
	if got.Metadata.Category != "Maintenance" {
		t.Fatalf("category = %q, want %q", got.Metadata.Category, "Maintenance")
	}
	if got.Metadata.Categories["automation"] != "Maintenance" {
		t.Fatalf("categories = %#v, want automation=Maintenance", got.Metadata.Categories)
	}
	if got.Metadata.CategoryIDs["automation"] != "cat_maintenance" {
		t.Fatalf("category_ids = %#v, want automation=cat_maintenance", got.Metadata.CategoryIDs)
	}
	if got.Config["description"] != "Fire if any of the door deadbolt batteries is below 35%" {
		t.Fatalf("config.description = %#v", got.Config["description"])
	}
	if !strings.HasPrefix(got.LastTriggered, "-") || !strings.HasSuffix(got.LastTriggered, "s") {
		t.Fatalf("last_triggered = %q, want exact-second delta format", got.LastTriggered)
	}
}

func TestHARegistrySearchFindsEntityAndDevice(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{
			EntityID: "sensor.frontdoor_battery_level",
			State:    "28",
			Attributes: map[string]any{
				"friendly_name": "Front Door Battery Level",
			},
		},
	}
	fake.areas = []map[string]any{
		{"area_id": "area_entry", "name": "Front Door", "aliases": []string{"Entry Door"}, "labels": []string{}},
	}
	fake.labels = []map[string]any{
		{"label_id": "label_battery", "name": "Battery Watch"},
	}
	fake.devices = []map[string]any{
		{
			"id":                "device_lock_front",
			"name_by_user":      "Front Door Deadbolt",
			"manufacturer":      "Schlage",
			"model":             "Encode Plus",
			"area_id":           "area_entry",
			"labels":            []string{"label_battery"},
			"configuration_url": "https://example.invalid/device",
		},
	}
	entity := map[string]any{
		"id":              "entity_sensor_frontdoor_battery",
		"entity_id":       "sensor.frontdoor_battery_level",
		"name":            "Battery Level",
		"original_name":   "Front Door Battery Level",
		"area_id":         "area_entry",
		"device_id":       "device_lock_front",
		"labels":          []string{"label_battery"},
		"platform":        "zwave_js",
		"entity_category": "diagnostic",
	}
	fake.entityRows = []map[string]any{entity}
	fake.entityByID["sensor.frontdoor_battery_level"] = entity

	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_registry_search", `{"query":"front door battery","limit":5}`)
	if err != nil {
		t.Fatalf("ha_registry_search failed: %v", err)
	}

	var got haRegistrySearchResult
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(got.Entities) == 0 || got.Entities[0].EntityID != "sensor.frontdoor_battery_level" {
		t.Fatalf("entities = %#v, want sensor.frontdoor_battery_level", got.Entities)
	}
	if len(got.Devices) == 0 || got.Devices[0].DeviceID != "device_lock_front" {
		t.Fatalf("devices = %#v, want device_lock_front", got.Devices)
	}
}

func TestHAAutomationListIncludesActivitySummary(t *testing.T) {
	fake := newFakeHAServer(t)
	now := time.Now().UTC()
	fake.states = []homeassistant.State{
		{
			EntityID: "automation.low_battery_watch",
			State:    "on",
			Attributes: map[string]any{
				"id":            "low-battery-watch",
				"friendly_name": "Low Battery Watch",
			},
		},
		{
			EntityID: "automation.unused_automation",
			State:    "on",
			Attributes: map[string]any{
				"id":            "unused-automation",
				"friendly_name": "Unused Automation",
			},
		},
	}
	fake.logbook = []map[string]any{
		{
			"when":      float64(now.Add(-15 * time.Minute).Unix()),
			"name":      "Low Battery Watch",
			"message":   "triggered by numeric state of sensor.frontdoor_battery_level",
			"entity_id": "automation.low_battery_watch",
			"domain":    "automation",
		},
		{
			"when":      float64(now.Add(-2 * time.Hour).Unix()),
			"name":      "Low Battery Watch",
			"message":   "triggered",
			"entity_id": "automation.low_battery_watch",
			"domain":    "automation",
		},
		{
			"when":      float64(now.Add(-26 * time.Hour).Unix()),
			"name":      "Low Battery Watch",
			"message":   "triggered",
			"entity_id": "automation.low_battery_watch",
			"domain":    "automation",
		},
		{
			"when":      float64(now.Add(-8 * 24 * time.Hour).Unix()),
			"name":      "Low Battery Watch",
			"message":   "triggered",
			"entity_id": "automation.low_battery_watch",
			"domain":    "automation",
		},
		{
			"when":      float64(now.Add(-10 * time.Minute).Unix()),
			"name":      "Low Battery Watch",
			"message":   "turned off",
			"entity_id": "automation.low_battery_watch",
			"domain":    "automation",
		},
	}

	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_automation_list", `{"limit":10}`)
	if err != nil {
		t.Fatalf("ha_automation_list failed: %v", err)
	}

	var got []haAutomationView
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	byEntity := make(map[string]haAutomationView, len(got))
	for _, view := range got {
		byEntity[view.EntityID] = view
	}

	watch := byEntity["automation.low_battery_watch"]
	if watch.Activity == nil {
		t.Fatal("low battery watch activity is nil")
	}
	if watch.Activity.Activations1h != 1 {
		t.Fatalf("activations_1h = %d, want 1", watch.Activity.Activations1h)
	}
	if watch.Activity.Activations24h != 2 {
		t.Fatalf("activations_24h = %d, want 2", watch.Activity.Activations24h)
	}
	if watch.Activity.Activations7d != 3 {
		t.Fatalf("activations_7d = %d, want 3", watch.Activity.Activations7d)
	}
	if watch.Activity.ActivationRate7dPerDay != 0.43 {
		t.Fatalf("activation_rate_7d_per_day = %v, want 0.43", watch.Activity.ActivationRate7dPerDay)
	}
	if len(watch.Activity.RecentActivations) != 3 {
		t.Fatalf("recent_activations = %#v, want 3 entries", watch.Activity.RecentActivations)
	}
	if !strings.HasPrefix(watch.Activity.RecentActivations[0], "-") || !strings.HasSuffix(watch.Activity.RecentActivations[0], "s") {
		t.Fatalf("recent_activations[0] = %q, want delta-only format", watch.Activity.RecentActivations[0])
	}

	unused := byEntity["automation.unused_automation"]
	if unused.Activity == nil {
		t.Fatal("unused automation activity is nil")
	}
	if unused.Activity.Activations1h != 0 || unused.Activity.Activations24h != 0 || unused.Activity.Activations7d != 0 {
		t.Fatalf("unused activity = %#v, want zero counts", unused.Activity)
	}
	if len(unused.Activity.RecentActivations) != 0 {
		t.Fatalf("unused recent_activations = %#v, want empty", unused.Activity.RecentActivations)
	}
}

func TestHAAutomationListResolvesCategoryNames(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{
			EntityID: "automation.front_door_watch",
			State:    "on",
			Attributes: map[string]any{
				"id":            "front-door-watch",
				"friendly_name": "Front Door Watch",
			},
		},
	}
	fake.areas = []map[string]any{
		{"area_id": "entry", "name": "Entry"},
	}
	fake.categories["automation"] = []map[string]any{
		{"category_id": "01JSPY2KHMDFXMSDFXJNKZWX2V", "name": "Physical"},
	}
	row := map[string]any{
		"id":         "front_door_watch_row",
		"entity_id":  "automation.front_door_watch",
		"name":       "Front Door Watch",
		"area_id":    "entry",
		"labels":     []string{},
		"aliases":    []string{},
		"categories": map[string]any{"automation": "01JSPY2KHMDFXMSDFXJNKZWX2V"},
	}
	fake.entityRows = []map[string]any{row}
	fake.entityByID["automation.front_door_watch"] = row

	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_automation_list", `{"query":"physical","limit":10}`)
	if err != nil {
		t.Fatalf("ha_automation_list failed: %v", err)
	}

	var got []haAutomationView
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d automations, want 1", len(got))
	}
	if got[0].Metadata == nil {
		t.Fatal("metadata is nil")
	}
	if got[0].Metadata.CategoryID != "01JSPY2KHMDFXMSDFXJNKZWX2V" {
		t.Fatalf("category_id = %q, want raw category ID", got[0].Metadata.CategoryID)
	}
	if got[0].Metadata.Category != "Physical" {
		t.Fatalf("category = %q, want %q", got[0].Metadata.Category, "Physical")
	}
	if got[0].Metadata.Categories["automation"] != "Physical" {
		t.Fatalf("categories = %#v, want automation=Physical", got[0].Metadata.Categories)
	}
	if got[0].Metadata.CategoryIDs["automation"] != "01JSPY2KHMDFXMSDFXJNKZWX2V" {
		t.Fatalf("category_ids = %#v, want raw automation category ID", got[0].Metadata.CategoryIDs)
	}
}

func TestBuildEntityRegistryUpdateNormalizesMetadataNamesToIDs(t *testing.T) {
	update, err := buildEntityRegistryUpdate(map[string]any{
		"area_name": "Garage Entry",
		"labels": []any{
			map[string]any{"id": "label_critical", "name": "Critical"},
			"Battery Watch",
		},
		"categories": map[string]any{
			"automation": "Maintenance",
		},
	}, haMetadataMaps{
		areas: map[string]string{
			"area_entry": "Garage Entry",
		},
		labels: map[string]string{
			"label_critical": "Critical",
			"label_battery":  "Battery Watch",
		},
		categories: map[string]map[string]string{
			"automation": {
				"cat_maintenance": "Maintenance",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildEntityRegistryUpdate failed: %v", err)
	}

	if update["area_id"] != "area_entry" {
		t.Fatalf("area_id = %#v, want %q", update["area_id"], "area_entry")
	}
	labels, ok := update["labels"].([]string)
	if !ok {
		t.Fatalf("labels update = %#v, want []string", update["labels"])
	}
	if len(labels) != 2 || labels[0] != "label_critical" || labels[1] != "label_battery" {
		t.Fatalf("labels = %#v, want [label_critical label_battery]", labels)
	}
	categories, ok := update["categories"].(map[string]any)
	if !ok {
		t.Fatalf("categories update = %#v, want map", update["categories"])
	}
	if categories["automation"] != "cat_maintenance" {
		t.Fatalf("categories.automation = %#v, want %q", categories["automation"], "cat_maintenance")
	}
	if _, ok := update["area_name"]; ok {
		t.Fatalf("area_name leaked into update payload: %#v", update)
	}
	if _, ok := update["category"]; ok {
		t.Fatalf("category leaked into update payload: %#v", update)
	}
	if _, ok := update["category_ids"]; ok {
		t.Fatalf("category_ids leaked into update payload: %#v", update)
	}
	if _, ok := update["labels"].([]any); ok {
		t.Fatalf("labels leaked in unnormalized form: %#v", update["labels"])
	}
}

func TestBuildEntityRegistryUpdateRejectsAmbiguousMetadataNames(t *testing.T) {
	_, err := buildEntityRegistryUpdate(map[string]any{
		"label_ids": []any{"Critical"},
	}, haMetadataMaps{
		labels: map[string]string{
			"label_a": "Critical",
			"label_b": "Critical",
		},
	})
	if err == nil {
		t.Fatal("expected ambiguous label name error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want ambiguous match error", err)
	}
}

func TestHAAutomationCreateUpdateDeleteOperationalFlow(t *testing.T) {
	fake := newFakeHAServer(t)
	reg := fake.registry(t)

	createResult, err := reg.Execute(context.Background(), "ha_automation_create", `{
		"id": "battery-watch",
		"config": {
			"alias": "Battery Watch",
			"triggers": [{"trigger":"numeric_state","entity_id":"sensor.frontdoor_battery_level","below":30}],
			"actions": [{"action":"mqtt.publish","data":{"topic":"thane/test"}}]
		},
		"metadata": {
			"area_id": "garage",
			"label_ids": ["critical"],
			"entity_id": "automation.battery_watch_custom"
		},
		"enabled": false
	}`)
	if err != nil {
		t.Fatalf("ha_automation_create failed: %v", err)
	}

	var created haAutomationView
	if err := json.Unmarshal([]byte(createResult), &created); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	if created.EntityID != "automation.battery_watch_custom" {
		t.Fatalf("created entity_id = %q, want automation.battery_watch_custom", created.EntityID)
	}
	if created.State != "off" || created.Enabled {
		t.Fatalf("created state/enabled = %q/%v, want off/false", created.State, created.Enabled)
	}
	if created.Metadata == nil || created.Metadata.AreaID != "garage" {
		t.Fatalf("created metadata = %#v, want area_id garage", created.Metadata)
	}

	updateResult, err := reg.Execute(context.Background(), "ha_automation_update", `{
		"id": "battery-watch",
		"config": {
			"description": "Updated description"
		},
		"metadata": {
			"entity_id": "automation.battery_watch_renamed"
		},
		"enabled": true
	}`)
	if err != nil {
		t.Fatalf("ha_automation_update failed: %v", err)
	}

	var updated haAutomationView
	if err := json.Unmarshal([]byte(updateResult), &updated); err != nil {
		t.Fatalf("unmarshal update result: %v", err)
	}
	if updated.EntityID != "automation.battery_watch_renamed" {
		t.Fatalf("updated entity_id = %q, want automation.battery_watch_renamed", updated.EntityID)
	}
	if updated.Config["description"] != "Updated description" {
		t.Fatalf("updated description = %#v, want Updated description", updated.Config["description"])
	}
	if updated.State != "on" || !updated.Enabled {
		t.Fatalf("updated state/enabled = %q/%v, want on/true", updated.State, updated.Enabled)
	}

	deleteResult, err := reg.Execute(context.Background(), "ha_automation_delete", `{"id":"battery-watch"}`)
	if err != nil {
		t.Fatalf("ha_automation_delete failed: %v", err)
	}

	var deleted map[string]any
	if err := json.Unmarshal([]byte(deleteResult), &deleted); err != nil {
		t.Fatalf("unmarshal delete result: %v", err)
	}
	if deleted["deleted"] != true {
		t.Fatalf("delete result = %#v, want deleted=true", deleted)
	}
	if _, ok := fake.configs["battery-watch"]; ok {
		t.Fatal("expected automation config to be deleted")
	}
}

func TestHAAutomationListFiltersByAreaAndDisabled(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{
			EntityID: "automation.garage_watch",
			State:    "on",
			Attributes: map[string]any{
				"id":            "garage-watch",
				"friendly_name": "Garage Watch",
			},
		},
		{
			EntityID: "automation.kitchen_watch",
			State:    "on",
			Attributes: map[string]any{
				"id":            "kitchen-watch",
				"friendly_name": "Kitchen Watch",
			},
		},
	}
	fake.areas = []map[string]any{
		{"area_id": "garage", "name": "Garage"},
		{"area_id": "kitchen", "name": "Kitchen"},
	}
	fake.entityRows = []map[string]any{
		{
			"id":          "garage_row",
			"entity_id":   "automation.garage_watch",
			"name":        "Garage Watch",
			"area_id":     "garage",
			"labels":      []string{},
			"aliases":     []string{},
			"categories":  map[string]any{},
			"disabled_by": "",
		},
		{
			"id":          "kitchen_row",
			"entity_id":   "automation.kitchen_watch",
			"name":        "Kitchen Watch",
			"area_id":     "kitchen",
			"labels":      []string{},
			"aliases":     []string{},
			"categories":  map[string]any{},
			"disabled_by": "user",
		},
	}
	fake.entityByID["automation.garage_watch"] = fake.entityRows[0]
	fake.entityByID["automation.kitchen_watch"] = fake.entityRows[1]

	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_automation_list", `{"area_id":"garage","include_disabled":false}`)
	if err != nil {
		t.Fatalf("ha_automation_list failed: %v", err)
	}

	var got []haAutomationView
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got) != 1 || got[0].EntityID != "automation.garage_watch" {
		t.Fatalf("filtered list = %#v, want only automation.garage_watch", got)
	}
}

func TestToIndentedJSONHardCap(t *testing.T) {
	var items []map[string]any
	for i := 0; i < 400; i++ {
		items = append(items, map[string]any{
			"id":          i,
			"description": strings.Repeat("battery_watch_", 40),
		})
	}

	result := toIndentedJSON(items)
	if len(result) > maxHAToolResultBytes {
		t.Fatalf("result exceeded hard cap: got %d, want <= %d", len(result), maxHAToolResultBytes)
	}
	if !strings.Contains(result, "_truncated") {
		t.Fatalf("result = %q, want truncation metadata when payload exceeds hard cap", result)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func stringSliceFromAny(v any) []string {
	switch raw := v.(type) {
	case []string:
		return append([]string(nil), raw...)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func (f *fakeHAServer) ensureAutomationStateLocked(id string, cfg map[string]any) {
	entityID := automationEntityIDFromConfigID(id)
	alias, _ := cfg["alias"].(string)

	foundState := false
	for i, state := range f.states {
		if state.EntityID != entityID {
			continue
		}
		if state.Attributes == nil {
			state.Attributes = map[string]any{}
		}
		state.Attributes["id"] = id
		if strings.TrimSpace(alias) != "" {
			state.Attributes["friendly_name"] = alias
		}
		if state.State == "" {
			state.State = "on"
		}
		f.states[i] = state
		foundState = true
		break
	}
	if !foundState {
		attrs := map[string]any{"id": id}
		if strings.TrimSpace(alias) != "" {
			attrs["friendly_name"] = alias
		}
		f.states = append(f.states, homeassistant.State{
			EntityID:   entityID,
			State:      "on",
			Attributes: attrs,
		})
	}

	if _, ok := f.entityByID[entityID]; !ok {
		row := map[string]any{
			"id":         entityID + "_row",
			"entity_id":  entityID,
			"name":       alias,
			"labels":     []string{},
			"aliases":    []string{},
			"categories": map[string]any{},
		}
		f.entityByID[entityID] = row
		f.entityRows = append(f.entityRows, row)
	}
}

func (f *fakeHAServer) setAutomationStateLocked(entityID, stateValue string) {
	for i, state := range f.states {
		if state.EntityID == entityID {
			f.states[i].State = stateValue
			return
		}
	}
}

func automationEntityIDFromConfigID(id string) string {
	return "automation." + strings.ReplaceAll(id, "-", "_")
}
