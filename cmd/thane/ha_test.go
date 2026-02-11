//go:build integration
// +build integration

package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

func TestHAClient(t *testing.T) {
	url := os.Getenv("HA_URL")
	token := os.Getenv("HA_TOKEN")
	if url == "" || token == "" {
		t.Skip("HA_URL and HA_TOKEN not set")
	}

	client := homeassistant.NewClient(url, token, slog.Default())
	ctx := context.Background()

	// Test ping
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	t.Log("Ping: OK")

	// Test config
	cfg, err := client.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	t.Logf("Config: %s (HA %s)", cfg.LocationName, cfg.Version)

	// Test states (just count)
	states, err := client.GetStates(ctx)
	if err != nil {
		t.Fatalf("GetStates failed: %v", err)
	}
	t.Logf("States: %d entities", len(states))

	// Test single state
	state, err := client.GetState(ctx, "sun.sun")
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	t.Logf("sun.sun: %s", state.State)
}
