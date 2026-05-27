package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestWithChannelSubjects_InjectsBoth(t *testing.T) {
	binding := &memory.ChannelBinding{
		ContactID: "c-abc",
		Address:   "+15551234567",
	}
	ctx := withChannelSubjects(context.Background(), binding)

	got := knowledge.SubjectsFromContext(ctx)
	want := []string{"contact:c-abc", "contact:+15551234567"}
	if len(got) != len(want) {
		t.Fatalf("subjects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("subjects[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWithChannelSubjects_PreservesExistingThenAppends(t *testing.T) {
	base := knowledge.WithSubjects(context.Background(), []string{
		"entity:binary_sensor.driveway",
		"contact:c-abc", // duplicate of what binding would add — should not duplicate
	})
	binding := &memory.ChannelBinding{
		ContactID: "c-abc",
		Address:   "+15551234567",
	}
	ctx := withChannelSubjects(base, binding)

	got := knowledge.SubjectsFromContext(ctx)
	want := []string{"entity:binary_sensor.driveway", "contact:c-abc", "contact:+15551234567"}
	if len(got) != len(want) {
		t.Fatalf("subjects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("subjects[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWithChannelSubjects_NilBindingIsNoop(t *testing.T) {
	ctx := withChannelSubjects(context.Background(), nil)
	if got := knowledge.SubjectsFromContext(ctx); got != nil {
		t.Errorf("expected nil subjects, got %v", got)
	}
}

func TestWithChannelSubjects_NilBindingPreservesExisting(t *testing.T) {
	base := knowledge.WithSubjects(context.Background(), []string{"entity:light.office"})
	ctx := withChannelSubjects(base, nil)
	got := knowledge.SubjectsFromContext(ctx)
	if len(got) != 1 || got[0] != "entity:light.office" {
		t.Errorf("subjects = %v, want [entity:light.office]", got)
	}
}

func TestWithChannelSubjects_EmptyBindingFieldsAreSkipped(t *testing.T) {
	binding := &memory.ChannelBinding{Channel: "signal"} // no ContactID, no Address
	ctx := withChannelSubjects(context.Background(), binding)
	if got := knowledge.SubjectsFromContext(ctx); got != nil {
		t.Errorf("expected nil subjects when binding has no id/address, got %v", got)
	}
}

func TestAppendUniqueSubject(t *testing.T) {
	got := appendUniqueSubject([]string{"a", "b"}, "a")
	if len(got) != 2 {
		t.Errorf("expected 'a' to be deduplicated, got %v", got)
	}
	got = appendUniqueSubject([]string{"a", "b"}, "c")
	if len(got) != 3 || got[2] != "c" {
		t.Errorf("expected 'c' appended, got %v", got)
	}
}
