package loop

import (
	"strings"
	"testing"
	"time"
)

// TestUpsertRejectsContainerWithServiceParent is the regression
// test for the LOW finding in the post-#894 audit: a container
// whose parent_name resolves to a non-container would silently
// lose its inheritance chain (the cascade walk only flows
// through container ancestors). Catch it at write time.
func TestUpsertRejectsContainerWithServiceParent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	// Seed a service that some future operator might try to make
	// a container's parent.
	parentSvc := Spec{
		Name:         "background_worker",
		Operation:    OperationService,
		Task:         "t",
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
	}
	if err := reg.Upsert(parentSvc, now); err != nil {
		t.Fatalf("upsert service: %v", err)
	}

	// Now try to push a container that names the service as its parent.
	badContainer := Spec{
		Name:       "home_automation",
		Operation:  OperationContainer,
		ParentName: "background_worker",
	}
	err = reg.Upsert(badContainer, now)
	if err == nil {
		t.Fatal("Upsert accepted container with service parent; want rejection")
	}
	if !strings.Contains(err.Error(), "container") || !strings.Contains(err.Error(), "must themselves be containers") {
		t.Errorf("err = %v, should explain the container-parent constraint", err)
	}
}

// TestUpsertAcceptsContainerWithContainerParent covers the happy
// path — nested containers are valid and stored normally.
func TestUpsertAcceptsContainerWithContainerParent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	if err := reg.Upsert(Spec{Name: "outer", Operation: OperationContainer}, now); err != nil {
		t.Fatalf("upsert outer: %v", err)
	}
	nested := Spec{
		Name:       "inner",
		Operation:  OperationContainer,
		ParentName: "outer",
	}
	if err := reg.Upsert(nested, now); err != nil {
		t.Fatalf("upsert inner: %v", err)
	}
}

// TestUpsertAllowsContainerParentNameForwardReference covers an
// awkward but legal case: a container declares parent_name
// pointing at a container that hasn't been upserted yet. The
// cross-spec check uses specByName which returns ok=false for
// missing parents — we let the upsert through and rely on
// hydration / topological sort to surface the missing parent at
// startup. Catching it loudly at write time would block bulk
// imports where children land before parents.
func TestUpsertAllowsContainerParentNameForwardReference(t *testing.T) {
	t.Parallel()

	now := time.Now()
	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	// Child upserted first, parent name points at a not-yet-
	// registered container. Should succeed.
	if err := reg.Upsert(Spec{
		Name:       "inner",
		Operation:  OperationContainer,
		ParentName: "outer_not_yet_pushed",
	}, now); err != nil {
		t.Fatalf("Upsert with forward parent ref: %v", err)
	}
}
