package awareness

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// These tests cover the WIRING rather than the projection: that each
// provider actually consults its presence source on every render path.
// Testing enrichPersonPresence alone would leave the option, the struct
// field, and every p.presence argument free to be deleted with CI green.

func presenceTestStore(t *testing.T) *WatchlistStore {
	t.Helper()
	db, err := sql.Open("sqlite-thane", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func presenceTestHA() *fakeHA {
	return &fakeHA{states: map[string]*homeassistant.State{
		"person.nugget": {EntityID: "person.nugget", State: "home"},
		"person.dan":    {EntityID: "person.dan", State: "home"},
	}}
}

// officePresence records which entities the provider asked about, so a
// test can prove the source was consulted on the path under test rather
// than inferring it from the rendered output alone.
func officePresence(seen *[]string) PersonPresenceSource {
	return func(entityID string) (PersonPresenceFields, bool) {
		*seen = append(*seen, entityID)
		return PersonPresenceFields{Contact: "Nugget", ContactID: "019c76e4", Room: "office"}, true
	}
}

func TestWatchlistProviderEnrichesConcretePersonSubscription(t *testing.T) {
	var seen []string
	store := presenceTestStore(t)
	p := NewWatchlistProvider(store, presenceTestHA(), slog.Default(),
		WithPersonPresence(officePresence(&seen)))
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "person.nugget"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	out, err := p.TagContext(context.Background(), agentctx.ContextRequest{IncludeAlways: true})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	for _, want := range []string{`"contact":"Nugget"`, `"contact_id":"019c76e4"`, `"room":"office"`} {
		if !strings.Contains(out, want) {
			t.Errorf("watchlist concrete render missing %s:\n%s", want, out)
		}
	}
	if len(seen) == 0 {
		t.Error("presence source was never consulted on the concrete path")
	}
}

func TestWatchlistProviderEnrichesGlobPersonSubscription(t *testing.T) {
	var seen []string
	store := presenceTestStore(t)
	p := NewWatchlistProvider(store, presenceTestHA(), slog.Default(),
		WithPersonPresence(officePresence(&seen)))
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "person.*"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	out, err := p.TagContext(context.Background(), agentctx.ContextRequest{IncludeAlways: true})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"contact":"Nugget"`) {
		t.Errorf("glob expansion did not enrich:\n%s", out)
	}
	if len(seen) < 2 {
		t.Errorf("glob expansion consulted presence for %v, want both people", seen)
	}
}

func TestLoopSubscriptionProviderEnrichesPersonSubscription(t *testing.T) {
	var seen []string
	store := presenceTestStore(t)
	reg := looppkg.NewRegistry()
	p := NewLoopSubscriptionProvider(reg, store, presenceTestHA(), slog.Default(),
		WithPersonPresence(officePresence(&seen)))

	// The motivating case: a service loop declares the people it may
	// see, and needs a room and a contact id to act on them.
	doorbell, err := looppkg.New(looppkg.Config{
		Name:          "doorbell",
		Task:          "answer the door",
		Subscriptions: []looppkg.EntitySubscription{{EntityID: "person.nugget"}},
	}, looppkg.Deps{Runner: noopRunnerForLoopSub{}})
	if err != nil {
		t.Fatalf("new loop: %v", err)
	}
	if err := reg.Register(doorbell); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx := looppkg.WithLoopIDForTest(context.Background(), doorbell.ID())
	out, err := p.TagContext(ctx, agentctx.ContextRequest{IncludeLoopScoped: true})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	for _, want := range []string{`"contact":"Nugget"`, `"contact_id":"019c76e4"`, `"room":"office"`} {
		if !strings.Contains(out, want) {
			t.Errorf("loop-scoped render missing %s:\n%s", want, out)
		}
	}
	if len(seen) == 0 {
		t.Error("presence source was never consulted on the loop-scoped path")
	}
}

// TestSubscriptionProvidersWithoutPresenceRenderRawState pins the
// degradation: no option means the raw Home Assistant row, unchanged.
func TestSubscriptionProvidersWithoutPresenceRenderRawState(t *testing.T) {
	store := presenceTestStore(t)
	p := NewWatchlistProvider(store, presenceTestHA(), slog.Default())
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "person.nugget"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	out, err := p.TagContext(context.Background(), agentctx.ContextRequest{IncludeAlways: true})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, "person.nugget") {
		t.Fatalf("person row missing entirely:\n%s", out)
	}
	// Match JSON keys, not bare words: the block's own header prose
	// mentions "other rooms".
	for _, unwanted := range []string{`"contact":`, `"contact_id":`, `"room":`, `"trust_zone":`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unwired provider emitted %s:\n%s", unwanted, out)
		}
	}
}
