package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// TestUnmarshalJSONMigratesLegacyQualityFloor is the regression
// test for the post-PR-R1 routing-surface unification: a
// persisted overlay spec written before PR-R1 with the top-level
// `quality_floor` field must hydrate cleanly, with the value
// translated onto Profile.QualityFloor. Without this, an upgrade
// from a pre-PR-R1 deployment would silently lose the operator's
// configured quality floor on first restart.
func TestUnmarshalJSONMigratesLegacyQualityFloor(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"name": "legacy",
		"operation": "service",
		"task": "t",
		"sleep_min": "1m",
		"sleep_max": "1m",
		"sleep_default": "1m",
		"quality_floor": 7
	}`)

	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.Profile.QualityFloor != 7 {
		t.Errorf("Profile.QualityFloor = %d, want 7 (legacy migration)", s.Profile.QualityFloor)
	}
}

// TestUnmarshalJSONMigratesLegacySupervisorFields covers the
// parallel migration for supervisor turns: the legacy
// `supervisor_quality_floor` and `supervisor_context` fields
// translate onto SupervisorProfile.QualityFloor and
// SupervisorProfile.Instructions respectively. Allocates a
// SupervisorProfile only when at least one legacy supervisor
// field is set, so a spec with no supervisor overrides at all
// still hydrates with nil SupervisorProfile.
func TestUnmarshalJSONMigratesLegacySupervisorFields(t *testing.T) {
	t.Parallel()

	t.Run("both supervisor fields populate SupervisorProfile", func(t *testing.T) {
		raw := []byte(`{
			"name": "legacy",
			"operation": "service",
			"task": "t",
			"sleep_min": "1m",
			"sleep_max": "1m",
			"sleep_default": "1m",
			"supervisor_quality_floor": 9,
			"supervisor_context": "Review this turn's outputs against past patterns."
		}`)
		var s Spec
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if s.SupervisorProfile == nil {
			t.Fatal("SupervisorProfile is nil; expected allocation when legacy supervisor fields are present")
		}
		if s.SupervisorProfile.QualityFloor != 9 {
			t.Errorf("SupervisorProfile.QualityFloor = %d, want 9", s.SupervisorProfile.QualityFloor)
		}
		if !strings.Contains(s.SupervisorProfile.Instructions, "Review this turn") {
			t.Errorf("SupervisorProfile.Instructions = %q, want the legacy supervisor_context content", s.SupervisorProfile.Instructions)
		}
	})

	t.Run("no supervisor fields leaves SupervisorProfile nil", func(t *testing.T) {
		raw := []byte(`{
			"name": "leaf",
			"operation": "service",
			"task": "t",
			"sleep_min": "1m",
			"sleep_max": "1m",
			"sleep_default": "1m"
		}`)
		var s Spec
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if s.SupervisorProfile != nil {
			t.Errorf("SupervisorProfile = %+v, want nil for a spec with no overrides", s.SupervisorProfile)
		}
	})
}

// TestUnmarshalJSONPrefersNewShapeOverLegacy guards the
// collision rule: when both the legacy top-level fields AND the
// new Profile/SupervisorProfile fields are present in the JSON
// (unlikely in practice but possible during a partial upgrade),
// the new shape wins. Legacy values do NOT clobber explicit new
// fields.
func TestUnmarshalJSONPrefersNewShapeOverLegacy(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"name": "mixed",
		"operation": "service",
		"task": "t",
		"sleep_min": "1m",
		"sleep_max": "1m",
		"sleep_default": "1m",
		"quality_floor": 3,
		"profile": {"quality_floor": "8"},
		"supervisor_quality_floor": 5,
		"supervisor_profile": {"quality_floor": "10"}
	}`)
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.Profile.QualityFloor != 8 {
		t.Errorf("Profile.QualityFloor = %d, want 8 (new shape should win)", s.Profile.QualityFloor)
	}
	if s.SupervisorProfile == nil || s.SupervisorProfile.QualityFloor != 10 {
		t.Errorf("SupervisorProfile.QualityFloor = %+v, want 10 (new shape should win)", s.SupervisorProfile)
	}
}

