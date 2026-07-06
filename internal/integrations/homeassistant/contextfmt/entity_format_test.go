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

// The 2026.7 event domain: raw state is the ISO timestamp of the last
// firing — the exact shape the conventions forbid. The projection leads
// with what fired and expresses when as a delta; the raw timestamp
// never reaches the model. Fixture mirrors a live DoorBird event
// entity verbatim.
func TestFormatEventProjectsFiringNotTimestamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	fired := time.Date(2026, 7, 2, 16, 44, 40, 0, time.UTC)
	state := &homeassistant.State{
		EntityID: "event.doorbird_3040_motion",
		State:    "2026-07-02T16:44:40.728+00:00",
		Attributes: map[string]any{
			"event_types":   []any{"motion"},
			"event_type":    "motion",
			"device_class":  "motion",
			"friendly_name": "DoorBird-3040 Motion",
		},
		LastChanged: fired,
	}

	got := Format(state, now)
	if strings.Contains(got, "2026-07-02T16") {
		t.Errorf("raw ISO timestamp leaked into event context: %s", got)
	}
	var ec struct {
		Event       string `json:"event"`
		DeviceClass string `json:"device_class"`
		Since       string `json:"since"`
	}
	if err := json.Unmarshal([]byte(got), &ec); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if ec.Event != "motion" || ec.DeviceClass != "motion" {
		t.Errorf("event/device_class = %q/%q, want motion/motion", ec.Event, ec.DeviceClass)
	}
	if ec.Since == "" {
		t.Errorf("since delta missing: %s", got)
	}

	// Sparse input: no LastChanged — the delta derives from the state
	// timestamp itself.
	sparse := &homeassistant.State{
		EntityID:   "event.doorbird_3040_pool",
		State:      "2026-06-29T16:47:15.978+00:00",
		Attributes: map[string]any{"event_type": "ring", "device_class": "doorbell"},
	}
	out := Format(sparse, now)
	if strings.Contains(out, "2026-06-29T16") {
		t.Errorf("raw timestamp leaked on sparse input: %s", out)
	}
	if !strings.Contains(out, `"since":"-`) {
		t.Errorf("since not derived from state timestamp: %s", out)
	}
}

// Alarm panel state is already semantic; the curation is who changed it
// and whether arming needs a code. Attribute names per current HA
// alarm_control_panel documentation.
func TestFormatAlarmControlPanel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	state := &homeassistant.State{
		EntityID: "alarm_control_panel.home",
		State:    "armed_away",
		Attributes: map[string]any{
			"friendly_name":      "Home Alarm",
			"changed_by":         "Alice",
			"code_arm_required":  true,
			"supported_features": float64(63), // raw bitmask must not leak
		},
		LastChanged: now.Add(-2 * time.Hour),
	}

	got := Format(state, now)
	var ac struct {
		State           string `json:"state"`
		ChangedBy       string `json:"changed_by"`
		CodeArmRequired bool   `json:"code_arm_required"`
	}
	if err := json.Unmarshal([]byte(got), &ac); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if ac.State != "armed_away" || ac.ChangedBy != "Alice" || !ac.CodeArmRequired {
		t.Errorf("alarm context = %+v, want armed_away/Alice/code required", ac)
	}
	if strings.Contains(got, "supported_features") || strings.Contains(got, "63") {
		t.Errorf("raw bitmask leaked: %s", got)
	}
}
