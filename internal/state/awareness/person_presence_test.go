package awareness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func personState(entityID, state string) *homeassistant.State {
	return &homeassistant.State{EntityID: entityID, State: state}
}

func trackedPresence(fields PersonPresenceFields) PersonPresenceSource {
	return func(string) (PersonPresenceFields, bool) { return fields, true }
}

// TestEnrichPersonPresenceKeepsBaseKeyOrder pins the splice against the
// obvious implementation. Round-tripping through map[string]any
// re-marshals with sorted keys, which would bury "entity" and "state"
// behind alphabetically earlier presence keys and scatter identity
// through the row.
func TestEnrichPersonPresenceKeepsBaseKeyOrder(t *testing.T) {
	base := `{"entity":"person.nugget","state":"home","since":"-1h"}`
	got := enrichPersonPresence(base, personState("person.nugget", "home"),
		trackedPresence(PersonPresenceFields{Contact: "Nugget", ContactID: "019c76e4", Room: "office"}))

	if !strings.HasPrefix(got, `{"entity":"person.nugget","state":"home","since":"-1h",`) {
		t.Fatalf("base row key order not preserved: %s", got)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("enriched row is not valid JSON: %s", got)
	}
	for _, want := range []string{`"contact":"Nugget"`, `"contact_id":"019c76e4"`, `"room":"office"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

// TestEnrichPersonPresenceDropsRoomWhenNotHome covers the stale-room
// hazard. The tracker clears rooms only on a literal "not_home", so a
// person whose HA state moved to a named zone still holds the room they
// were last seen in. The live row is the authority on whether they are
// home to have one.
func TestEnrichPersonPresenceDropsRoomWhenNotHome(t *testing.T) {
	fields := PersonPresenceFields{Contact: "Nugget", Room: "kitchen", RoomProvider: "bermuda", RoomSource: "ble"}
	for _, state := range []string{"not_home", "zone.work", "unknown"} {
		got := enrichPersonPresence(`{"entity":"person.nugget","state":"x"}`,
			personState("person.nugget", state), trackedPresence(fields))
		if strings.Contains(got, "kitchen") || strings.Contains(got, "room_provider") {
			t.Errorf("state %q kept a room: %s", state, got)
		}
		if !strings.Contains(got, `"contact":"Nugget"`) {
			t.Errorf("state %q dropped contact identity too: %s", state, got)
		}
	}
}

// TestEnrichPersonPresenceDropsRoomOnConflict pins that disagreement
// asserts no room, matching the ambient block rather than picking a
// winner.
func TestEnrichPersonPresenceDropsRoomOnConflict(t *testing.T) {
	got := enrichPersonPresence(`{"entity":"person.nugget","state":"home"}`,
		personState("person.nugget", "home"),
		trackedPresence(PersonPresenceFields{Room: "office", RoomConflict: true}))
	if strings.Contains(got, "office") {
		t.Errorf("conflicting observations still asserted a room: %s", got)
	}
	if !strings.Contains(got, `"room_conflict":true`) {
		t.Errorf("conflict not reported while home: %s", got)
	}
}

// TestEnrichPersonPresencePassesThrough covers every degradation: a
// non-person entity, an untracked person, no tracker wired at all, and a
// tracked person the presence tracker knows nothing else about.
func TestEnrichPersonPresencePassesThrough(t *testing.T) {
	base := `{"entity":"sensor.porch","state":"12"}`
	if got := enrichPersonPresence(base, personState("sensor.porch", "12"),
		trackedPresence(PersonPresenceFields{Contact: "Nugget"})); got != base {
		t.Errorf("non-person entity enriched: %s", got)
	}

	personBase := `{"entity":"person.stranger","state":"home"}`
	untracked := PersonPresenceSource(func(string) (PersonPresenceFields, bool) {
		return PersonPresenceFields{}, false
	})
	if got := enrichPersonPresence(personBase, personState("person.stranger", "home"), untracked); got != personBase {
		t.Errorf("untracked person enriched: %s", got)
	}
	if got := enrichPersonPresence(personBase, personState("person.stranger", "home"), nil); got != personBase {
		t.Errorf("nil source enriched: %s", got)
	}
	if got := enrichPersonPresence(personBase, personState("person.stranger", "home"),
		trackedPresence(PersonPresenceFields{})); got != personBase {
		t.Errorf("empty fields added keys: %s", got)
	}
}

// TestEnrichPersonPresenceRefusesNonObject guards the prompt path. Rows
// that are not JSON objects reach this function — the glob truncation
// marker and fetch-error rows among them — and must pass through rather
// than being spliced into malformed JSON.
func TestEnrichPersonPresenceRefusesNonObject(t *testing.T) {
	for _, base := range []string{"", "not json", "[1,2]", "{unterminated", `{"a":}`, `{"a":1,}`} {
		got := enrichPersonPresence(base, personState("person.nugget", "home"),
			trackedPresence(PersonPresenceFields{Contact: "Nugget"}))
		if got != base {
			t.Errorf("base %q was modified to %q", base, got)
		}
	}
}
