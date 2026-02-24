package hainject

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// mockFetcher returns preconfigured state values or errors.
type mockFetcher struct {
	states map[string]string // entity_id → state value
	err    error             // returned for entities not in states map
}

func (m *mockFetcher) FetchState(_ context.Context, entityID string) (string, error) {
	if m.err != nil {
		if _, ok := m.states[entityID]; !ok {
			return "", m.err
		}
	}
	state, ok := m.states[entityID]
	if !ok {
		return "", fmt.Errorf("entity %q not found", entityID)
	}
	return state, nil
}

func TestResolve(t *testing.T) {
	logger := slog.Default()

	tests := []struct {
		name     string
		content  string
		fetcher  StateFetcher
		wantIn   []string // substrings that must appear in result
		wantOut  []string // substrings that must NOT appear in result
		wantSame bool     // result should equal input unchanged
	}{
		{
			name:     "no directives",
			content:  "# Architecture\nUse hexagonal pattern.",
			fetcher:  &mockFetcher{states: map[string]string{"x": "y"}},
			wantSame: true,
		},
		{
			name:     "nil fetcher",
			content:  "<!-- ha-inject: sensor.temp -->\n# Doc",
			fetcher:  nil,
			wantSame: true,
		},
		{
			name:    "single directive single entity",
			content: "<!-- ha-inject: input_boolean.burn_ban -->\n# Burn Ban",
			fetcher: &mockFetcher{states: map[string]string{
				"input_boolean.burn_ban": "on",
			}},
			wantIn: []string{
				"## Current HA State (live)",
				"- input_boolean.burn_ban: on",
				"---",
				"# Burn Ban",
			},
		},
		{
			name:    "single directive multiple entities",
			content: "<!-- ha-inject: input_boolean.burn_ban, sensor.pool_temp -->\n# Status",
			fetcher: &mockFetcher{states: map[string]string{
				"input_boolean.burn_ban": "on",
				"sensor.pool_temp":       "82.5",
			}},
			wantIn: []string{
				"- input_boolean.burn_ban: on",
				"- sensor.pool_temp: 82.5",
				"# Status",
			},
		},
		{
			name: "multiple directives",
			content: "<!-- ha-inject: sensor.temp -->\n# Section 1\n\n" +
				"<!-- ha-inject: switch.pool_pump -->\n# Section 2",
			fetcher: &mockFetcher{states: map[string]string{
				"sensor.temp":      "72",
				"switch.pool_pump": "off",
			}},
			wantIn: []string{
				"- sensor.temp: 72",
				"- switch.pool_pump: off",
				"# Section 1",
				"# Section 2",
			},
		},
		{
			name:    "duplicate entity IDs deduplicated",
			content: "<!-- ha-inject: sensor.temp -->\n<!-- ha-inject: sensor.temp -->\n# Doc",
			fetcher: &mockFetcher{states: map[string]string{
				"sensor.temp": "72",
			}},
			wantIn: []string{"- sensor.temp: 72"},
		},
		{
			name:    "all fetches fail",
			content: "<!-- ha-inject: sensor.missing -->\n# Doc",
			fetcher: &mockFetcher{
				states: map[string]string{},
				err:    fmt.Errorf("connection refused"),
			},
			wantIn:  []string{"⚠️ HA entity state unavailable", "# Doc"},
			wantOut: []string{"## Current HA State"},
		},
		{
			name:    "partial failure",
			content: "<!-- ha-inject: sensor.temp, sensor.missing -->\n# Doc",
			fetcher: &mockFetcher{
				states: map[string]string{"sensor.temp": "72"},
				err:    fmt.Errorf("not found"),
			},
			wantIn: []string{
				"## Current HA State (live)",
				"- sensor.temp: 72",
				"- sensor.missing: ⚠️ fetch failed",
				"# Doc",
			},
		},
		{
			name:    "whitespace in directive",
			content: "<!--   ha-inject:  sensor.temp ,  switch.light   -->\n# Doc",
			fetcher: &mockFetcher{states: map[string]string{
				"sensor.temp":  "72",
				"switch.light": "on",
			}},
			wantIn: []string{
				"- sensor.temp: 72",
				"- switch.light: on",
			},
		},
		{
			name:     "empty entity list",
			content:  "<!-- ha-inject: -->\n# Doc",
			fetcher:  &mockFetcher{states: map[string]string{}},
			wantSame: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Resolve(context.Background(), []byte(tt.content), tt.fetcher, logger)
			got := string(result)

			if tt.wantSame {
				if got != tt.content {
					t.Errorf("expected content unchanged, got:\n%s", got)
				}
				return
			}

			for _, want := range tt.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("result missing %q\n\ngot:\n%s", want, got)
				}
			}
			for _, unwant := range tt.wantOut {
				if strings.Contains(got, unwant) {
					t.Errorf("result should not contain %q\n\ngot:\n%s", unwant, got)
				}
			}
		})
	}
}

func TestParseDirectives(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "no directives",
			content: "plain markdown",
			want:    nil,
		},
		{
			name:    "single entity",
			content: "<!-- ha-inject: sensor.temp -->",
			want:    []string{"sensor.temp"},
		},
		{
			name:    "multiple entities",
			content: "<!-- ha-inject: sensor.temp, switch.light -->",
			want:    []string{"sensor.temp", "switch.light"},
		},
		{
			name:    "deduplication preserves order",
			content: "<!-- ha-inject: b, a, b -->",
			want:    []string{"b", "a"},
		},
		{
			name:    "multiple directives merged",
			content: "<!-- ha-inject: a -->\ntext\n<!-- ha-inject: b -->",
			want:    []string{"a", "b"},
		},
		{
			name:    "empty value skipped",
			content: "<!-- ha-inject: sensor.temp, , switch.light -->",
			want:    []string{"sensor.temp", "switch.light"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDirectives([]byte(tt.content))
			if len(got) != len(tt.want) {
				t.Fatalf("parseDirectives() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("parseDirectives()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
