package loop

import (
	"testing"
	"time"
)

// TestEvaluateConditionsReturnsFoundFalseForMissingDefinition is
// the regression test for the post-#896 audit's MED finding:
// previously EvaluateConditions collapsed "definition not found"
// into Eligible:true, which let gated call sites (bootstrap,
// LaunchDefinition, ReconcileDefinition) treat a stale snapshot
// or registry race as "safe to spawn." Callers now MUST check the
// returned found bool before reading .Eligible.
func TestEvaluateConditionsReturnsFoundFalseForMissingDefinition(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	status, evals, found := reg.EvaluateConditions("ghost", time.Now())
	if found {
		t.Fatal("found = true for a name that was never registered")
	}
	if status.Eligible {
		t.Errorf("Eligible = true for missing definition; must be the zero status")
	}
	if status.Reason != "" || !status.NextTransitionAt.IsZero() {
		t.Errorf("status = %+v, want zero status when found=false", status)
	}
	if evals != nil {
		t.Errorf("evals = %v, want nil when found=false", evals)
	}
}

// TestEvaluateConditionsTrimsName covers the input-normalization
// invariant: callers passing " leaf " (whitespace around the
// name) should resolve the same definition as "leaf". Upsert,
// AncestorSpecs, and the rest of the registry trim, so
// EvaluateConditions should match. Otherwise a copy-pasted
// definition name with trailing whitespace would silently
// register as "not found" and the new found-bool contract would
// produce a confusing false negative.
func TestEvaluateConditionsTrimsName(t *testing.T) {
	t.Parallel()

	now := time.Now()
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := reg.Upsert(Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
	}, now); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	_, _, found := reg.EvaluateConditions("  leaf  ", now)
	if !found {
		t.Errorf("EvaluateConditions(\"  leaf  \") returned found=false; should trim and resolve")
	}
}

// TestEvaluateConditionsNilReceiver guards the nil-receiver
// contract: callers holding an optional registry handle should
// safely get (zero, nil, false) without panicking. Pairs with
// the nil-tolerance pattern on [AncestorSpecs] and [Get].
func TestEvaluateConditionsNilReceiver(t *testing.T) {
	t.Parallel()

	var reg *DefinitionRegistry
	status, evals, found := reg.EvaluateConditions("anything", time.Now())
	if found {
		t.Error("nil receiver returned found=true")
	}
	if status.Eligible || status.Reason != "" || !status.NextTransitionAt.IsZero() {
		t.Errorf("nil receiver returned non-zero status: %+v", status)
	}
	if evals != nil {
		t.Errorf("nil receiver returned non-nil evals: %v", evals)
	}
}

// TestEvaluateConditionsReturnsFoundTrueForRegisteredDefinition
// covers the happy path: a registered definition returns found=true
// with the live cascade result. Pairs with the not-found case
// above to pin the two-state contract.
func TestEvaluateConditionsReturnsFoundTrueForRegisteredDefinition(t *testing.T) {
	t.Parallel()

	now := time.Now()
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := reg.Upsert(Spec{
		Name:         "leaf",
		Task:         "t",
		Operation:    OperationService,
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
	}, now); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	status, _, found := reg.EvaluateConditions("leaf", now)
	if !found {
		t.Fatal("found = false for a registered definition")
	}
	if !status.Eligible {
		t.Errorf("Eligible = false for an unconditional spec; got %+v", status)
	}
}
