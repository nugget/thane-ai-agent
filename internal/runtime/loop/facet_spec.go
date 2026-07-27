package loop

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FacetFormat describes how a facet's value is encoded, which decides
// what a surface can do with it.
//
// Format is a facet property rather than a binding one: it is part of
// how the face is cut, not where it is mounted. A binding then checks
// its own capacity against the facet's format and budget — an entity
// state that holds 255 characters can carry a status_line and cannot
// carry a digest — so a mismatch is refused where it is declared rather
// than truncated where it is displayed.
type FacetFormat string

const (
	// FacetFormatMarkdown is prose with structure. The default, and what
	// a document section wants.
	FacetFormatMarkdown FacetFormat = "markdown"
	// FacetFormatPlain is prose with no markup, safe to speak aloud or
	// drop into a surface that renders nothing.
	FacetFormatPlain FacetFormat = "plain"
	// FacetFormatJSON is a structured value, for consumers that are code
	// rather than readers.
	FacetFormatJSON FacetFormat = "json"
)

// FacetSpec declares one facet and how it is cut.
//
// It decodes from either a bare name or an object, because the common
// case is a name and nothing else — `facets: [status_line, teaser]`
// stays readable, and an author who needs an attribute reaches for the
// longer form only on the facet that needs it.
//
// A facet is identified either by [FacetSpec.Name] — one of the reading
// projections, cut for surfaces that show prose at different sizes — or
// by [FacetSpec.Target], cut for a surface that shows no prose at all.
// The two are alternatives rather than a name with a modifier: a watch
// complication is not a longer status line, it is a different face.
type FacetSpec struct {
	// Name is which reading projection this is: status_line, teaser, or
	// digest. Empty when Target is set.
	Name OutputFacet `yaml:"name,omitempty" json:"name,omitempty"`
	// Target is the registered [outputtargets] surface this facet is cut
	// for, such as "apple_watch.rectangular". The surface's slots, their
	// budgets, and their validation all come from the registry, so an
	// author declares where the value goes and nothing about its shape.
	Target string `yaml:"target,omitempty" json:"target,omitempty"`
	// Format is how the value is encoded. Empty means markdown, or json
	// for a target facet, whose value is always a slot object.
	Format FacetFormat `yaml:"format,omitempty" json:"format,omitempty"`
}

// IsTarget reports whether this facet is cut for a registered surface
// rather than for reading.
func (f FacetSpec) IsTarget() bool {
	return strings.TrimSpace(f.Target) != ""
}

// EffectiveFormat resolves the declared format. A target facet is always
// json — its value is a slot object the registry defines — and every
// other facet defaults to markdown.
func (f FacetSpec) EffectiveFormat() FacetFormat {
	if f.IsTarget() {
		return FacetFormatJSON
	}
	if f.Format == "" {
		return FacetFormatMarkdown
	}
	return f.Format
}

// UnmarshalJSON accepts either "status_line" or {"name":"status_line"}.
func (f *FacetSpec) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, `"`) {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return err
		}
		*f = FacetSpec{Name: OutputFacet(name)}
		return nil
	}
	// A named type breaks the recursion into this method.
	type facetSpecWire FacetSpec
	var wire facetSpecWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("facet must be a name or an object with a name: %w", err)
	}
	*f = FacetSpec(wire)
	return nil
}

// UnmarshalYAML mirrors UnmarshalJSON for config-authored specs.
func (f *FacetSpec) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if err := unmarshal(&name); err == nil {
		*f = FacetSpec{Name: OutputFacet(name)}
		return nil
	}
	type facetSpecWire FacetSpec
	var wire facetSpecWire
	if err := unmarshal(&wire); err != nil {
		return fmt.Errorf("facet must be a name or a mapping with a name: %w", err)
	}
	*f = FacetSpec(wire)
	return nil
}

// MarshalJSON writes the short form when nothing but the name is set, so
// a round trip does not inflate every declaration into an object. A
// target facet always takes the object form: its identity lives in a
// field the short form cannot carry.
func (f FacetSpec) MarshalJSON() ([]byte, error) {
	if f.Format == "" && !f.IsTarget() {
		return json.Marshal(string(f.Name))
	}
	type facetSpecWire FacetSpec
	return json.Marshal(facetSpecWire(f))
}

// validFacetFormats gates the enum in one place.
var validFacetFormats = map[FacetFormat]struct{}{
	FacetFormatMarkdown: {}, FacetFormatPlain: {}, FacetFormatJSON: {},
}
