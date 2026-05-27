package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestWithRequestSubjects_InjectsBoth(t *testing.T) {
	req := &Request{
		ChannelBinding: &memory.ChannelBinding{
			ContactID: "c-abc",
			Address:   "+15551234567",
		},
	}
	ctx := withRequestSubjects(context.Background(), req)

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

func TestWithRequestSubjects_PreservesExistingThenAppends(t *testing.T) {
	base := knowledge.WithSubjects(context.Background(), []string{
		"entity:binary_sensor.driveway",
		"contact:c-abc", // duplicate of what binding would add — should not duplicate
	})
	req := &Request{
		ChannelBinding: &memory.ChannelBinding{
			ContactID: "c-abc",
			Address:   "+15551234567",
		},
	}
	ctx := withRequestSubjects(base, req)

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

func TestWithRequestSubjects_NilBindingIsNoop(t *testing.T) {
	req := &Request{}
	ctx := withRequestSubjects(context.Background(), req)
	if got := knowledge.SubjectsFromContext(ctx); got != nil {
		t.Errorf("expected nil subjects, got %v", got)
	}
}

func TestWithRequestSubjects_NilBindingPreservesExisting(t *testing.T) {
	base := knowledge.WithSubjects(context.Background(), []string{"entity:light.office"})
	req := &Request{}
	ctx := withRequestSubjects(base, req)
	got := knowledge.SubjectsFromContext(ctx)
	if len(got) != 1 || got[0] != "entity:light.office" {
		t.Errorf("subjects = %v, want [entity:light.office]", got)
	}
}

func TestWithRequestSubjects_EmptyBindingFieldsAreSkipped(t *testing.T) {
	req := &Request{
		ChannelBinding: &memory.ChannelBinding{
			Channel: "signal", // present but no ContactID or Address
		},
	}
	ctx := withRequestSubjects(context.Background(), req)
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
