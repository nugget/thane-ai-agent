package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestExecute_UnknownToolReturnsErrToolUnavailable(t *testing.T) {
	reg := &Registry{tools: make(map[string]*Tool)}
	reg.Register(&Tool{
		Name:        "known_tool",
		Description: "a tool that exists",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "ok", nil
		},
	})

	// Calling an unknown tool should return ErrToolUnavailable.
	_, err := reg.Execute(context.Background(), "nonexistent_tool", "")
	if err == nil {
		t.Fatal("Execute on unknown tool should return error")
	}

	var unavail *ErrToolUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("error type = %T, want *ErrToolUnavailable", err)
	}
	if unavail.ToolName != "nonexistent_tool" {
		t.Errorf("ToolName = %q, want %q", unavail.ToolName, "nonexistent_tool")
	}
}

func TestExecute_KnownToolDoesNotReturnErrToolUnavailable(t *testing.T) {
	reg := &Registry{tools: make(map[string]*Tool)}
	reg.Register(&Tool{
		Name:        "good_tool",
		Description: "a tool that works",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "result", nil
		},
	})

	result, err := reg.Execute(context.Background(), "good_tool", "")
	if err != nil {
		t.Fatalf("Execute on known tool returned unexpected error: %v", err)
	}
	if result != "result" {
		t.Errorf("result = %q, want %q", result, "result")
	}
}

func TestRegister_AppliesCompiledToolMetadata(t *testing.T) {
	reg := &Registry{tools: make(map[string]*Tool)}
	reg.Register(&Tool{
		Name:        "web_search",
		Description: "search",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "", nil
		},
	})

	tool := reg.Get("web_search")
	if tool == nil {
		t.Fatal("web_search not registered")
	}
	if tool.Source != "native" {
		t.Fatalf("Source = %q, want native", tool.Source)
	}
	if tool.CanonicalID != "native:web_search" {
		t.Fatalf("CanonicalID = %q", tool.CanonicalID)
	}
	if len(tool.Tags) != 1 || tool.Tags[0] != "web" {
		t.Fatalf("Tags = %#v", tool.Tags)
	}
}

func TestFormatEntityState(t *testing.T) {
	changed := time.Now().Add(-time.Minute)
	tests := []struct {
		name       string
		state      *homeassistant.State
		wantParts  []string
		wantAbsent []string
	}{
		{
			name: "light with brightness",
			state: &homeassistant.State{
				EntityID:    "light.office",
				State:       "on",
				LastChanged: changed,
				Attributes: map[string]any{
					"friendly_name": "Office Light",
					"brightness":    float64(255),
				},
			},
			wantParts: []string{
				`"entity":"light.office"`,
				`"state":"on"`,
				`"brightness":100`,
			},
		},
		{
			name: "sensor with unit",
			state: &homeassistant.State{
				EntityID:    "sensor.temperature",
				State:       "22.5",
				LastChanged: changed,
				Attributes: map[string]any{
					"friendly_name":       "Living Room Temp",
					"unit_of_measurement": "°C",
					"temperature":         float64(22.5),
				},
			},
			wantParts: []string{
				`"entity":"sensor.temperature"`,
				`"state":"22.5"`,
				`"name":"Living Room Temp"`,
				`"unit":"°C"`,
			},
		},
		{
			name: "minimal state no attributes",
			state: &homeassistant.State{
				EntityID:    "switch.pump",
				State:       "off",
				LastChanged: changed,
				Attributes:  map[string]any{},
			},
			wantParts: []string{
				`"entity":"switch.pump"`,
				`"state":"off"`,
			},
			wantAbsent: []string{
				`"name":`,
				`"brightness":`,
				`"temperature":`,
				`"unit":`,
			},
		},
		{
			name: "partial brightness",
			state: &homeassistant.State{
				EntityID:    "light.lamp",
				State:       "on",
				LastChanged: changed,
				Attributes: map[string]any{
					"brightness": float64(127.5),
				},
			},
			wantParts: []string{
				`"brightness":50`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatEntityState(tc.state)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("FormatEntityState() missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("FormatEntityState() should not contain %q:\n%s", absent, got)
				}
			}
		})
	}
}

// TestRegistry_List_SortedByName guards Anthropic prompt caching: the
// outbound tools block lands first in the cache key, so a non-deterministic
// order makes every turn miss the cached prefix even when the tool set is
// unchanged. See PR comments diagnosing 0% cache_read on opus turns.
func TestRegistry_List_SortedByName(t *testing.T) {
	// Insert in reverse-alphabetical order so any reliance on insertion
	// order would also fail this test.
	names := []string{"zeta", "yankee", "mike", "delta", "bravo", "alpha"}
	reg := &Registry{tools: make(map[string]*Tool)}
	for _, n := range names {
		reg.Register(&Tool{Name: n, Description: "desc " + n, Handler: func(context.Context, map[string]any) (string, error) { return "", nil }})
	}

	want := []string{"alpha", "bravo", "delta", "mike", "yankee", "zeta"}

	// Run List() repeatedly; if Go map iteration leaks through, one of
	// these calls eventually returns a different order. 32 iterations
	// is overkill for a 6-element map but cheap.
	for i := 0; i < 32; i++ {
		got := reg.List()
		if len(got) != len(want) {
			t.Fatalf("iter %d: List() len=%d, want %d", i, len(got), len(want))
		}
		for j, def := range got {
			fn, _ := def["function"].(map[string]any)
			gotName, _ := fn["name"].(string)
			if gotName != want[j] {
				t.Fatalf("iter %d: List()[%d].name = %q, want %q (full order: %v)", i, j, gotName, want[j], extractListNames(got))
			}
		}
	}
}

// TestRegistry_AllToolNames_Sorted protects external callers (debug
// tooling, telemetry) that already assume a stable order from the
// previously-undocumented map iteration randomness.
func TestRegistry_AllToolNames_Sorted(t *testing.T) {
	names := []string{"zeta", "delta", "bravo", "alpha", "yankee"}
	reg := &Registry{tools: make(map[string]*Tool)}
	for _, n := range names {
		reg.Register(&Tool{Name: n, Description: "x", Handler: func(context.Context, map[string]any) (string, error) { return "", nil }})
	}

	want := []string{"alpha", "bravo", "delta", "yankee", "zeta"}

	for i := 0; i < 32; i++ {
		got := reg.AllToolNames()
		if len(got) != len(want) {
			t.Fatalf("iter %d: AllToolNames() len=%d, want %d", i, len(got), len(want))
		}
		for j := range got {
			if got[j] != want[j] {
				t.Fatalf("iter %d: AllToolNames()[%d] = %q, want %q (full: %v)", i, j, got[j], want[j], got)
			}
		}
	}
}

func extractListNames(defs []map[string]any) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		fn, _ := def["function"].(map[string]any)
		name, _ := fn["name"].(string)
		out = append(out, name)
	}
	return out
}
