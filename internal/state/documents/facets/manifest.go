package facets

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// SchemaKey stores the document codec/version in frontmatter.
	SchemaKey = "thane_document"
	// FacetsKey stores the declared compact public projections.
	FacetsKey = "facets"
	// FacetFormatsKey stores non-default encodings as name=format values.
	FacetFormatsKey = "facet_formats"
	// ManagedByKey stores the exact structured mutation tool that owns a
	// document. Generic documents are owned by doc_write.
	ManagedByKey = "managed_by"
	// SchemaV1 is the canonical faceted Markdown codec written today.
	SchemaV1 = "faceted/v1"
)

// Manifest is the durable self-description needed to decode and validate a
// faceted document without knowing which loop or domain produced it.
type Manifest struct {
	Schema    string   `json:"schema"`
	Contract  Contract `json:"contract"`
	ManagedBy string   `json:"managed_by"`
}

// FromFrontmatter decodes a canonical manifest. A missing schema is not an
// error; callers may then use [InferLegacy] for compatibility reads.
func FromFrontmatter(frontmatter map[string][]string) (Manifest, bool, error) {
	schema := first(frontmatter[SchemaKey])
	if schema == "" {
		return Manifest{}, false, nil
	}
	if schema != SchemaV1 {
		return Manifest{}, true, fmt.Errorf("unsupported %s %q", SchemaKey, schema)
	}
	formats := make(map[Name]Format)
	for _, encoded := range frontmatter[FacetFormatsKey] {
		name, rawFormat, ok := strings.Cut(encoded, "=")
		if !ok {
			return Manifest{}, true, fmt.Errorf("invalid %s value %q; expected name=format", FacetFormatsKey, encoded)
		}
		formats[Name(strings.TrimSpace(name))] = Format(strings.TrimSpace(rawFormat))
	}
	contract := Contract{Facets: make([]Spec, 0, len(frontmatter[FacetsKey]))}
	for _, rawName := range frontmatter[FacetsKey] {
		name := Name(strings.TrimSpace(rawName))
		contract.Facets = append(contract.Facets, Spec{Name: name, Format: formats[name]})
	}
	if err := contract.ValidateDefinition(); err != nil {
		return Manifest{}, true, fmt.Errorf("invalid faceted document manifest: %w", err)
	}
	return Manifest{
		Schema:    schema,
		Contract:  contract,
		ManagedBy: first(frontmatter[ManagedByKey]),
	}, true, nil
}

// InferLegacy builds a compatibility manifest from a historical reserved-
// heading envelope. It never writes that inferred manifest; the next
// structured mutation does so through [Manifest.Frontmatter].
func InferLegacy(body string, managedBy string) (Manifest, Payload, bool) {
	payload, found := ParseLegacy(body)
	if !found {
		return Manifest{}, payload, false
	}
	contract := Contract{Facets: []Spec{{Name: StatusLine}}}
	if strings.TrimSpace(payload.Teaser) != "" {
		contract.Facets = append(contract.Facets, Spec{Name: Teaser})
	}
	if strings.TrimSpace(payload.Digest) != "" {
		contract.Facets = append(contract.Facets, Spec{Name: Digest})
	}
	return Manifest{Schema: SchemaV1, Contract: contract, ManagedBy: strings.TrimSpace(managedBy)}, payload, true
}

// Frontmatter returns the canonical reserved keys for this manifest.
func (m Manifest) Frontmatter() map[string][]string {
	values := map[string][]string{
		SchemaKey: {SchemaV1},
	}
	for _, facet := range m.Contract.Facets {
		values[FacetsKey] = append(values[FacetsKey], string(facet.Name))
		if format := facet.EffectiveFormat(); format != FormatMarkdown {
			values[FacetFormatsKey] = append(values[FacetFormatsKey], string(facet.Name)+"="+string(format))
		}
	}
	if owner := strings.TrimSpace(m.ManagedBy); owner != "" {
		values[ManagedByKey] = []string{owner}
	}
	for key := range values {
		sort.Strings(values[key])
	}
	return values
}

// ReservedFrontmatterKey reports whether key belongs to the storage codec and
// should therefore be hidden from ordinary model-facing metadata.
func ReservedFrontmatterKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case SchemaKey, FacetsKey, FacetFormatsKey, ManagedByKey:
		return true
	default:
		return false
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
