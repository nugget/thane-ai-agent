package outputtargets

import (
	"fmt"
	"sort"
	"strings"
)

// SlotKind classifies what a slot accepts and how it is validated.
type SlotKind string

const (
	// SlotKindText is a short display string constrained by
	// [Slot.MaxRunes].
	SlotKindText SlotKind = "text"
	// SlotKindFraction is a number in [0.0, 1.0] driving a gauge, ring,
	// or progress bar fill.
	SlotKindFraction SlotKind = "fraction"
	// SlotKindColor is an "#RRGGBB" hex color.
	SlotKindColor SlotKind = "color"
)

// Slot is one named position in a target's layout.
type Slot struct {
	// Name is the slot identifier. It is both the tool parameter name
	// and the payload attribute key, so it stays lower_snake_case.
	Name string `json:"name"`
	// Kind selects the validation rules applied to this slot's value.
	Kind SlotKind `json:"kind"`
	// Description is model-facing: what this slot renders as on the
	// target surface, in enough detail to choose a good value.
	Description string `json:"description"`
	// Required marks a slot the target cannot render without. The
	// primary slot is always required.
	Required bool `json:"required,omitempty"`
	// MaxRunes bounds a text slot's length in runes (not bytes). Zero
	// means unbounded and is only valid for non-text kinds.
	MaxRunes int `json:"max_runes,omitempty"`
	// Primary marks the slot whose value becomes the payload state
	// rather than an attribute. Exactly one slot per target sets it.
	Primary bool `json:"primary,omitempty"`
}

// Target is one renderable surface with a fixed slot layout.
type Target struct {
	// ID is the stable identifier a loop output declares, such as
	// "apple_watch.rectangular".
	ID string `json:"id"`
	// Title is the human-readable target name for operator surfaces.
	Title string `json:"title"`
	// Summary tells the model what this surface is and what shape of
	// content survives on it.
	Summary string `json:"summary"`
	// Binding explains how the consuming surface reads the published
	// payload, so the operator wiring is discoverable from the contract
	// rather than from tribal knowledge.
	Binding string `json:"binding"`
	// Icon is the Material Design Icons name published with the sink's
	// entity registration. Empty leaves the sink default.
	Icon string `json:"icon,omitempty"`
	// Slots are the target's positions in render order.
	Slots []Slot `json:"slots"`
}

// PrimarySlot returns the slot whose value becomes the payload state.
// Every registered target has one; the zero [Slot] and false are
// returned for a target that does not, which [Target.validate] rejects at
// registration.
func (t Target) PrimarySlot() (Slot, bool) {
	for _, slot := range t.Slots {
		if slot.Primary {
			return slot, true
		}
	}
	return Slot{}, false
}

// Slot returns the named slot.
func (t Target) Slot(name string) (Slot, bool) {
	for _, slot := range t.Slots {
		if slot.Name == name {
			return slot, true
		}
	}
	return Slot{}, false
}

// SlotNames returns every slot name in render order, for error messages
// that need to enumerate the valid parameters.
func (t Target) SlotNames() []string {
	names := make([]string, 0, len(t.Slots))
	for _, slot := range t.Slots {
		names = append(names, slot.Name)
	}
	return names
}

// validate reports whether a target is internally coherent. It runs at
// package init over the built-in registry, so a malformed target is a
// build-time-adjacent failure (the first test or process start) rather
// than a runtime surprise in a loop handler.
func (t Target) validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("target id is required")
	}
	if len(t.Slots) == 0 {
		return fmt.Errorf("target %q declares no slots", t.ID)
	}
	seen := make(map[string]struct{}, len(t.Slots))
	primaries := 0
	for _, slot := range t.Slots {
		if strings.TrimSpace(slot.Name) == "" {
			return fmt.Errorf("target %q has a slot with no name", t.ID)
		}
		if _, dup := seen[slot.Name]; dup {
			return fmt.Errorf("target %q declares duplicate slot %q", t.ID, slot.Name)
		}
		seen[slot.Name] = struct{}{}
		switch slot.Kind {
		case SlotKindText:
			if slot.MaxRunes <= 0 {
				return fmt.Errorf("target %q slot %q is text and needs a positive max_runes", t.ID, slot.Name)
			}
		case SlotKindFraction, SlotKindColor:
			if slot.MaxRunes != 0 {
				return fmt.Errorf("target %q slot %q is %s and must not set max_runes", t.ID, slot.Name, slot.Kind)
			}
		default:
			return fmt.Errorf("target %q slot %q has unsupported kind %q", t.ID, slot.Name, slot.Kind)
		}
		if slot.Primary {
			primaries++
			if slot.Kind != SlotKindText {
				return fmt.Errorf("target %q primary slot %q must be text; the payload state is a scalar string", t.ID, slot.Name)
			}
			if !slot.Required {
				return fmt.Errorf("target %q primary slot %q must be required", t.ID, slot.Name)
			}
		}
	}
	if primaries != 1 {
		return fmt.Errorf("target %q declares %d primary slots; exactly one is required", t.ID, primaries)
	}
	return nil
}

// registry holds every built-in target keyed by ID. It is populated once
// at init and never mutated, so no lock is needed for the read paths.
var registry = func() map[string]Target {
	targets := []Target{
		appleWatchRectangular,
		appleWatchCircular,
	}
	out := make(map[string]Target, len(targets))
	for _, target := range targets {
		if err := target.validate(); err != nil {
			panic("outputtargets: invalid built-in target: " + err.Error())
		}
		if _, dup := out[target.ID]; dup {
			panic("outputtargets: duplicate built-in target id " + target.ID)
		}
		out[target.ID] = target
	}
	return out
}()

// Lookup returns the registered target with the given ID.
func Lookup(id string) (Target, bool) {
	target, ok := registry[strings.TrimSpace(id)]
	return target, ok
}

// IDs returns every registered target ID in sorted order. Callers use it
// to enumerate valid choices in schemas and in errors that reject an
// unknown target.
func IDs() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// All returns every registered target sorted by ID.
func All() []Target {
	ids := IDs()
	targets := make([]Target, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, registry[id])
	}
	return targets
}
