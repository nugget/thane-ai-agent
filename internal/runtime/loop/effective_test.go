package loop

import (
	"testing"
	"time"
)

// TestRegistryEffectiveSubscriptionsCarriesProvenance asserts the
// provenance contract: the loop's own subscriptions are marked
// "self"; inherited subscriptions name the originating ancestor
// container. This is the load-bearing test for PR-B's debugging
// promise — without provenance the model can't tell which entries
// it owns and which would persist regardless.
func TestRegistryEffectiveSubscriptionsCarriesProvenance(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	container, err := New(Config{
		Name:      "home_automation",
		Operation: OperationContainer,
		Subscriptions: []EntitySubscription{
			{EntityID: "weather.home", Forecast: "hourly"},
			{EntityID: "presence.front_door"},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}

	leaf, err := New(Config{
		Name:     "morning_briefing",
		Task:     "summarize the morning",
		ParentID: container.ID(),
		Subscriptions: []EntitySubscription{
			{EntityID: "calendar.work", History: []int{86400}},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.EffectiveSubscriptions(leaf.ID())
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (own + 2 inherited): %+v", len(got), got)
	}

	// First entry is the loop's own declaration (walk is parent-first
	// after the starting loop contributes).
	if got[0].EntityID != "calendar.work" {
		t.Errorf("got[0].EntityID = %q, want calendar.work", got[0].EntityID)
	}
	if got[0].From != EffectiveOriginSelf {
		t.Errorf("got[0].From = %q, want %q", got[0].From, EffectiveOriginSelf)
	}
	// Inherited entries name the container.
	for _, sub := range got[1:] {
		if sub.From != "home_automation" {
			t.Errorf("inherited sub %q has From=%q, want home_automation", sub.EntityID, sub.From)
		}
	}
}

// TestRegistryEffectiveTagsCarriesProvenance mirrors the subscription
// test for tags — same provenance contract, same walk shape.
func TestRegistryEffectiveTagsCarriesProvenance(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	container, err := New(Config{
		Name:      "home_automation",
		Operation: OperationContainer,
		Tags:      []string{"home", "ambient"},
	}, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}

	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "t",
		ParentID: container.ID(),
		Tags:     []string{"research"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.EffectiveTags(leaf.ID())
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (own + 2 inherited): %+v", len(got), got)
	}
	if got[0].Tag != "research" || got[0].From != EffectiveOriginSelf {
		t.Errorf("got[0] = %+v, want {research self}", got[0])
	}
	for _, t2 := range got[1:] {
		if t2.From != "home_automation" {
			t.Errorf("inherited tag %q has From=%q, want home_automation", t2.Tag, t2.From)
		}
	}
}

// TestRegistryEffectiveSubscriptionsDedupesAcrossLevels covers
// first-wins precedence when a leaf and an ancestor both declare the
// same entity: the leaf's options + provenance win, the ancestor's
// version is silently dropped from the effective view.
func TestRegistryEffectiveSubscriptionsDedupesAcrossLevels(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	root, err := New(Config{
		Name:      "root",
		Operation: OperationContainer,
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.shared", Forecast: "daily"},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}
	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "t",
		ParentID: root.ID(),
		Subscriptions: []EntitySubscription{
			{EntityID: "sensor.shared", Forecast: "hourly"},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.EffectiveSubscriptions(leaf.ID())
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (deduped)", len(got))
	}
	if got[0].From != EffectiveOriginSelf {
		t.Errorf("From = %q, want %q (leaf overrides root)", got[0].From, EffectiveOriginSelf)
	}
	if got[0].Forecast != "hourly" {
		t.Errorf("Forecast = %q, want hourly (leaf's option, not root's daily)", got[0].Forecast)
	}
}

// TestLoopStatusReportsEffectiveState covers the Status snapshot
// surface: a registered loop's Status() includes EffectiveTags and
// EffectiveSubscriptions populated by the registry-installed hook.
func TestLoopStatusReportsEffectiveState(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	container, err := New(Config{
		Name:      "ctx",
		Operation: OperationContainer,
		Tags:      []string{"ambient"},
		Subscriptions: []EntitySubscription{
			{EntityID: "weather.home"},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}
	leaf, err := New(Config{
		Name:     "reporter",
		Task:     "t",
		ParentID: container.ID(),
		Tags:     []string{"news"},
		Subscriptions: []EntitySubscription{
			{EntityID: "calendar.work"},
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	st := leaf.Status()
	if len(st.EffectiveTags) != 2 {
		t.Fatalf("EffectiveTags len = %d, want 2: %+v", len(st.EffectiveTags), st.EffectiveTags)
	}
	if len(st.EffectiveSubscriptions) != 2 {
		t.Fatalf("EffectiveSubscriptions len = %d, want 2: %+v", len(st.EffectiveSubscriptions), st.EffectiveSubscriptions)
	}
	// Status returns the loop's own first, then ancestor's.
	if st.EffectiveTags[0].Tag != "news" || st.EffectiveTags[0].From != EffectiveOriginSelf {
		t.Errorf("EffectiveTags[0] = %+v, want {news self}", st.EffectiveTags[0])
	}
	if st.EffectiveTags[1].From != "ctx" {
		t.Errorf("inherited tag From = %q, want ctx", st.EffectiveTags[1].From)
	}
}

// TestLoopStatusEffectiveEmptyWithoutRegistry guards against the
// degraded path: a Loop built with New() outside any registry has
// no effectiveStateFunc and Status() returns empty effective slices
// without panicking.
func TestLoopStatusEffectiveEmptyWithoutRegistry(t *testing.T) {
	t.Parallel()

	l, err := New(Config{Name: "standalone", Task: "t"}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	st := l.Status()
	if st.EffectiveTags != nil {
		t.Errorf("EffectiveTags = %v, want nil for unregistered loop", st.EffectiveTags)
	}
	if st.EffectiveSubscriptions != nil {
		t.Errorf("EffectiveSubscriptions = %v, want nil for unregistered loop", st.EffectiveSubscriptions)
	}
}

// TestBuildDefinitionRegistryViewPopulatesEffective wires the full
// path: a running loop plus its container ancestor produce a
// DefinitionView with Effective populated and carrying provenance.
func TestBuildDefinitionRegistryViewPopulatesEffective(t *testing.T) {
	t.Parallel()

	// Build a small definition registry + live registry pairing that
	// mirrors what the app does in production.
	defReg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	containerSpec := Spec{
		Name:      "home_automation",
		Enabled:   true,
		Operation: OperationContainer,
		Tags:      []string{"home"},
		Subscriptions: []EntitySubscription{
			{EntityID: "weather.home"},
		},
	}
	now := time.Now()
	if err := defReg.Upsert(containerSpec, now); err != nil {
		t.Fatalf("upsert container: %v", err)
	}
	leafSpec := Spec{
		Name:         "morning_brief",
		Enabled:      true,
		Task:         "summarize",
		Operation:    OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Tags:         []string{"news"},
		ParentName:   "home_automation",
		Subscriptions: []EntitySubscription{
			{EntityID: "calendar.work"},
		},
	}
	if err := defReg.Upsert(leafSpec, now); err != nil {
		t.Fatalf("upsert leaf: %v", err)
	}

	live := NewRegistry()
	container, err := New(containerSpec.ToConfig(), Deps{})
	if err != nil {
		t.Fatalf("new container loop: %v", err)
	}
	if err := live.Register(container); err != nil {
		t.Fatalf("register container loop: %v", err)
	}
	leafCfg := leafSpec.ToConfig()
	leafCfg.ParentID = container.ID()
	leaf, err := New(leafCfg, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf loop: %v", err)
	}
	if err := live.Register(leaf); err != nil {
		t.Fatalf("register leaf loop: %v", err)
	}

	runtimeStatus := map[string]DefinitionRuntimeStatus{
		"home_automation": {Running: true, LoopID: container.ID()},
		"morning_brief":   {Running: true, LoopID: leaf.ID()},
	}

	view := BuildDefinitionRegistryView(defReg.Snapshot(), runtimeStatus, WithLiveRegistry(live))
	if view == nil {
		t.Fatal("BuildDefinitionRegistryView returned nil")
	}

	var leafView *DefinitionView
	for i := range view.Definitions {
		if view.Definitions[i].Name == "morning_brief" {
			leafView = &view.Definitions[i]
			break
		}
	}
	if leafView == nil {
		t.Fatal("morning_brief not in view")
	}
	if leafView.Effective == nil {
		t.Fatal("Effective is nil; want populated for running loop")
	}
	if len(leafView.Effective.Tags) != 2 {
		t.Errorf("Effective.Tags len = %d, want 2: %+v", len(leafView.Effective.Tags), leafView.Effective.Tags)
	}
	if len(leafView.Effective.Subscriptions) != 2 {
		t.Errorf("Effective.Subscriptions len = %d, want 2: %+v", len(leafView.Effective.Subscriptions), leafView.Effective.Subscriptions)
	}
}

// TestBuildDefinitionRegistryViewEffectiveNilWithoutRegistry covers
// the safe-default branch: callers that build the view from a
// snapshot alone (CLI tools, lint surfaces) get no Effective field
// rather than a misleading empty one.
func TestBuildDefinitionRegistryViewEffectiveNilWithoutRegistry(t *testing.T) {
	t.Parallel()

	defReg, err := NewDefinitionRegistry([]Spec{
		{
			Name:         "snapshot_only",
			Enabled:      true,
			Task:         "t",
			Operation:    OperationService,
			SleepMin:     time.Minute,
			SleepMax:     time.Minute,
			SleepDefault: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	view := BuildDefinitionRegistryView(defReg.Snapshot(), nil)
	if view == nil {
		t.Fatal("nil view")
	}
	for _, dv := range view.Definitions {
		if dv.Effective != nil {
			t.Errorf("definition %q has Effective populated without a live registry; want nil", dv.Name)
		}
	}
}
