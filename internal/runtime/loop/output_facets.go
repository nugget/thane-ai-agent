package loop

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// Rune budgets for the published projection facets. They are display
// budgets, not storage limits: each one is the size at which that
// projection still fits the surfaces that consume it — a fleet overview
// row, a search snippet, a subscription digest. Overflow is rejected
// rather than clipped, because a clipped projection is an unreadable
// fragment with nothing to signal that anything was dropped.
const (
	statusLineMaxRunes = 120
	teaserMaxRunes     = 500
	digestMaxRunes     = 2048
)

// MaxOutputDocumentBytes is the ceiling on a maintained document's
// body, enforced at every owner write (publish, replace, notes, and
// create-time seed). It exists to close an invariant with the owner's
// privileged read budget (128 KiB in the app layer): anything the loop
// can write, it can always read back whole in one call. The gap
// between the two is serialization headroom — the read returns the
// body JSON-escaped inside a payload with outline and frontmatter, so
// the write ceiling sits a third below the read budget. Rejecting the
// write is the honest failure: a document at this size has outgrown
// single-document maintenance, and the loop should hear that at the
// moment of writing, not discover it as a truncated read next cycle.
const MaxOutputDocumentBytes = 96 * 1024

// ValidateOutputBodySize checks a maintained document body against
// [MaxOutputDocumentBytes]. The error teaches the restructure rather
// than inviting a retry: no smaller attempt at the same content will
// pass, so the correction is to the document's design.
func ValidateOutputBodySize(body string) error {
	if size := len(body); size > MaxOutputDocumentBytes {
		return fmt.Errorf("the document body is %d bytes and a maintained document's ceiling is %d (96 KiB) — past this size the document has outgrown single-document maintenance; move detail into linked documents or trim the body. The ceiling is what guarantees you can always read back what you write in one call", size, MaxOutputDocumentBytes)
	}
	return nil
}

// FacetPayload is one complete published projection set for a faceted
// maintained document. Every publish carries the whole payload: a
// rendered document shows exactly the last publish, so a partially
// updated payload would leave one projection describing a state the
// others have moved past.
type FacetPayload struct {
	StatusLine string
	Teaser     string
	Digest     string
	Full       string
}

// FacetField describes one publishable field of a faceted output, as the
// generated publish tool advertises it to the model.
type FacetField struct {
	// Key is the tool argument name.
	Key string `json:"key"`
	// MaxRunes is the display budget in runes; zero is unbounded.
	MaxRunes int `json:"max_runes"`
	// SingleLine marks a field that must not contain line breaks.
	SingleLine bool `json:"single_line"`
	// Guidance is the model-facing description of what this field is
	// for and where it surfaces.
	Guidance string `json:"guidance"`
	// Format is how this facet's value is encoded. Empty on the full
	// body, which is always markdown.
	Format FacetFormat `json:"format,omitempty"`
	// ContextRole describes how a consumer should treat this projection
	// when it is offered as model context. It does not grant injection or
	// choose a rank; the context discriminator owns those decisions.
	ContextRole agentctx.ContextProjectionRole `json:"context_role"`
}

// facetSection binds one projection to its canonical document section and
// its budget.
type facetSection struct {
	// Facet is the declared facet this section publishes, or empty for
	// the always-present full projection.
	Facet   OutputFacet
	Field   FacetField
	Heading string
	// scaffoldHint is the short phrase rendered under this section's
	// heading in a pre-first-publish scaffold — a compressed cue of what
	// belongs there, sized for a placeholder rather than for teaching
	// (the publish tool's Guidance carries the full contract).
	scaffoldHint string
	value        func(*FacetPayload) *string
}

