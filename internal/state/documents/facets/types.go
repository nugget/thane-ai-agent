// Package facets defines the logical projection contract for managed
// documents. Markdown is the persistence codec; callers read and write
// projections without depending on its reserved section envelope.
package facets

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// Name identifies one compact public projection of a document. Full is the
// document's substance and is therefore implicit rather than a declared facet.
type Name string

const (
	// StatusLine is the ambient one-line projection.
	StatusLine Name = "status_line"
	// Teaser is the short reason to open the document.
	Teaser Name = "teaser"
	// Digest is the standalone actionable summary.
	Digest Name = "digest"
)

// Format describes how a projection value is encoded.
type Format string

const (
	// FormatMarkdown is prose with Markdown structure and is the default.
	FormatMarkdown Format = "markdown"
	// FormatPlain is prose intended for a surface that renders no markup.
	FormatPlain Format = "plain"
	// FormatJSON is a machine-readable JSON value.
	FormatJSON Format = "json"
)

// Spec declares one compact projection and its encoding.
//
// It decodes from either a bare name or an object so the common YAML form can
// remain compact while format-bearing projections remain explicit.
type Spec struct {
	Name   Name   `yaml:"name" json:"name"`
	Format Format `yaml:"format,omitempty" json:"format,omitempty"`
}

// EffectiveFormat resolves the declared format, defaulting to Markdown.
func (s Spec) EffectiveFormat() Format {
	if s.Format == "" {
		return FormatMarkdown
	}
	return s.Format
}

// UnmarshalJSON accepts either "status_line" or
// {"name":"status_line","format":"plain"}.
func (s *Spec) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, `"`) {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return err
		}
		*s = Spec{Name: Name(name)}
		return nil
	}
	type wire Spec
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("facet must be a name or an object with a name: %w", err)
	}
	*s = Spec(value)
	return nil
}

// UnmarshalYAML accepts the same short and long forms as [Spec.UnmarshalJSON].
func (s *Spec) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if err := unmarshal(&name); err == nil {
		*s = Spec{Name: Name(name)}
		return nil
	}
	type wire Spec
	var value wire
	if err := unmarshal(&value); err != nil {
		return fmt.Errorf("facet must be a name or a mapping with a name: %w", err)
	}
	*s = Spec(value)
	return nil
}

// MarshalJSON emits the short form when no format override is present.
func (s Spec) MarshalJSON() ([]byte, error) {
	if s.Format == "" {
		return json.Marshal(string(s.Name))
	}
	type wire Spec
	return json.Marshal(wire(s))
}

// Payload is one complete logical document state. Every declared compact
// projection moves with Full so readers never see projections from different
// moments.
type Payload struct {
	StatusLine string `json:"status_line"`
	Teaser     string `json:"teaser,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Full       string `json:"full"`
}

// Field describes one projection field exposed by a structured writer.
type Field struct {
	Key         string                         `json:"key"`
	MaxRunes    int                            `json:"max_runes"`
	SingleLine  bool                           `json:"single_line"`
	Guidance    string                         `json:"guidance"`
	Format      Format                         `json:"format,omitempty"`
	ContextRole agentctx.ContextProjectionRole `json:"context_role"`
}

// ByKey returns one projection value by its model-facing key. Full is
// included even though it is not a declared compact facet.
func (p Payload) ByKey(key string) (string, bool) {
	section, ok := sectionByKey(key)
	if !ok {
		return "", false
	}
	value := *section.value(&p)
	return value, strings.TrimSpace(value) != ""
}

// FromArgs decodes structured writer arguments according to contract. Missing
// fields remain empty so [Contract.Validate] can report all omissions at once.
func (c Contract) FromArgs(args map[string]any) (Payload, error) {
	var payload Payload
	for _, field := range c.Fields() {
		section, ok := sectionByKey(field.Key)
		if !ok {
			continue
		}
		raw, present := args[field.Key]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return Payload{}, fmt.Errorf("%s must be a string", field.Key)
		}
		*section.value(&payload) = value
	}
	return payload, nil
}
