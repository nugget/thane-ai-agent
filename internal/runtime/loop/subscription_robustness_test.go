package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestUnmarshalJSONStampsAddedAtForTTL is the regression test for
// the documented "permanent watcher" footgun. A persisted or
// API-supplied spec with `ttl_seconds: 60` and missing `added_at`
// previously deserialized into a subscription whose
// [EntitySubscription.IsExpired] returned false forever. The
// UnmarshalJSON sweep now stamps AddedAt = now so the TTL clock
// actually starts.
func TestUnmarshalJSONStampsAddedAtForTTL(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"name": "watcher",
		"operation": "service",
		"task": "t",
		"sleep_min": "1m",
		"sleep_max": "1m",
		"sleep_default": "1m",
		"subscriptions": [
			{"entity_id": "sensor.foo", "ttl_seconds": 60}
		]
	}`)

	var s Spec
	before := time.Now()
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	after := time.Now()

	if len(s.Subscriptions) != 1 {
		t.Fatalf("Subscriptions len = %d, want 1", len(s.Subscriptions))
	}
	sub := s.Subscriptions[0]
	if sub.TTLSeconds != 60 {
		t.Errorf("TTLSeconds = %d, want 60", sub.TTLSeconds)
	}
	if sub.AddedAt.IsZero() {
		t.Fatal("AddedAt is zero — UnmarshalJSON should have stamped it for TTL>0")
	}
	if sub.AddedAt.Before(before) || sub.AddedAt.After(after) {
		t.Errorf("AddedAt = %v, want between %v and %v", sub.AddedAt, before, after)
	}
}

// TestUnmarshalJSONPreservesExistingAddedAt confirms the stamp
// only fires when AddedAt is zero — a spec round-tripped through
// JSON keeps its original timestamp.
func TestUnmarshalJSONPreservesExistingAddedAt(t *testing.T) {
	t.Parallel()

	original := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	raw, err := json.Marshal(Spec{
		Name:         "watcher",
		Operation:    OperationService,
		Task:         "t",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.foo", TTLSeconds: 60, AddedAt: original},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !s.Subscriptions[0].AddedAt.Equal(original) {
		t.Errorf("AddedAt = %v, want %v (existing timestamp should not be overwritten)", s.Subscriptions[0].AddedAt, original)
	}
}

// TestUnmarshalJSONNormalizesForecast covers the parallel
// invariant: "none" maps to "" and unknown values are rejected at
// the hydration boundary, matching the tool-side normalizer.
// Without this, a persisted forecast of "none" would reach the
// renderer as a real forecast type and trigger an invalid HA
// fetch.
func TestUnmarshalJSONNormalizesForecast(t *testing.T) {
	t.Parallel()

	t.Run("none collapses to empty", func(t *testing.T) {
		raw := []byte(`{
			"name": "w",
			"operation": "service",
			"task": "t",
			"sleep_min": "1m",
			"sleep_max": "1m",
			"sleep_default": "1m",
			"subscriptions": [{"entity_id": "weather.home", "forecast": "none"}]
		}`)
		var s Spec
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if s.Subscriptions[0].Forecast != "" {
			t.Errorf("Forecast = %q, want empty", s.Subscriptions[0].Forecast)
		}
	})

	t.Run("invalid forecast rejected", func(t *testing.T) {
		raw := []byte(`{
			"name": "w",
			"operation": "service",
			"task": "t",
			"sleep_min": "1m",
			"sleep_max": "1m",
			"sleep_default": "1m",
			"subscriptions": [{"entity_id": "weather.home", "forecast": "monthly"}]
		}`)
		var s Spec
		err := json.Unmarshal(raw, &s)
		if err == nil {
			t.Fatal("Unmarshal accepted invalid forecast; want rejection")
		}
		if !strings.Contains(err.Error(), "forecast") {
			t.Errorf("err = %v, should mention forecast", err)
		}
	})
}

// TestNormalizeSubscriptionForecastTable covers the canonicalizer
// directly so the boundary contract is pinned independently of
// the UnmarshalJSON path.
func TestNormalizeSubscriptionForecastTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"none", "", false},
		{"  ", "", false},
		{"daily", "daily", false},
		{"hourly", "hourly", false},
		{"twice_daily", "twice_daily", false},
		{"  daily  ", "daily", false},
		{"monthly", "", true},
		{"DAILY", "", true}, // strict — case-sensitive at this boundary
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := NormalizeSubscriptionForecast(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
