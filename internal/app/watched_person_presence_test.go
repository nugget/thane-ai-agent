package app

import (
	"log/slog"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// TestWatchedPersonPresenceDegradesPerSource pins the contract the doc
// comment states: tracker and contact store fail independently. A person
// the tracker knows but no contact claims still contributes their room —
// dropping the whole row there would lose live house state over a
// missing vCard.
func TestWatchedPersonPresenceDegradesPerSource(t *testing.T) {
	logger := slog.Default()

	t.Run("no tracker yields nothing", func(t *testing.T) {
		a := &App{}
		source := a.watchedPersonPresence(&newState{}, logger)
		if _, ok := source("person.nugget"); ok {
			t.Error("presence reported for a nil tracker")
		}
	})

	t.Run("untracked entity yields nothing", func(t *testing.T) {
		a := &App{}
		s := &newState{personTracker: contacts.NewPresenceTracker(
			[]string{"person.nugget"}, "UTC", logger)}
		source := a.watchedPersonPresence(s, logger)
		if _, ok := source("person.stranger"); ok {
			t.Error("presence reported for an untracked entity")
		}
	})

	t.Run("tracked with a store but no claiming contact keeps room", func(t *testing.T) {
		// A live store that simply holds no contact for this entity:
		// the resolver runs and reports no match, which must omit
		// identity without discarding the room.
		db, err := database.OpenMemory()
		if err != nil {
			t.Fatalf("database.OpenMemory: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store, err := contacts.NewStore(db, logger)
		if err != nil {
			t.Fatalf("contacts.NewStore: %v", err)
		}
		a := &App{contactStore: store}
		s := &newState{personTracker: contacts.NewPresenceTracker(
			[]string{"person.nugget"}, "UTC", logger)}
		source := a.watchedPersonPresence(s, logger)

		fields, ok := source("person.nugget")
		if !ok {
			t.Fatal("a tracked person with no claiming contact reported no presence at all")
		}
		if fields.Contact != "" || fields.ContactID != "" {
			t.Errorf("identity invented without a contact: %+v", fields)
		}
	})

	t.Run("tracked without a contact store keeps room, omits identity", func(t *testing.T) {
		a := &App{}
		s := &newState{personTracker: contacts.NewPresenceTracker(
			[]string{"person.nugget"}, "UTC", logger)}
		source := a.watchedPersonPresence(s, logger)

		fields, ok := source("person.nugget")
		if !ok {
			t.Fatal("a tracked person reported no presence at all")
		}
		if fields.Contact != "" || fields.ContactID != "" || fields.TrustZone != "" || fields.IsOperator {
			t.Errorf("identity invented without a contact: %+v", fields)
		}
	})
}