// facetSections is the single source of truth for the publish tool's
// schema, the payload validator, the document renderer, and the parser
// that reads a rendered document back — so those four cannot drift
// apart. Order is the canonical ladder order; declaration order in a
// spec's facets list carries no meaning.
var facetSections = []facetSection{
	{
		Facet:   OutputFacetStatusLine,
		Heading: "Status Line",
		Field: FacetField{
			Key:         "status_line",
			MaxRunes:    statusLineMaxRunes,
			SingleLine:  true,
			Guidance:    "The tight signal shape: one standalone line of current state, no line breaks. Reads as an ambient status: what is true right now. This is the only thing some surfaces show, so it must stand alone without the document around it.",
			ContextRole: agentctx.ContextRoleSignal,
		},
		scaffoldHint: "one standalone line of what is true right now",
		value:        func(p *FacetPayload) *string { return &p.StatusLine },
	},
	{
		Facet:   OutputFacetTeaser,
		Heading: "Teaser",
		Field: FacetField{
			Key:         "teaser",
			MaxRunes:    teaserMaxRunes,
			Guidance:    "The roomy signal shape: one short paragraph on why a reader would open this document right now. Surfaces as the snippet in search results and cross-references, so write the hook — the reason to look — rather than a compressed summary.",
			ContextRole: agentctx.ContextRoleSignal,
		},
		scaffoldHint: "why a reader would open this document right now",
		value:        func(p *FacetPayload) *string { return &p.Teaser },
	},
	{
		Facet:   OutputFacetDigest,
		Heading: "Digest",
		Field: FacetField{
			Key:         "digest",
			MaxRunes:    digestMaxRunes,
			Guidance:    "The context payload: a standalone summary carrying enough substance to act on without opening the full document. Surfaces in subscription rows and periodic digests.",
			ContextRole: agentctx.ContextRoleContext,
		},
		scaffoldHint: "a summary with enough substance to act on",
		value:        func(p *FacetPayload) *string { return &p.Digest },
	},
	{
		Heading: "Details",
		Field: FacetField{
			Key:         "full",
			Guidance:    "The detail payload: the complete current state in markdown. This is what a reader opens when the digest is not enough. Always required: it is the document's substance, and the other projections are views of it.",
			ContextRole: agentctx.ContextRoleDetail,
		},
		scaffoldHint: "the complete current state",
		value:        func(p *FacetPayload) *string { return &p.Full },
	},
}

// FacetFields returns the fields a faceted output publishes, in canonical
// ladder order: each declared facet, then the always-present full
// projection. Every returned field is required at publish time —
// optionality lives in the declaration (a spec may declare only
// status_line), not in the write.
func (o OutputSpec) FacetFields() []FacetField {
	if len(o.Facets) == 0 {
		return nil
	}
	declared := make(map[OutputFacet]FacetSpec, len(o.Facets))
	for _, facet := range o.Facets {
		declared[facet.Name] = facet
	}
	fields := make([]FacetField, 0, len(o.Facets)+1)
	for _, section := range facetSections {
		// The full body is always published and is never declared, so it
		// carries the contract's own format rather than a facet's.
		if section.Facet == "" {
			fields = append(fields, section.Field)
			continue
		}
		if facet, ok := declared[section.Facet]; ok {
			field := section.Field
			field.Format = facet.EffectiveFormat()
			fields = append(fields, field)
		}
	}
	return fields
}

// FacetFieldByKey returns the canonical metadata for one projection key.
// Consumers that advertise faceted documents use this to share the same
// role and size vocabulary as the generated publish tool.
func FacetFieldByKey(key string) (FacetField, bool) {
	section, ok := facetSectionByKey(key)
	if !ok {
		return FacetField{}, false
	}
	return section.Field, true
}

// HasFacets reports whether this output publishes through the faceted
// projection contract rather than a whole-body replacement.
func (o OutputSpec) HasFacets() bool {
	return o.Type == OutputTypeMaintainedDocument && len(o.Facets) > 0
}

