package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

type whereaboutsFixture struct {
	deps      CounterpartyToolDeps
	contactID string
}

// newWhereaboutsFixture builds a contact ("Alice Operator") with an HA
// person binding, one bound companion account carrying the given
// observations, and a fake presence snapshot.
func newWhereaboutsFixture(t *testing.T, state, room string, observations map[string]companion.ObservationStatus) whereaboutsFixture {
	t.Helper()
	cdb, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("contacts db: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	contactStore, err := contacts.NewStore(cdb, nil)
	if err != nil {
		t.Fatalf("contacts store: %v", err)
	}
	alice, err := contactStore.Upsert(&contacts.Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := contactStore.SetHAPersonEntity(alice.ID, "person.alice"); err != nil {
		t.Fatal(err)
	}

	companionStore := newObservationToolStore(t)
	now := time.Now().UTC()
	i := 0
	for device, status := range observations {
		seedLocation(t, companionStore, "alice-acct", device, status, now.Add(-time.Duration(30+i*30)*time.Minute))
		i++
	}

	deps := CounterpartyToolDeps{
		Contacts:   contactStore,
		Companions: companionStore,
		Presence: func(entity string) (contacts.PersonSnapshot, bool) {
			if entity != "person.alice" {
				return contacts.PersonSnapshot{}, false
			}
			snap := contacts.PersonSnapshot{EntityID: entity, State: state, Since: now.Add(-2 * time.Hour)}
			if room != "" {
				snap.Room = room
				snap.RoomSource = "bermuda"
				snap.RoomSince = now.Add(-20 * time.Minute)
			}
			return snap, true
		},
		AccountsForContact: func(id string) []string {
			if id == alice.ID.String() {
				return []string{"alice-acct"}
			}
			return nil
		},
		LiveIdentities: func() map[[2]string]bool { return nil },
	}
	return whereaboutsFixture{deps: deps, contactID: alice.ID.String()}
}

func decodeWhereabouts(t *testing.T, out string) whereaboutsResult {
	t.Helper()
	var res whereaboutsResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return res
}

func TestContactWhereaboutsHomeRanking(t *testing.T) {
	fx := newWhereaboutsFixture(t, "home", "office", map[string]companion.ObservationStatus{"device-1": companion.ObservationAvailable})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.Contact != "Alice Operator" || res.ContactID != fx.contactID {
		t.Errorf("identity = %q/%q", res.Contact, res.ContactID)
	}
	if len(res.Sources) != 3 {
		t.Fatalf("sources = %+v, want room + zone + device", res.Sources)
	}
	if res.Sources[0].Source != "bermuda_room" || res.Sources[0].Room != "office" || res.Sources[0].RoomVia != "bermuda" {
		t.Errorf("home ranking: leading source = %+v, want bermuda room", res.Sources[0])
	}
	if res.Sources[1].Source != "ha_person_zone" || res.Sources[2].Source != "companion_location" {
		t.Errorf("home order = %s, %s", res.Sources[1].Source, res.Sources[2].Source)
	}
	if res.BestSource != "bermuda_room" || !strings.Contains(res.Basis, "home") {
		t.Errorf("verdict = %q / %q", res.BestSource, res.Basis)
	}
}

func TestContactWhereaboutsAwayRanking(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", map[string]companion.ObservationStatus{"device-1": companion.ObservationAvailable})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	if res.Sources[0].Source != "companion_location" {
		t.Errorf("away ranking: leading source = %+v, want device location", res.Sources[0])
	}
	if res.Sources[0].Location == nil || !strings.Contains(string(res.Sources[0].Location), "latitude") {
		t.Errorf("device payload missing: %+v", res.Sources[0])
	}
	last := res.Sources[len(res.Sources)-1]
	if last.Source != "ha_person_zone" || last.State != "not_home" {
		t.Errorf("zone floor = %+v", last)
	}
	if res.BestSource != "companion_location" {
		t.Errorf("BestSource = %q", res.BestSource)
	}
}

func TestContactWhereaboutsWithdrawnSinks(t *testing.T) {
	fx := newWhereaboutsFixture(t, "not_home", "", map[string]companion.ObservationStatus{
		"device-1": companion.ObservationWithdrawn,
		"device-2": companion.ObservationAvailable,
	})
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "Alice Operator", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := decodeWhereabouts(t, out)
	var deviceEntries []whereaboutsSource
	for _, src := range res.Sources {
		if src.Source == "companion_location" {
			deviceEntries = append(deviceEntries, src)
		}
	}
	if len(deviceEntries) != 2 {
		t.Fatalf("device entries = %+v", deviceEntries)
	}
	if deviceEntries[0].Status != "available" || deviceEntries[1].Status != "withdrawn" {
		t.Errorf("withdrawn did not sink: %+v", deviceEntries)
	}
	if deviceEntries[1].Location != nil {
		t.Errorf("withdrawn entry leaked payload: %+v", deviceEntries[1])
	}
}

func TestContactWhereaboutsNoSources(t *testing.T) {
	cdb, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cdb.Close() })
	store, err := contacts.NewStore(cdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(&contacts.Contact{FormattedName: "Bob Guest"}); err != nil {
		t.Fatal(err)
	}
	deps := CounterpartyToolDeps{Contacts: store}
	_, err = handleContactWhereabouts(context.Background(), deps, "Bob Guest", "")
	if err == nil || !strings.Contains(err.Error(), "no whereabouts source") {
		t.Fatalf("unbound contact must explain the configuration gap: %v", err)
	}
}

func TestContactWhereaboutsResolution(t *testing.T) {
	fx := newWhereaboutsFixture(t, "home", "", nil)
	if _, err := handleContactWhereabouts(context.Background(), fx.deps, "", ""); err == nil {
		t.Error("empty selector accepted")
	}
	if _, err := handleContactWhereabouts(context.Background(), fx.deps, "Nobody Real", ""); err == nil || !strings.Contains(err.Error(), "Nobody Real") {
		t.Errorf("unknown name error must echo the selection: %v", err)
	}
	out, err := handleContactWhereabouts(context.Background(), fx.deps, "", fx.contactID)
	if err != nil {
		t.Fatalf("by-id: %v", err)
	}
	if res := decodeWhereabouts(t, out); res.BestSource == "" {
		t.Errorf("by-id result = %+v", res)
	}
}
