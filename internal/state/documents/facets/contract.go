package facets

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

const (
	statusLineMaxRunes = 120
	teaserMaxRunes     = 500
	digestMaxRunes     = 2048

	// MaxDocumentBytes is the write ceiling that guarantees a maintained
	// document can be read back whole by its owner in one call.
	MaxDocumentBytes = 96 * 1024
)

// Contract defines the compact projections carried with a document's full
// content. Declaration order is irrelevant; [Contract.Fields] is canonical.
type Contract struct {
	Facets []Spec `json:"facets"`
}

type section struct {
	name         Name
	field        Field
	heading      string
	scaffoldHint string
	value        func(*Payload) *string
}

var sections = []section{
	{
		name:    StatusLine,
		heading: "Status Line",
		field: Field{
			Key:         string(StatusLine),
			MaxRunes:    statusLineMaxRunes,
			SingleLine:  true,
			Guidance:    "The tight signal shape: one standalone line of current state, no line breaks. Reads as an ambient status: what is true right now. This is the only thing some surfaces show, so it must stand alone without the document around it.",
			ContextRole: agentctx.ContextRoleSignal,
		},
		scaffoldHint: "one standalone line of what is true right now",
		value:        func(p *Payload) *string { return &p.StatusLine },
	},
	{
		name:    Teaser,
		heading: "Teaser",
		field: Field{
			Key:         string(Teaser),
			MaxRunes:    teaserMaxRunes,
			Guidance:    "The roomy signal shape: one short paragraph on why a reader would open this document right now. Surfaces as the snippet in search results and cross-references, so write the hook - the reason to look - rather than a compressed summary.",
			ContextRole: agentctx.ContextRoleSignal,
		},
		scaffoldHint: "why a reader would open this document right now",
		value:        func(p *Payload) *string { return &p.Teaser },
	},
	{
		name:    Digest,
		heading: "Digest",
		field: Field{
			Key:         string(Digest),
			MaxRunes:    digestMaxRunes,
			Guidance:    "The context payload: a standalone summary carrying enough substance to act on without opening the full document. Surfaces in subscription rows and periodic digests.",
			ContextRole: agentctx.ContextRoleContext,
		},
		scaffoldHint: "a summary with enough substance to act on",
		value:        func(p *Payload) *string { return &p.Digest },
	},
	{
		heading: "Details",
		field: Field{
			Key:         "full",
			Guidance:    "The detail payload: the complete current state in markdown. This is what a reader opens when the digest is not enough. Always required: it is the document's substance, and the other projections are views of it.",
			ContextRole: agentctx.ContextRoleDetail,
		},
		scaffoldHint: "the complete current state",
		value:        func(p *Payload) *string { return &p.Full },
	},
}

// DefaultContract returns the normal document shape. Callers may omit teaser
// or digest by constructing a narrower contract when no consumer needs them.
func DefaultContract() Contract {
	return Contract{Facets: []Spec{{Name: StatusLine}, {Name: Teaser}, {Name: Digest}}}
}

// Fields returns declared projections followed by full in canonical order.
func (c Contract) Fields() []Field {
	declared := make(map[Name]Spec, len(c.Facets))
	for _, facet := range c.Facets {
		declared[facet.Name] = facet
	}
	fields := make([]Field, 0, len(c.Facets)+1)
	for _, item := range sections {
		if item.name == "" {
			fields = append(fields, item.field)
			continue
		}
		if facet, ok := declared[item.name]; ok {
			field := item.field
			field.Format = facet.EffectiveFormat()
			fields = append(fields, field)
		}
	}
	return fields
}

