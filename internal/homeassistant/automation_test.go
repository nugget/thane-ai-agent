package homeassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateOrUpdateAutomation(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody AutomationConfig

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		token:      "test-token",
		httpClient: srv.Client(),
	}

	config := AutomationConfig{
		ID:          "thane_test_auto",
		Alias:       "Thane: Test Automation",
		Description: "A test automation managed by Thane",
		Trigger: []any{
			map[string]any{
				"platform":  "state",
				"entity_id": "binary_sensor.front_door",
				"to":        "on",
			},
		},
		Action: []any{
			map[string]any{
				"service": "mqtt.publish",
				"data": map[string]any{
					"topic":   "thane/test/anticipations",
					"payload": `{"anticipation_id":"ant_123"}`,
				},
			},
		},
		Mode: "single",
	}

	err := c.CreateOrUpdateAutomation(context.Background(), "thane_test_auto", config)
	if err != nil {
		t.Fatalf("CreateOrUpdateAutomation: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if want := "/api/config/automation/config/thane_test_auto"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Alias != "Thane: Test Automation" {
		t.Errorf("alias = %q, want %q", gotBody.Alias, "Thane: Test Automation")
	}
}

func TestDeleteAutomation(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		token:      "test-token",
		httpClient: srv.Client(),
	}

	err := c.DeleteAutomation(context.Background(), "thane_test_auto")
	if err != nil {
		t.Fatalf("DeleteAutomation: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if want := "/api/config/automation/config/thane_test_auto"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestGetAutomation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AutomationConfig{
			ID:    "thane_test_auto",
			Alias: "Thane: Test Automation",
			Mode:  "single",
		})
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		token:      "test-token",
		httpClient: srv.Client(),
	}

	config, err := c.GetAutomation(context.Background(), "thane_test_auto")
	if err != nil {
		t.Fatalf("GetAutomation: %v", err)
	}

	if config.Alias != "Thane: Test Automation" {
		t.Errorf("alias = %q, want %q", config.Alias, "Thane: Test Automation")
	}
}

func TestAutomationCRUD_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "Automation not found"}`))
	}))
	defer srv.Close()

	c := &Client{
		baseURL:    srv.URL,
		token:      "test-token",
		httpClient: srv.Client(),
	}

	ctx := context.Background()

	if err := c.CreateOrUpdateAutomation(ctx, "missing", AutomationConfig{}); err == nil {
		t.Error("CreateOrUpdateAutomation: expected error for 404")
	}
	if err := c.DeleteAutomation(ctx, "missing"); err == nil {
		t.Error("DeleteAutomation: expected error for 404")
	}
	if _, err := c.GetAutomation(ctx, "missing"); err == nil {
		t.Error("GetAutomation: expected error for 404")
	}
}
