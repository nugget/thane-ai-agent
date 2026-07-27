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
type FacetSpec struct {
	// Name is which face this is: status_line, teaser, or digest.
	Name OutputFacet `yaml:"name" json:"name"`
	// Format is how the value is encoded. Empty means markdown.
	Format FacetFormat `yaml:"format,omitempty" json:"format,omitempty"`
}

// EffectiveFormat resolves the declared format, defaulting to markdown.
func (f FacetSpec) EffectiveFormat() FacetFormat {
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
// a round trip does not inflate every declaration into an object.
func (f FacetSpec) MarshalJSON() ([]byte, error) {
	if f.Format == "" {
		return json.Marshal(string(f.Name))
	}
	type facetSpecWire FacetSpec
	return json.Marshal(facetSpecWire(f))
}

// validFacetFormats gates the enum in one place.
var validFacetFormats = map[FacetFormat]struct{}{
	FacetFormatMarkdown: {}, FacetFormatPlain: {}, FacetFormatJSON: {},
}