// ValidateDefinition checks the facet set independently of any loop or domain
// owner.
func (c Contract) ValidateDefinition() error {
	if len(c.Facets) == 0 {
		return fmt.Errorf("facets must include %q", StatusLine)
	}
	seen := make(map[Name]struct{}, len(c.Facets))
	hasStatus := false
	for i, facet := range c.Facets {
		switch facet.Name {
		case StatusLine, Teaser, Digest:
		default:
			return fmt.Errorf("facets[%d]: unsupported facet %q; use %q, %q, or %q", i, facet.Name, StatusLine, Teaser, Digest)
		}
		switch facet.EffectiveFormat() {
		case FormatMarkdown, FormatPlain, FormatJSON:
		default:
			return fmt.Errorf("facets[%d]: unsupported format %q for %q", i, facet.Format, facet.Name)
		}
		if _, duplicate := seen[facet.Name]; duplicate {
			return fmt.Errorf("facets[%d]: duplicate facet %q", i, facet.Name)
		}
		seen[facet.Name] = struct{}{}
		hasStatus = hasStatus || facet.Name == StatusLine
	}
	if !hasStatus {
		return fmt.Errorf("facets must include %q; teaser and digest are optional", StatusLine)
	}
	return nil
}

// Validate checks one complete logical document and reports all independently
// correctable field violations together.
func (c Contract) Validate(payload Payload) error {
	if err := c.ValidateDefinition(); err != nil {
		return err
	}
	var validationErrors []error
	for _, field := range c.Fields() {
		item, _ := sectionByKey(field.Key)
		value := strings.TrimSpace(*item.value(&payload))
		if value == "" {
			validationErrors = append(validationErrors, fmt.Errorf("%s is required; every declared projection is written together so they cannot describe different moments", field.Key))
			continue
		}
		if field.Format == FormatJSON && !json.Valid([]byte(value)) {
			validationErrors = append(validationErrors, fmt.Errorf("%s declares format %q but the value is not valid JSON; a consumer that asked for json cannot read prose", field.Key, FormatJSON))
		}
		if field.SingleLine && strings.ContainsAny(value, "\r\n") {
			validationErrors = append(validationErrors, fmt.Errorf("%s must be a single line with no line breaks", field.Key))
		}
		if field.MaxRunes > 0 {
			if runes := utf8.RuneCountInString(value); runes > field.MaxRunes {
				validationErrors = append(validationErrors, fmt.Errorf("%s is %d characters and the limit is %d; tighten it rather than allowing truncation", field.Key, runes, field.MaxRunes))
			}
		}
		if heading, found := firstReservedHeading(value); found {
			validationErrors = append(validationErrors, fmt.Errorf("%s contains the reserved section heading %q; facet headings are rendered automatically, so pass only projection content", field.Key, heading))
		}
	}
	fullTooLarge := false
	if err := ValidateBodySize(payload.Full); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("full: %w", err))
		fullTooLarge = true
	}
	if !fullTooLarge {
		if err := ValidateBodySize(c.Render(payload)); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("the rendered document (every projection plus its headings): %w - full is the lever; compact projections are already budget-capped", err))
		}
	}
	if len(validationErrors) > 0 {
		return errors.Join(validationErrors...)
	}
	return nil
}

// ValidateBodySize enforces the maintained-document readback invariant.
func ValidateBodySize(body string) error {
	if size := len(body); size > MaxDocumentBytes {
		return fmt.Errorf("the document body is %d bytes and a maintained document's ceiling is %d (96 KiB) - past this size the document has outgrown single-document maintenance; move detail into linked documents or trim the body. The ceiling guarantees you can always read back what you write in one call", size, MaxDocumentBytes)
	}
	return nil
}

// FieldByKey returns canonical metadata for one readable projection.
func FieldByKey(key string) (Field, bool) {
	item, ok := sectionByKey(key)
	if !ok {
		return Field{}, false
	}
	return item.field, true
}

// Keys returns the complete logical read ladder, including full.
func Keys() []string {
	keys := make([]string, 0, len(sections))
	for _, item := range sections {
		keys = append(keys, item.field.Key)
	}
	return keys
}

// IsKey reports whether key names a readable projection.
func IsKey(key string) bool {
	_, ok := sectionByKey(key)
	return ok
}

// FormatGuidance returns model-facing guidance for non-default encodings.
func FormatGuidance(format Format) string {
	switch format {
	case FormatPlain:
		return " Write plain text with no markdown: this may be spoken aloud or shown somewhere that renders nothing."
	case FormatJSON:
		return " Emit valid JSON, not prose: this is read by code and a non-JSON value is rejected."
	default:
		return ""
	}
}

func sectionByKey(key string) (section, bool) {
	for _, item := range sections {
		if item.field.Key == key {
			return item, true
		}
	}
	return section{}, false
}
