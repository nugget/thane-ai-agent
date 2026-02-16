package unifi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetClientStations_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/network/api/s/default/stat/sta" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-API-KEY"); got != "test-key" {
			t.Errorf("expected X-API-KEY test-key, got %q", got)
		}

		resp := struct {
			Data []ClientStation `json:"data"`
		}{
			Data: []ClientStation{
				{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "iphone", LastUplinkName: "ap-office", Signal: -45, LastSeen: 1000},
				{MAC: "11:22:33:44:55:66", Hostname: "macbook", LastUplinkName: "ap-bedroom", Signal: -60, LastSeen: 999},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", nil)
	stations, err := client.GetClientStations(context.Background())
	if err != nil {
		t.Fatalf("GetClientStations: %v", err)
	}

	if len(stations) != 2 {
		t.Fatalf("expected 2 stations, got %d", len(stations))
	}
	if stations[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected first MAC aa:bb:cc:dd:ee:ff, got %q", stations[0].MAC)
	}
	if stations[0].LastUplinkName != "ap-office" {
		t.Errorf("expected AP ap-office, got %q", stations[0].LastUplinkName)
	}
	if stations[1].Hostname != "macbook" {
		t.Errorf("expected hostname macbook, got %q", stations[1].Hostname)
	}
}

func TestGetClientStations_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid api key"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "bad-key", nil)
	_, err := client.GetClientStations(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestGetClientStations_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{invalid"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", nil)
	_, err := client.GetClientStations(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/network/api/s/default/stat/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", nil)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPing_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", nil)
	if err := client.Ping(context.Background()); err == nil {
		t.Fatal("expected error for 503 response")
	}
}

func TestLocateDevices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := struct {
			Data []ClientStation `json:"data"`
		}{
			Data: []ClientStation{
				{MAC: "aa:bb:cc:dd:ee:ff", LastUplinkName: "ap-office", Signal: -45, LastSeen: 1000},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", nil)
	locations, err := client.LocateDevices(context.Background())
	if err != nil {
		t.Fatalf("LocateDevices: %v", err)
	}

	if len(locations) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locations))
	}
	loc := locations[0]
	if loc.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected MAC aa:bb:cc:dd:ee:ff, got %q", loc.MAC)
	}
	if loc.APName != "ap-office" {
		t.Errorf("expected APName ap-office, got %q", loc.APName)
	}
	if loc.Signal != -45 {
		t.Errorf("expected Signal -45, got %d", loc.Signal)
	}
}