// ValidateFacetPayload checks one payload against the output's declared
// ladder. Errors name the field, the limit, and the observed size so the
// model can correct the offending projection in one more attempt without
// re-deriving the whole payload.
func (o OutputSpec) ValidateFacetPayload(payload FacetPayload) error {
	if !o.HasFacets() {
		return fmt.Errorf("output %q does not declare facets", o.Name)
	}
	for _, field := range o.FacetFields() {
		section, ok := facetSectionByKey(field.Key)
		if !ok {
			continue
		}
		value := strings.TrimSpace(*section.value(&payload))
		if value == "" {
			return fmt.Errorf("%s is required; every declared projection is published together so they cannot describe different moments", field.Key)
		}
		if err := validateFacetFormat(field, value); err != nil {
			return err
		}
		if field.SingleLine && strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must be a single line with no line breaks; it renders as one row on surfaces that show nothing else", field.Key)
		}
		if field.MaxRunes > 0 {
			if runes := utf8.RuneCountInString(value); runes > field.MaxRunes {
				return fmt.Errorf("%s is %d characters and the limit is %d; tighten it rather than letting it be cut, because a clipped projection reads as a fragment with no sign that anything is missing", field.Key, runes, field.MaxRunes)
			}
		}
		if heading, found := firstReservedFacetHeading(value); found {
			return fmt.Errorf("%s contains the reserved section heading %q; the facet headings are rendered automatically from the contract, so publish only the content beneath them", field.Key, heading)
		}
	}
	// The full projection is the document's substance and the only
	// unbudgeted field above, so it alone can push the document past
	// the size the owner is guaranteed to read back whole. Checked
	// first for the clean attribution, then the rendered document as a
	// whole — the projections and their headings render into one body,
	// and a full sitting exactly at the ceiling would put the composite
	// over it.
	if err := ValidateOutputBodySize(payload.Full); err != nil {
		return fmt.Errorf("full: %w", err)
	}
	if err := ValidateOutputBodySize(o.RenderFacetDocument(payload)); err != nil {
		return fmt.Errorf("the rendered document (every projection plus its headings): %w — full is the lever; the other projections are already budget-capped", err)
	}
	return nil
}

// RenderFacetDocument renders a payload as the canonical document body:
// one H2 section per published projection, in ladder order. Go owns this
// structure so the model never authors the section convention and the
// document stays parseable back into a payload.
func (o OutputSpec) RenderFacetDocument(payload FacetPayload) string {
	fields := o.FacetFields()
	published := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		published[field.Key] = struct{}{}
	}
	formats := make(map[string]FacetFormat, len(fields))
	for _, field := range fields {
		formats[field.Key] = field.Format
	}

	blocks := make([]string, 0, len(fields))
	for _, section := range facetSections {
		if _, ok := published[section.Field.Key]; !ok {
			continue
		}
		value := strings.TrimSpace(*section.value(&payload))
		if value == "" {
			continue
		}
		if formats[section.Field.Key] == FacetFormatJSON {
			value = jsonFence + "\n" + value + "\n" + fenceClose
		}
		blocks = append(blocks, "## "+section.Heading+"\n\n"+value)
	}
	return strings.Join(blocks, "\n\n")
}

// RenderFacetScaffold renders the pre-first-publish body for a faceted
// output: the same section skeleton [OutputSpec.RenderFacetDocument]
// will produce, with an awaiting-first-cycle placeholder under each
// heading. The first iteration sees this body in its Declared Durable
// Outputs context, so the scaffold showing the exact shape a correct
// publish produces is what stops that turn from inventing a structure
// of its own. Rendering goes through the real renderer rather than a
// copy so the scaffold cannot drift from the published form.
func (o OutputSpec) RenderFacetScaffold() string {
	var payload FacetPayload
	for _, field := range o.FacetFields() {
		section, ok := facetSectionByKey(field.Key)
		if !ok {
			continue
		}
		*section.value(&payload) = facetScaffoldPlaceholder(field, section.scaffoldHint)
	}
	return o.RenderFacetDocument(payload)
}

// facetScaffoldPlaceholder builds one section's placeholder. A json
// facet gets a valid-JSON placeholder because its section renders
// inside a json fence, where prose reads as damage.
func facetScaffoldPlaceholder(field FacetField, hint string) string {
	if field.Format == FacetFormatJSON {
		return `{"awaiting_first_cycle": true}`
	}
	if field.MaxRunes > 0 {
		return fmt.Sprintf("_(awaiting first cycle — %s, ≤%d characters)_", hint, field.MaxRunes)
	}
	return fmt.Sprintf("_(awaiting first cycle — %s)_", hint)
}

