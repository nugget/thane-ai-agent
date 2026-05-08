package agent

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func newTestCapStore(t *testing.T) *OpstateCapabilityTagStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	state, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewOpstateCapabilityTagStore(state)
}

func TestCapabilityTagStore_SaveAndLoad(t *testing.T) {
	store := newTestCapStore(t)

	if err := store.SaveTags("conv-1", []string{"forge", "ha"}); err != nil {
		t.Fatal(err)
	}

	tags, err := store.LoadTags("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "forge" || tags[1] != "ha" {
		t.Errorf("LoadTags = %v, want [forge ha]", tags)
	}
}

func TestCapabilityTagStore_EmptyLoad(t *testing.T) {
	store := newTestCapStore(t)

	tags, err := store.LoadTags("conv-nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if tags != nil {
		t.Errorf("LoadTags for missing conv = %v, want nil", tags)
	}
}

func TestCapabilityTagStore_SaveEmptyClears(t *testing.T) {
	store := newTestCapStore(t)

	store.SaveTags("conv-1", []string{"forge"})
	store.SaveTags("conv-1", nil) // clear

	tags, _ := store.LoadTags("conv-1")
	if tags != nil {
		t.Errorf("LoadTags after clear = %v, want nil", tags)
	}
}

func TestCapabilityTagStore_IsolatedConversations(t *testing.T) {
	store := newTestCapStore(t)

	store.SaveTags("conv-1", []string{"forge"})
	store.SaveTags("conv-2", []string{"ha", "email"})

	tags1, _ := store.LoadTags("conv-1")
	tags2, _ := store.LoadTags("conv-2")

	if len(tags1) != 1 || tags1[0] != "forge" {
		t.Errorf("conv-1 tags = %v, want [forge]", tags1)
	}
	if len(tags2) != 2 {
		t.Errorf("conv-2 tags = %v, want [ha email]", tags2)
	}
}

func TestCapabilityTagStore_Overwrite(t *testing.T) {
	store := newTestCapStore(t)

	store.SaveTags("conv-1", []string{"forge"})
	store.SaveTags("conv-1", []string{"ha", "email"})

	tags, _ := store.LoadTags("conv-1")
	if len(tags) != 2 || tags[0] != "ha" {
		t.Errorf("tags after overwrite = %v, want [ha email]", tags)
	}
}
