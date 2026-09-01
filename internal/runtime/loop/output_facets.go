package loop

import (
	"fmt"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// MaxOutputDocumentBytes is retained for loop-output callers. The document
// layer owns the actual maintained-document readback invariant.
const MaxOutputDocumentBytes = documentfacets.MaxDocumentBytes

// ValidateOutputBodySize delegates the shared maintained-document ceiling to
// the document layer.
func ValidateOutputBodySize(body string) error {
	return documentfacets.ValidateBodySize(body)
}

// FacetPayload is the loop-facing compatibility shape for a logical document
// payload. Its semantics and codec are owned by the document layer.
type FacetPayload documentfacets.Payload

// FacetField describes one structured projection field.
type FacetField = documentfacets.Field

// FacetFields returns the output's declared fields in canonical order.
func (o OutputSpec) FacetFields() []FacetField {
	if len(o.Facets) == 0 {
		return nil
	}
	return o.facetContract().Fields()
}

// FacetFieldByKey returns canonical metadata for one projection.
func FacetFieldByKey(key string) (FacetField, bool) {
	return documentfacets.FieldByKey(key)
}

// HasFacets reports whether this maintained output publishes projections.
func (o OutputSpec) HasFacets() bool {
	return o.Type == OutputTypeMaintainedDocument && len(o.Facets) > 0
}

// ValidateFacetPayload validates a complete projection set through the shared
// document contract.
func (o OutputSpec) ValidateFacetPayload(payload FacetPayload) error {
	if !o.HasFacets() {
		return fmt.Errorf("output %q does not declare facets", o.Name)
	}
	return o.facetContract().Validate(documentfacets.Payload(payload))
}

// RenderFacetDocument encodes projections with the shared Markdown codec.
func (o OutputSpec) RenderFacetDocument(payload FacetPayload) string {
	return o.facetContract().Render(documentfacets.Payload(payload))
}

// RenderFacetScaffold renders the output's first-cycle placeholder.
func (o OutputSpec) RenderFacetScaffold() string {
	return o.facetContract().RenderScaffold()
}

// ParseFacetDocument decodes a document using this output's declared formats.
func (o OutputSpec) ParseFacetDocument(body string) FacetPayload {
	return FacetPayload(o.facetContract().Parse(body))
}

// ParseFacetSections recognizes the legacy and canonical section envelope
// without a manifest. New document-layer consumers should prefer the durable
// manifest when one is present.
func ParseFacetSections(body string) (FacetPayload, bool) {
	payload, found := documentfacets.ParseLegacy(body)
	return FacetPayload(payload), found
}

// FacetByKey returns one projection by its model-facing key.
func (p FacetPayload) FacetByKey(key string) (string, bool) {
	return documentfacets.Payload(p).ByKey(key)
}

// FacetKeys returns the complete logical read ladder.
func FacetKeys() []string {
	return documentfacets.Keys()
}

// FormatGuidance returns model-facing guidance for non-default encodings.
func FormatGuidance(format FacetFormat) string {
	return documentfacets.FormatGuidance(format)
}

// FacetPayloadFromArgs decodes structured tool arguments according to this
// output's declared contract.
func (o OutputSpec) FacetPayloadFromArgs(args map[string]any) (FacetPayload, error) {
	payload, err := o.facetContract().FromArgs(args)
	return FacetPayload(payload), err
}

// IsFacetKey reports whether key names a readable document projection.
func IsFacetKey(key string) bool {
	return documentfacets.IsKey(key)
}

func (o OutputSpec) facetContract() documentfacets.Contract {
	return documentfacets.Contract{Facets: append([]documentfacets.Spec(nil), o.Facets...)}
}
