package contacts

import (
	"reflect"
	"testing"
)

func TestOriginPolicyFromProperties(t *testing.T) {
	props := []Property{
		{Property: PropertyOriginTag, Value: "signal, projects"},
		{Property: PropertyOriginTag, Type: "email", Value: "email"},
		{Property: PropertyOriginTag, Type: "signal", Value: "ha\nprojects"},
		{Property: PropertyOriginContextRef, Value: "kb:projects/current.md"},
		{Property: PropertyOriginContextRef, Type: "email", Value: "kb:email/thread.md"},
	}

	got := OriginPolicyFromProperties(props, "signal")
	if want := []string{"signal", "projects", "ha"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("Tags = %#v, want %#v", got.Tags, want)
	}
	if want := []string{"kb:projects/current.md"}; !reflect.DeepEqual(got.ContextRefs, want) {
		t.Fatalf("ContextRefs = %#v, want %#v", got.ContextRefs, want)
	}
}

func TestOriginPolicyFromProperties_AllSourceTypes(t *testing.T) {
	props := []Property{
		{Property: PropertyOriginTag, Type: "all", Value: "common"},
		{Property: PropertyOriginTag, Type: "*", Value: "shared"},
		{Property: PropertyOriginTag, Type: "email", Value: "email"},
	}

	got := OriginPolicyFromProperties(props, "signal")
	if want := []string{"common", "shared"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("Tags = %#v, want %#v", got.Tags, want)
	}
}