// ParseFacetDocument reads a rendered document body back into a payload.
// It is the inverse of [OutputSpec.RenderFacetDocument] and the reason
// the document is the canonical store: any derived binding — an
// ambient rail, a published entity, a remote display — can be re-seeded
// from the document alone after a restart.
//
// A body with no recognized facet sections parses entirely into Full,
// which is what lets an existing facetless maintained document be adopted
// into the faceted contract without losing its content. Content ahead of
// the first recognized heading is folded into Full for the same reason.
func (o OutputSpec) ParseFacetDocument(body string) FacetPayload {
	payload, _ := ParseFacetSections(body)
	// Only a field this output declared as json is unfenced. A markdown
	// facet whose content legitimately opens with a fenced JSON example
	// would otherwise come back with that fence eaten — the parser cannot
	// tell an example from an encoding without being told which is which,
	// and only the spec knows.
	for _, field := range o.FacetFields() {
		if field.Format != FacetFormatJSON {
			continue
		}
		section, ok := facetSectionByKey(field.Key)
		if !ok {
			continue
		}
		value := section.value(&payload)
		*value = unfence(*value)
	}
	return payload
}

// ParseFacetSections splits a rendered body into its facets without a
// spec, reporting whether the body carried any facet section at all.
//
// A reader consulting someone else's document has the body and not the
// declaration behind it, and does not need it: the section headings are
// fixed by the contract, so what a document *has* is answerable from the
// document. Only the json unfencing needs the spec, which is why that
// step lives in [OutputSpec.ParseFacetDocument] and this does not repeat
// the split.
//
// A body with no recognized facet sections parses entirely into Full and
// reports false, which is what lets an ordinary maintained document be
// read through the same path and answer "no facets" rather than error.
// Content ahead of the first recognized heading folds into Full for the
// same reason.
func ParseFacetSections(body string) (FacetPayload, bool) {
	var payload FacetPayload
	var preamble []string
	current := ""
	found := false
	collected := make(map[string][]string, len(facetSections))

	for _, line := range strings.Split(body, "\n") {
		if heading, ok := reservedFacetHeadingOf(line); ok {
			current = heading
			found = true
			continue
		}
		if current == "" {
			preamble = append(preamble, line)
			continue
		}
		collected[current] = append(collected[current], line)
	}

	for _, section := range facetSections {
		lines, ok := collected[section.Heading]
		if !ok {
			continue
		}
		*section.value(&payload) = strings.TrimSpace(strings.Join(lines, "\n"))
	}

	if leading := strings.TrimSpace(strings.Join(preamble, "\n")); leading != "" {
		if payload.Full == "" {
			payload.Full = leading
		} else {
			payload.Full = leading + "\n\n" + payload.Full
		}
	}
	return payload, found
}

// FacetByKey returns one projection level's value from a payload by its
// field key — "full" included — so a consumer can ask for the level it
// wants without a switch of its own over the ladder.
func (p FacetPayload) FacetByKey(key string) (string, bool) {
	section, ok := facetSectionByKey(key)
	if !ok {
		return "", false
	}
	value := *section.value(&p)
	return value, strings.TrimSpace(value) != ""
}

// FacetKeys returns every facet key in canonical order, for a consumer
// advertising the levels a document offers. It includes "full"
// deliberately: the full body is not a declarable facet — it is the
// document itself — but it is always a readable projection level, and a
// consumer advertising levels needs the complete ladder.
func FacetKeys() []string {
	keys := make([]string, 0, len(facetSections))
	for _, section := range facetSections {
		keys = append(keys, section.Field.Key)
	}
	return keys
}

// facetSectionByKey looks up the section table entry for a field key.
func facetSectionByKey(key string) (facetSection, bool) {
	for _, section := range facetSections {
		if section.Field.Key == key {
			return section, true
		}
	}
	return facetSection{}, false
}

// reservedFacetHeadingOf reports the canonical heading a line declares,
// if the line is exactly one of the contract's H2 section headings.
// Deeper headings inside a projection are ordinary content.
func reservedFacetHeadingOf(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "## ") {
		return "", false
	}
	text := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
	for _, section := range facetSections {
		if strings.EqualFold(text, section.Heading) {
			return section.Heading, true
		}
	}
	return "", false
}

