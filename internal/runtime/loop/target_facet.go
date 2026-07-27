package loop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
)

// Facets cut for a rendering surface rather than for a reader.
//
// A reading projection is prose with a budget, and the contract in
// output_facets.go says everything there is to say about it. A target
// facet says almost nothing itself: the surface it is cut for is
// registered in [outputtargets], and its slots, their types, their
// budgets, and the words the model reads all come from there. What lives
// here is only the seam — how a registered surface becomes a section of
// the document, and how the arguments that fill it become the value that
// is stored.

// targetSection builds the section for a facet cut for a registered
// surface. Everything about it — the heading, the argument name, what
// the model is told, what its values must satisfy — comes from the
// registry, so adding a surface adds no code here.
//
// An unregistered ID yields no section. Spec validation rejects one at
// declaration and on every load, so reaching this with an unknown ID
// means a target was removed from the registry beneath a stored spec;
// the loop then fails validation loudly rather than publishing a section
// nothing can consume.
func targetSection(id string) (facetSection, bool) {
	target, ok := outputtargets.Lookup(id)
	if !ok {
		return facetSection{}, false
	}
	return facetSection{
		Heading: target.Title,
		Field: FacetField{
			Key:      target.ArgKey(),
			Format:   FacetFormatJSON,
			Target:   target.ID,
			Guidance: target.Summary,
		},
		get: func(p FacetPayload) string { return p.Targets[target.ID] },
		set: func(p *FacetPayload, v string) {
			if p.Targets == nil {
				p.Targets = make(map[string]string, 1)
			}
			p.Targets[target.ID] = v
		},
	}, true
}

// FacetPayloadFromArgs reads publish-tool arguments into a payload.
//
// It lives with the contract rather than with the tool that calls it,
// because turning arguments into a payload is the same question as what
// a facet accepts: a reading projection takes a string, and a target
// facet takes the surface's slot object, normalized by the registry
// before it is stored. A second reading of those arguments anywhere else
// would be a second answer.
//
// A missing argument is left empty rather than rejected here, so
// [OutputSpec.ValidateFacetPayload] reports every omission at once
// instead of one per attempt.
func (o OutputSpec) FacetPayloadFromArgs(args map[string]any) (FacetPayload, error) {
	var payload FacetPayload
	for _, section := range o.sections() {
		field := section.Field
		raw, present := args[field.Key]
		if !present {
			continue
		}
		if field.Target != "" {
			slots, err := targetSlotsFromArg(field, raw)
			if err != nil {
				return FacetPayload{}, err
			}
			section.set(&payload, slots)
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return FacetPayload{}, fmt.Errorf("%s must be a string", field.Key)
		}
		section.set(&payload, value)
	}
	return payload, nil
}

// targetSlotsFromArg turns one target facet's argument into the JSON slot
// object stored in the document.
//
// The slots the model sent are what is kept, not the payload a sink would
// derive from them: the document is the canonical store, and a stored
// slot set can be re-normalized for any binding, while a stored binding
// payload has already thrown away which slot each value came from.
// Normalization still runs, because a value that cannot be published is
// one the model should hear about now rather than at the far end.
func targetSlotsFromArg(field FacetField, raw any) (string, error) {
	target, ok := outputtargets.Lookup(field.Target)
	if !ok {
		return "", fmt.Errorf("%s is cut for target %q, which is not registered", field.Key, field.Target)
	}
	slots, ok := raw.(map[string]any)
	if !ok {
		// Some model families send a nested object as its JSON text.
		// Accepting that costs one branch and saves a turn.
		text, isText := raw.(string)
		if !isText {
			return "", fmt.Errorf("%s must be an object of slot values for %s; valid slots are %s", field.Key, target.Title, strings.Join(target.SlotNames(), ", "))
		}
		if err := json.Unmarshal([]byte(text), &slots); err != nil {
			return "", fmt.Errorf("%s must be an object of slot values for %s, and the string sent is not JSON either: %w", field.Key, target.Title, err)
		}
	}
	canonical, err := target.NormalizeSlots(slots)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field.Key, err)
	}
	// Encoded from the canonical slot map rather than echoed, so the
	// document holds one spelling of a given payload regardless of key
	// order, stray whitespace, or hex casing on the way in.
	encoded, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%s: %w", field.Key, err)
	}
	return string(encoded), nil
}
