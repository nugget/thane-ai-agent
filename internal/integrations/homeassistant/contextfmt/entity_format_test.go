package contextfmt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestAttachMetadataAppendsWithoutReorderingBasePayload(t *testing.T) {
	t.Parallel()

	formatted := `{"entity":"sensor.office","state":"72","since":"-1s"}`
	metadata := &homeassistant.EntityMetadata{
		Area: &homeassistant.EntityAreaMetadata{
			ID:   "office",
			Name: "Office",
		},
	}

	got := AttachMetadata(formatted, metadata)
	want := `{"entity":"sensor.office","state":"72","since":"-1s","metadata":{"area":{"id":"office","name":"Office"}}}`
	if got != want {
		t.Fatalf("AttachMetadata() = %s, want %s", got, want)
	}
}

func TestAttachMetadataSkipsNonJSONObject(t *testing.T) {
	t.Parallel()

	formatted := `["sensor.office"]`
	metadata := &homeassistant.EntityMetadata{
		Area: &homeassistant.EntityAreaMetadata{ID: "office"},
	}

	if got := AttachMetadata(formatted, metadata); got != formatted {
		t.Fatalf("AttachMetadata() = %s, want original payload", got)
	}
}

func TestFormatPersonCarriesInZones(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	state := &homeassistant.State{
		EntityID: "person.alice",
		State:    "Pool House",
		Attributes: map[string]any{
			"source":   "device_tracker.alicephone",
			"in_zones": []any{"zone.pool_house", "zone.home"},
		},
		LastChanged: now.Add(-5 * time.Minute),
	}

	got := Format(state, now)
	var pc struct {
		State   string   `json:"state"`
		InZones []string `json:"in_zones"`
	}
	if err := json.Unmarshal([]byte(got), &pc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	// State stays the smallest zone (HA semantics); in_zones carries the
	// full membership so the model can see she is still home.
	if pc.State != "Pool House" {
		t.Errorf("state = %q, want Pool House", pc.State)
	}
	if len(pc.InZones) != 2 || pc.InZones[0] != "zone.pool_house" || pc.InZones[1] != "zone.home" {
		t.Errorf("in_zones = %v, want [zone.pool_house zone.home]", pc.InZones)
	}

	// Present-but-empty (HA 2026.7 away): renders an explicit [] so the
	// model can tell "in no zone" apart from "source doesn't report zones".
	away := &homeassistant.State{
		EntityID:    "person.carol",
		State:       "not_home",
		Attributes:  map[string]any{"in_zones": []any{}},
		LastChanged: now,
	}
	if out := Format(away, now); !strings.Contains(out, `"in_zones":[]`) {
		t.Errorf("present-but-empty in_zones should render []: %s", out)
	}

	// Without the attribute entirely, the field is omitted.
	bare := &homeassistant.State{EntityID: "person.bob", State: "not_home", LastChanged: now}
	if out := Format(bare, now); strings.Contains(out, "in_zones") {
		t.Errorf("in_zones should be omitted when absent: %s", out)
	}
}
