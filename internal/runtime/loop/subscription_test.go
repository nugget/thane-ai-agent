package loop

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// TestEntitySubscriptionIsExpired covers the per-row TTL contract used
// by the awareness renderer to drop stale rows at render time without
// a background sweeper.
func TestEntitySubscriptionIsExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		sub  EntitySubscription
		want bool
	}{
		{"zero TTL never expires", EntitySubscription{EntityID: "a", AddedAt: now.Add(-100 * time.Hour)}, false},
		{"zero AddedAt is not expired", EntitySubscription{EntityID: "a", TTLSeconds: 60}, false},
		{"within window is not expired", EntitySubscription{EntityID: "a", AddedAt: now.Add(-30 * time.Second), TTLSeconds: 60}, false},
		{"exactly at expiry is not expired", EntitySubscription{EntityID: "a", AddedAt: now.Add(-60 * time.Second), TTLSeconds: 60}, false},
		{"beyond expiry is expired", EntitySubscription{EntityID: "a", AddedAt: now.Add(-90 * time.Second), TTLSeconds: 60}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.IsExpired(now); got != tc.want {
				t.Errorf("IsExpired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCloneEntitySubscriptionsDeepCopiesInclude(t *testing.T) {
	t.Parallel()

	include := &homeassistant.EntityMetadataIncludes{Area: true, Labels: true}
	src := []EntitySubscription{{
		EntityID: "sensor.office_temperature",
		Include:  include,
	}}

	got := cloneEntitySubscriptions(src)
	if len(got) != 1 || got[0].Include == nil {
		t.Fatalf("clone = %#v, want include pointer", got)
	}
	got[0].Include.Area = false
	if !src[0].Include.Area {
		t.Fatal("clone mutated source Include pointer")
	}
}

// TestRegistryAncestorSubscriptionsMergesContainers asserts the
// structural inheritance contract: a leaf loop's effective list is
// own + every container ancestor's, parent-first, deduplicated by
// EntityID with first-wins.
func TestRegistryAncestorSubscriptionsMergesContainers(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	root, err := New(Config{
		Name:      "root_container",
		Operation: OperationContainer,
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.shared", History: []int{600}},
			{EntityID: "sensor.root_only"},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}

	mid, err := New(Config{
		Name:      "mid_container",
		Operation: OperationContainer,
		ParentID:  root.ID(),
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.mid_only"},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new mid: %v", err)
	}
	if err := r.Register(mid); err != nil {
		t.Fatalf("register mid: %v", err)
	}

	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "do",
		ParentID: mid.ID(),
		Subscriptions: []EntitySubscription{
			// Override root's shared subscription with different history.
			// Own list wins under first-wins dedup.
			{EntityID: "sensor.shared", History: []int{86400}},
			{EntityID: "sensor.leaf_only"},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.AncestorSubscriptions(leaf.ID())
	if len(got) != 4 {
		t.Fatalf("got %d subscriptions, want 4: %+v", len(got), got)
	}
	byID := make(map[string]EntitySubscription, len(got))
	for _, sub := range got {
		byID[sub.EntityID] = sub
	}
	if _, ok := byID["sensor.leaf_only"]; !ok {
		t.Error("missing sensor.leaf_only")
	}
	if _, ok := byID["sensor.mid_only"]; !ok {
		t.Error("missing sensor.mid_only")
	}
	if _, ok := byID["sensor.root_only"]; !ok {
		t.Error("missing sensor.root_only")
	}
	shared, ok := byID["sensor.shared"]
	if !ok {
		t.Fatal("missing sensor.shared")
	}
	// Leaf's own declaration must win on options merge.
	if len(shared.History) != 1 || shared.History[0] != 86400 {
		t.Errorf("sensor.shared history = %v, want [86400] (leaf override)", shared.History)
	}
}

// TestRegistryAncestorSubscriptionsSkipsNonContainerAncestors verifies
// that subscriptions are inherited only from container ancestors —
// the Phase-1A "containers are state-inheritance nodes" contract
// extends to subscriptions, not to arbitrary parent loops.
func TestRegistryAncestorSubscriptionsSkipsNonContainerAncestors(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	svc, err := New(Config{
		Name:      "service_parent",
		Task:      "t",
		Operation: OperationService,
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.from_service"},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new svc: %v", err)
	}
	if err := r.Register(svc); err != nil {
		t.Fatalf("register svc: %v", err)
	}
	child, err := New(Config{
		Name:     "child",
		Task:     "t",
		ParentID: svc.ID(),
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.own"},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := r.Register(child); err != nil {
		t.Fatalf("register child: %v", err)
	}

	got := r.AncestorSubscriptions(child.ID())
	if len(got) != 1 || got[0].EntityID != "sensor.own" {
		t.Errorf("got %+v, want only own subscription (service parent contributes nothing)", got)
	}
}

// TestLoopSetSubscriptionsRoundtrip exercises the runtime mutator
// contract: tools running inside an iteration can replace the live
// subscription list, and the next call to AncestorSubscriptions sees
// the change without restart.
func TestLoopSetSubscriptionsRoundtrip(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l, err := New(Config{
		Name: "mutable",
		Task: "t",
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.initial"},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := r.Register(l); err != nil {
		t.Fatalf("register: %v", err)
	}

	l.SetSubscriptions([]EntitySubscription{
		{EntityID: "sensor.new1"},
		{EntityID: "sensor.new2"},
	})

	got := r.AncestorSubscriptions(l.ID())
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 after SetSubscriptions", len(got))
	}
	if got[0].EntityID != "sensor.new1" || got[1].EntityID != "sensor.new2" {
		t.Errorf("subs = %+v, want sensor.new1 + sensor.new2", got)
	}
	if got := l.Subscriptions(); len(got) != 2 {
		t.Errorf("Subscriptions accessor returned %d entries, want 2", len(got))
	}
}
