package loop

import documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"

// FacetFormat describes how a projection value is encoded. The document layer
// owns this vocabulary; the loop package keeps aliases for persisted output
// specs and compatibility with callers that author loop definitions.
type FacetFormat = documentfacets.Format

const (
	FacetFormatMarkdown = documentfacets.FormatMarkdown
	FacetFormatPlain    = documentfacets.FormatPlain
	FacetFormatJSON     = documentfacets.FormatJSON
)

// FacetSpec declares one document projection and its encoding.
type FacetSpec = documentfacets.Spec

var validFacetFormats = map[FacetFormat]struct{}{
	FacetFormatMarkdown: {},
	FacetFormatPlain:    {},
	FacetFormatJSON:     {},
}
