package outputtargets

import (
	"strings"
	"testing"
)

func TestRegistryTargetsAreCoherent(t *testing.T) {
	targets := All()
	if len(targets) == 0 {
		t.Fatal("registry is empty")
	}
	for _, target := range targets {
		if err := target.validate(); err != nil {
			t.Errorf("target %q: %v", target.ID, err)
		}
		if strings.TrimSpace(target.Summary) == "" {
			t.Errorf("target %q has no summary; the model needs to know what surface it is filling", target.ID)
		}
		if strings.TrimSpace(target.Binding) == "" {
			t.Errorf("target %q has no binding text; the operator wiring would be undiscoverable", target.ID)
		}
		for _, slot := range target.Slots {
			if strings.TrimSpace(slot.Description) == "" {
				t.Errorf("target %q slot %q has no description", target.ID, slot.Name)
			}
			if slot.Name != strings.ToLower(slot.Name) {
				t.Errorf("target %q slot %q must be lower_snake_case: it is both a tool parameter and an attribute key", target.ID, slot.Name)
			}
		}
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "registered", id: "apple_watch.rectangular", want: true},
		{name: "surrounding whitespace tolerated", id: "  apple_watch.circular  ", want: true},
		{name: "unknown", id: "apple_watch.trapezoid", want: false},
		{name: "empty", id: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := Lookup(tt.id)
			if ok != tt.want {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tt.id, ok, tt.want)
			}
		})
	}
}

func TestIDsSorted(t *testing.T) {
	ids := IDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("IDs not sorted: %v", ids)
		}
	}
}

func TestTargetValidateRejectsMalformed(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   string
	}{
		{
			name:   "no id",
			target: Target{Slots: []Slot{{Name: "value", Kind: SlotKindText, MaxRunes: 4, Required: true, Primary: true}}},
			want:   "target id is required",
		},
		{
			name:   "no title",
			target: Target{ID: "x"},
			want:   "has no title",
		},
		{
			name:   "no slots",
			target: Target{ID: "x", Title: "X"},
			want:   "declares no slots",
		},
		{
			name: "no primary",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "value", Kind: SlotKindText, MaxRunes: 4},
			}},
			want: "exactly one is required",
		},
		{
			name: "two primaries",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindText, MaxRunes: 4, Required: true, Primary: true},
				{Name: "b", Kind: SlotKindText, MaxRunes: 4, Required: true, Primary: true},
			}},
			want: "declares 2 primary slots",
		},
		{
			name: "duplicate slot",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindText, MaxRunes: 4, Required: true, Primary: true},
				{Name: "a", Kind: SlotKindText, MaxRunes: 4},
			}},
			want: "duplicate slot",
		},
		{
			name: "text slot without budget",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindText, Required: true, Primary: true},
			}},
			want: "needs a positive max_runes",
		},
		{
			name: "non-text primary",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindFraction, Required: true, Primary: true},
			}},
			want: "must be text",
		},
		{
			name: "optional primary",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindText, MaxRunes: 4, Primary: true},
			}},
			want: "must be required",
		},
		{
			name: "budget on a color slot",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindText, MaxRunes: 4, Required: true, Primary: true},
				{Name: "tint", Kind: SlotKindColor, MaxRunes: 7},
			}},
			want: "must not set max_runes",
		},
		{
			name: "unknown kind",
			target: Target{ID: "x", Title: "X", Slots: []Slot{
				{Name: "a", Kind: SlotKindText, MaxRunes: 4, Required: true, Primary: true},
				{Name: "b", Kind: SlotKind("mystery")},
			}},
			want: "unsupported kind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.validate()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestPrimarySlotIsTheState(t *testing.T) {
	for _, target := range All() {
		slot, ok := target.PrimarySlot()
		if !ok {
			t.Fatalf("target %q has no primary slot", target.ID)
		}
		if slot.Name != "value" {
			// Not a hard requirement of the model, but every target so
			// far names its primary slot "value"; a divergence should be
			// a deliberate decision rather than a typo.
			t.Errorf("target %q primary slot is %q, expected the conventional \"value\"", target.ID, slot.Name)
		}
	}
}
