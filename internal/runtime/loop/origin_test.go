package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestSpecOrigin_ExcludedFromYAML guards the #1125 review fix: origin is
// runtime-stamped, not config-authorable, so it must not serialize to YAML —
// otherwise the generated config example carries an invalid zero-time
// placeholder (created_at: {}) that fails to parse if uncommented. JSON still
// carries it (the overlay is JSON); that path is covered by the round-trip test.
func TestSpecOrigin_ExcludedFromYAML(t *testing.T) {
	spec := Spec{
		Name:      "canyon_lake_watch",
		Operation: OperationService,
		Origin: &OriginInfo{
			RequestID:       "r_x",
			CreatedByLoopID: "lp_x",
			CreatedAt:       time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		},
	}
	out, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	for _, leak := range []string{"origin", "created_by_loop_id", "created_at"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("origin must not serialize to YAML (found %q):\n%s", leak, out)
		}
	}
}

// TestSpecOrigin_JSONRoundTrip confirms the first-class Origin provenance
// survives the custom specJSON wire format (#1106 C2).
func TestSpecOrigin_JSONRoundTrip(t *testing.T) {
	created := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	spec := Spec{
		Name:      "canyon_lake_watch",
		Operation: OperationService,
		Origin: &OriginInfo{
			RequestID:       "r_81e65a55af96da69",
			ConversationID:  "loop-signal-42",
			CreatedByLoopID: "lp_signal",
			CreatedAt:       created,
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"created_by_loop_id":"lp_signal"`) {
		t.Errorf("marshaled JSON missing origin pointers: %s", data)
	}

	var got Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Origin == nil {
		t.Fatal("round-trip dropped origin")
	}
	if got.Origin.RequestID != "r_81e65a55af96da69" ||
		got.Origin.ConversationID != "loop-signal-42" ||
		got.Origin.CreatedByLoopID != "lp_signal" ||
		!got.Origin.CreatedAt.Equal(created) {
		t.Errorf("round-trip origin mismatch: %+v", got.Origin)
	}
}

func TestOriginInfo_Clone(t *testing.T) {
	var nilOrigin *OriginInfo
	if nilOrigin.Clone() != nil {
		t.Error("nil.Clone() should be nil")
	}
	o := &OriginInfo{RequestID: "r_1"}
	c := o.Clone()
	if c == o {
		t.Error("Clone returned the same pointer")
	}
	c.RequestID = "r_2"
	if o.RequestID != "r_1" {
		t.Error("Clone aliased the original")
	}
}