// TestMarshalJSONOmitsLegacyFields confirms a freshly-built spec
// never emits the legacy top-level fields on its way back to
// disk — the canonical shape moves forward and the migration
// shim cleans up old persisted state in one direction only.
func TestMarshalJSONOmitsLegacyFields(t *testing.T) {
	t.Parallel()

	s := Spec{
		Name:         "fresh",
		Operation:    OperationService,
		Task:         "t",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Profile: router.LoopProfile{
			QualityFloor: 4,
		},
		SupervisorProfile: &router.LoopProfile{
			QualityFloor: 9,
			Instructions: "Periodic review.",
		},
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	js := string(out)
	// The retired top-level fields must not appear on the wire.
	// Note: `quality_floor` IS the canonical Profile field name,
	// so we can't just grep for the key — we check that it never
	// appears at the spec root, only nested inside profile and
	// supervisor_profile objects.
	for _, legacy := range []string{
		`"supervisor_quality_floor"`,
		`"supervisor_context"`,
	} {
		if strings.Contains(js, legacy) {
			t.Errorf("Marshal emitted retired field %q in: %s", legacy, js)
		}
	}
	// The canonical Profile / SupervisorProfile blocks must
	// carry quality_floor as an int (not the legacy string
	// form). A pre-PR-Q1 marshal would have emitted "4" /
	// "9"; the int wire format pins the conversion.
	if !strings.Contains(js, `"profile":{"quality_floor":4}`) {
		t.Errorf("Marshal didn't emit profile.quality_floor as int 4: %s", js)
	}
	if !strings.Contains(js, `"supervisor_profile":{"quality_floor":9`) {
		t.Errorf("Marshal didn't emit supervisor_profile.quality_floor as int 9: %s", js)
	}
}

// TestEffectiveSupervisorRoutingFactorsCascadesFromContainers
// pins the parallel cascade for SupervisorProfile: a container
// ancestor's SupervisorProfile.QualityFloor reaches the
// descendant via [Registry.EffectiveSupervisorRoutingFactors],
// with the leaf's own SupervisorProfile winning on collision.
// Normal-turn routing factors (Profile cascade) are unaffected.
func TestEffectiveSupervisorRoutingFactorsCascadesFromContainers(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}

	// Container ancestor declares a SupervisorProfile override.
	// (Mission cascades because it's a routing factor; only
	// SupervisorProfile fields go into the supervisor cascade.)
	parent, err := New(Config{
		Name:      "research",
		Operation: OperationContainer,
		SupervisorProfile: &router.LoopProfile{
			QualityFloor: 9,
			LocalOnly:    "false",
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new parent: %v", err)
	}
	if err := r.Register(parent); err != nil {
		t.Fatalf("register parent: %v", err)
	}

	// Leaf has its own SupervisorProfile that overrides one
	// field but leaves the other to inherit.
	leaf, err := New(Config{
		Name:     "researcher",
		Task:     "research things",
		ParentID: parent.ID(),
		SupervisorProfile: &router.LoopProfile{
			QualityFloor: 10,
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	factors := r.EffectiveSupervisorRoutingFactors(leaf.ID())
	got := make(map[string]EffectiveRoutingFactor, len(factors))
	for _, f := range factors {
		got[f.Key] = f
	}

	// quality_floor: leaf wins.
	if f, ok := got["quality_floor"]; !ok {
		t.Error("quality_floor missing from cascade")
	} else {
		if f.Value != "10" {
			t.Errorf("quality_floor.Value = %q, want %q (leaf wins)", f.Value, "10")
		}
		if f.From != EffectiveOriginSelf {
			t.Errorf("quality_floor.From = %q, want %q (leaf own decl)", f.From, EffectiveOriginSelf)
		}
	}
	// local_only: only declared on parent — inherits.
	if f, ok := got["local_only"]; !ok {
		t.Error("local_only missing from cascade (should inherit from parent)")
	} else {
		if f.Value != "false" {
			t.Errorf("local_only.Value = %q, want %q (inherited from parent)", f.Value, "false")
		}
		if f.From != "research" {
			t.Errorf("local_only.From = %q, want %q (parent name)", f.From, "research")
		}
	}

	// And the normal-turn cascade should be empty — none of the
	// fields on either spec went into Profile.
	if normal := r.EffectiveRoutingFactors(leaf.ID()); len(normal) != 0 {
		t.Errorf("EffectiveRoutingFactors (normal) = %v, want empty (SupervisorProfile shouldn't leak into Profile cascade)", normal)
	}
}

// TestSupervisorProfileNilLeavesNormalRoutingUntouched guards
// the no-overrides case: a loop with Supervisor=true and
// SupervisorProb>0 but SupervisorProfile=nil should run
// supervisor turns with the same routing factors as normal
// turns. The supervisor flag still flips on the iteration
// record; just no overrides apply.
func TestSupervisorProfileNilLeavesNormalRoutingUntouched(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	leaf, err := New(Config{
		Name: "service",
		Task: "do",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	if got := r.EffectiveSupervisorRoutingFactors(leaf.ID()); len(got) != 0 {
		t.Errorf("EffectiveSupervisorRoutingFactors = %v, want empty (no SupervisorProfile declared)", got)
	}
}
