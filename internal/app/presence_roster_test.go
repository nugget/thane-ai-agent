package app

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

func newRosterTestApp(t *testing.T, bindings map[string]string) *App {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, slog.Default())
	if err != nil {
		t.Fatalf("contacts.NewStore: %v", err)
	}
	for name, entity := range bindings {
		c, err := store.Upsert(&contacts.Contact{FormattedName: name})
		if err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		if err := store.SetHAPersonEntity(c.ID, entity); err != nil {
			t.Fatalf("bind %s: %v", name, err)
		}
	}
	return &App{contactStore: store}
}

// TestPresenceRosterComesFromContactBindings pins the inversion: the
// contact store decides who is tracked, not person.track. The bound
// contact that person.track does NOT name must still be in the roster —
// that is the whole point of rooting identity in the contacts database.
func TestPresenceRosterComesFromContactBindings(t *testing.T) {
	a := newRosterTestApp(t, map[string]string{
		"Alice Operator": "person.alice",
		"Zoe Resident":   "person.zoe",
	})
	cfg := &config.Config{}
	cfg.Person.Track = []string{"person.alice"}

	roster, err := a.presenceRoster(cfg)
	if err != nil {
		t.Fatalf("presenceRoster: %v", err)
	}
	if len(roster) != 2 {
		t.Fatalf("roster = %v, want both bound contacts", roster)
	}
	var sawZoe bool
	for _, entity := range roster {
		if entity == "person.zoe" {
			sawZoe = true
		}
	}
	if !sawZoe {
		t.Error("person.zoe is bound to a contact but absent from the roster; membership is still coming from person.track")
	}
}

// TestPresenceRosterRefusesUnclaimedTrackEntry pins the fail-loud
// reconciliation. person.track names entities no contact claims, which
// before this change silently halved the roster and took the ingest
// floor, Snapshot, and the MQTT AP sensors with it.
func TestPresenceRosterRefusesUnclaimedTrackEntry(t *testing.T) {
	a := newRosterTestApp(t, map[string]string{"Alice Operator": "person.alice"})
	cfg := &config.Config{}
	cfg.Person.Track = []string{"person.alice", "person.ghost"}

	roster, err := a.presenceRoster(cfg)
	if err == nil {
		t.Fatalf("presenceRoster accepted an unclaimed person.track entry, roster = %v", roster)
	}
	// The operator has to be able to act on this without reading code.
	if !strings.Contains(err.Error(), "person.ghost") {
		t.Errorf("error does not name the unclaimed entity: %v", err)
	}
	if strings.Contains(err.Error(), "person.alice") {
		t.Errorf("error names a correctly bound entity: %v", err)
	}
}

// TestPresenceRosterWithoutContactStore covers the test-only path where
// no identity root exists: no roster, and no error either, since there is
// nothing to reconcile against.
func TestPresenceRosterWithoutContactStore(t *testing.T) {
	a := &App{}
	cfg := &config.Config{}
	cfg.Person.Track = []string{"person.alice"}

	roster, err := a.presenceRoster(cfg)
	if err != nil {
		t.Fatalf("presenceRoster: %v", err)
	}
	if len(roster) != 0 {
		t.Fatalf("roster = %v, want empty", roster)
	}
}