// firstReservedFacetHeading finds a reserved section heading inside
// projection content. Such a line would be read back as a section
// boundary, silently moving content between projections, so publishing
// is refused instead.
func firstReservedFacetHeading(value string) (string, bool) {
	for _, line := range strings.Split(value, "\n") {
		if heading, ok := reservedFacetHeadingOf(line); ok {
			return "## " + heading, true
		}
	}
	return "", false
}

// validateFacetFormat enforces what a declared format actually promises
// a consumer.
//
// Only json is mechanically checkable, and it is the one worth checking:
// a consumer that declared it is code, and code given prose fails at the
// far end where nothing can explain why. plain is advisory — markdown is
// a spectrum rather than a grammar, and rejecting a stray asterisk would
// cost more than the surfaces gain — so it shapes the guidance a model
// reads instead of the value it may send.
func validateFacetFormat(field FacetField, value string) error {
	if field.Format != FacetFormatJSON {
		return nil
	}
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("%s declares format %q but the value is not valid JSON; a consumer that asked for json cannot read prose", field.Key, FacetFormatJSON)
	}
	return nil
}

// FormatGuidance returns the sentence appended to a facet's model-facing
// description when its format is anything but the markdown default.
//
// A rule that is enforced but never explained is one the model learns by
// failing, so the format that shapes validation also shapes the
// description the model reads before it writes.
func FormatGuidance(format FacetFormat) string {
	switch format {
	case FacetFormatPlain:
		return " Write plain text with no markdown: this is spoken aloud or shown somewhere that renders nothing, so asterisks and backticks are read out."
	case FacetFormatJSON:
		return " Emit valid JSON, not prose: this is read by code rather than a person, and a non-JSON value is rejected."
	default:
		return ""
	}
}

const (
	// jsonFence opens the block a json facet is rendered inside, and
	// fenceClose closes it.
	jsonFence  = "```json"
	fenceClose = "```"
)

// unfence removes the code fence a json facet was rendered inside, so
// the parse half of the round trip returns the value that was published
// rather than the markdown it was published as.
//
// The fence earns its place by making the document readable rather than
// by protecting the parser: a raw JSON value sitting under a heading
// beside prose sections reads as damage, and the fence also marks the
// section as machine-readable to anyone scanning the file. The parser
// splits only on the four reserved headings, so an arbitrary "## " line
// inside a value was never a hazard.
func unfence(value string) string {
	if !strings.HasPrefix(value, jsonFence) || !strings.HasSuffix(value, fenceClose) {
		return value
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(value, jsonFence), fenceClose)
	// A value that merely opens and closes with fences is not one block:
	// unwrapping it would splice unrelated content together.
	if strings.Contains(inner, fenceClose) {
		return value
	}
	return strings.TrimSpace(inner)
}

// FacetPayloadFromArgs reads publish-tool arguments into a payload.
//
// It lives with the contract rather than with the tool that calls it,
// because reading those arguments is the same question as what a facet
// accepts, and a second reading anywhere else would be a second answer.
// Keeping it here is also what lets the talent gate check a taught
// publish sample against the real path instead of a copy of it.
//
// A missing argument is left empty rather than rejected, so
// [OutputSpec.ValidateFacetPayload] reports every omission at once
// instead of one per attempt.
func (o OutputSpec) FacetPayloadFromArgs(args map[string]any) (FacetPayload, error) {
	var payload FacetPayload
	for _, field := range o.FacetFields() {
		section, ok := facetSectionByKey(field.Key)
		if !ok {
			continue
		}
		raw, present := args[field.Key]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return FacetPayload{}, fmt.Errorf("%s must be a string", field.Key)
		}
		*section.value(&payload) = value
	}
	return payload, nil
}

// IsFacetKey reports whether key names a facet in the contract, for a
// consumer validating a caller-supplied level before using it. "full"
// answers true: not a declarable facet, but always a readable level.
func IsFacetKey(key string) bool {
	_, ok := facetSectionByKey(key)
	return ok
}
