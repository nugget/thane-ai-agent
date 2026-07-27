package tools

import (
	"errors"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func configOwnedSnapshot(name string) *looppkg.DefinitionRegistrySnapshot {
	return &looppkg.DefinitionRegistrySnapshot{
		Definitions: []looppkg.DefinitionSnapshot{{
			Name:   name,
			Source: looppkg.DefinitionSourceConfig,
			Spec:   looppkg.Spec{Name: name, Operation: looppkg.OperationContainer},
		}},
	}
}

// TestEnsureDefinitionMutable covers the three outcomes every caller
// depends on. Config ownership is an invariant of the registry rather
// than of any one tool, and it had six separate implementations before
// this — the risk was never that one was wrong today, but that six
// copies drift without anything failing.
func TestEnsureDefinitionMutable(t *testing.T) {
	snap := configOwnedSnapshot("owned")

	t.Run("config-owned is refused", func(t *testing.T) {
		_, found, err := ensureDefinitionMutable(snap, "owned")
		if !found {
			t.Error("existing definition should be reported as found")
		}
		var immutable *looppkg.ImmutableDefinitionError
		if !errors.As(err, &immutable) {
			t.Fatalf("err = %v, want ImmutableDefinitionError", err)
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		_, found, err := ensureDefinitionMutable(snap, "nobody")
		if err != nil || found {
			t.Errorf("absent = (found %v, err %v), want (false, nil) — the caller is creating", found, err)
		}
	})

	t.Run("nil snapshot is not an error", func(t *testing.T) {
		if _, found, err := ensureDefinitionMutable(nil, "anything"); err != nil || found {
			t.Errorf("nil snapshot = (found %v, err %v), want (false, nil)", found, err)
		}
	})
}

// TestRequireMutableDefinition pins the stricter variant: callers that
// operate on an existing definition need absence to be an error, and the
// immutability check must still come from the same place.
func TestRequireMutableDefinition(t *testing.T) {
	snap := configOwnedSnapshot("owned")

	var immutable *looppkg.ImmutableDefinitionError
	if _, err := requireMutableDefinition(snap, "owned"); !errors.As(err, &immutable) {
		t.Errorf("config-owned err = %v, want ImmutableDefinitionError", err)
	}

	var unknown *looppkg.UnknownDefinitionError
	if _, err := requireMutableDefinition(snap, "nobody"); !errors.As(err, &unknown) {
		t.Errorf("absent err = %v, want UnknownDefinitionError", err)
	}
	if _, err := requireMutableDefinition(nil, "anything"); !errors.As(err, &unknown) {
		t.Errorf("nil snapshot err = %v, want UnknownDefinitionError", err)
	}
}
