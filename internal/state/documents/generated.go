package documents

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// GeneratedFieldBy is the frontmatter field naming the subsystem or
	// loop that produced a generated document.
	GeneratedFieldBy = "generated_by"
	// GeneratedFieldAt is the frontmatter field recording when a generated
	// document was produced.
	GeneratedFieldAt = "generated_at"
	// GeneratedFieldSourceRefs is the frontmatter field listing source
	// references used to produce a generated document.
	GeneratedFieldSourceRefs = "source_refs"
	// GeneratedFieldDocumentKind is the frontmatter field describing the
	// semantic kind of generated document.
	GeneratedFieldDocumentKind = "document_kind"
	// GeneratedFieldRefreshStrategy is the frontmatter field describing how
	// repeated generation should treat the document.
	GeneratedFieldRefreshStrategy = "refresh_strategy"
	// GeneratedFieldManagedRoot is the optional frontmatter field naming the
	// managed root prefix when the writer knows it.
	GeneratedFieldManagedRoot = "managed_root"
)

const (
	// RefreshStrategyImmutable marks generated documents that should be
	// written once and left unchanged.
	RefreshStrategyImmutable = "immutable"
	// RefreshStrategyReplace marks generated documents that are replaced in
	// full on refresh.
	RefreshStrategyReplace = "replace"
	// RefreshStrategyAppend marks generated documents that grow by appending
	// new entries.
	RefreshStrategyAppend = "append"
	// RefreshStrategyRollingWindow marks generated documents that keep a
	// bounded recent window.
	RefreshStrategyRollingWindow = "rolling-window"
)

const (
	// DocumentKindMediaAnalysis identifies a generated media analysis page.
	DocumentKindMediaAnalysis = "media_analysis"
	// DocumentKindMediaChannelIndex identifies an automatically maintained
	// media channel index page.
	DocumentKindMediaChannelIndex = "media_channel_index"
)

// GeneratedMetadata is the document-local provenance contract for generated
// markdown artifacts in managed document roots. It intentionally uses only
// flat frontmatter fields so the document index can filter and expose it
// without nested YAML support.
type GeneratedMetadata struct {
	GeneratedBy     string    `json:"generated_by" yaml:"generated_by"`
	GeneratedAt     time.Time `json:"generated_at" yaml:"generated_at"`
	SourceRefs      []string  `json:"source_refs,omitempty" yaml:"source_refs,omitempty"`
	DocumentKind    string    `json:"document_kind" yaml:"document_kind"`
	RefreshStrategy string    `json:"refresh_strategy" yaml:"refresh_strategy"`
	ManagedRoot     string    `json:"managed_root,omitempty" yaml:"managed_root,omitempty"`
}

// Validate reports whether metadata has the minimum fields required for a
// generated document to be machine-legible.
func (m GeneratedMetadata) Validate() error {
	if strings.TrimSpace(m.GeneratedBy) == "" {
		return fmt.Errorf("%s is required", GeneratedFieldBy)
	}
	if m.GeneratedAt.IsZero() {
		return fmt.Errorf("%s is required", GeneratedFieldAt)
	}
	if strings.TrimSpace(m.DocumentKind) == "" {
		return fmt.Errorf("%s is required", GeneratedFieldDocumentKind)
	}
	if strings.TrimSpace(m.RefreshStrategy) == "" {
		return fmt.Errorf("%s is required", GeneratedFieldRefreshStrategy)
	}
	return nil
}

// Frontmatter returns the generated-document metadata as index-compatible
// frontmatter values. Empty optional fields are omitted.
func (m GeneratedMetadata) Frontmatter() map[string][]string {
	meta := make(map[string][]string, 6)
	if value := strings.TrimSpace(m.GeneratedBy); value != "" {
		meta[GeneratedFieldBy] = []string{value}
	}
	if !m.GeneratedAt.IsZero() {
		meta[GeneratedFieldAt] = []string{m.GeneratedAt.UTC().Format(time.RFC3339)}
	}
	if values := normalizeFrontmatterValues(m.SourceRefs); len(values) > 0 {
		meta[GeneratedFieldSourceRefs] = values
	}
	if value := strings.TrimSpace(m.DocumentKind); value != "" {
		meta[GeneratedFieldDocumentKind] = []string{value}
	}
	if value := strings.TrimSpace(m.RefreshStrategy); value != "" {
		meta[GeneratedFieldRefreshStrategy] = []string{value}
	}
	if value := strings.TrimSpace(m.ManagedRoot); value != "" {
		meta[GeneratedFieldManagedRoot] = []string{value}
	}
	return meta
}

// RenderGeneratedFrontmatter renders generated-document metadata as YAML
// frontmatter lines suitable for insertion into a larger frontmatter block.
func RenderGeneratedFrontmatter(m GeneratedMetadata) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	meta := m.Frontmatter()
	lines := make([]string, 0, len(meta)+len(meta[GeneratedFieldSourceRefs]))
	for _, key := range []string{
		GeneratedFieldBy,
		GeneratedFieldAt,
		GeneratedFieldDocumentKind,
		GeneratedFieldRefreshStrategy,
		GeneratedFieldSourceRefs,
		GeneratedFieldManagedRoot,
	} {
		values := meta[key]
		if len(values) == 0 {
			continue
		}
		if key == GeneratedFieldSourceRefs {
			lines = appendBlockListFrontmatter(lines, key, values)
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", key, strconv.Quote(values[0])))
	}
	return strings.Join(lines, "\n"), nil
}
