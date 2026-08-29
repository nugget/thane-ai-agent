package loop

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

const maxOutputToolNameLength = 64

// OutputType names a durable output contract declared by a loop.
type OutputType string

const (
	// OutputTypeMaintainedDocument describes a document the loop owns
	// as a current complete state.
	OutputTypeMaintainedDocument OutputType = "maintained_document"
	// OutputTypeWorkingNotes describes a loop's private thinking:
	// maintained like its published document and internal-audience by
	// default, never projected into consumer surfaces.
	//
	// It holds what the loop currently believes — working theories, the
	// state of an experiment, what it expects next — and is rewritten as
	// that changes rather than appended to. A loop that appends its
	// theories has to reconstruct its own current view from a history of
	// superseded ones every turn, which is the difficulty holding state
	// across turns that this exists to remove, not a way to solve it.
	OutputTypeWorkingNotes OutputType = "working_notes"
)

// OutputMode describes the allowed write mode for a loop output. Since
// the journal_document type retired, the enum holds one value; it stays
// an enum because the mode axis is the declared extension point for
// future write shapes, not because a spec has a choice to make today.
type OutputMode string

const (
	// OutputModeReplace requires complete replacement content.
	OutputModeReplace OutputMode = "replace"
)

// OutputFacet names one face a maintained document publishes. The full
// body is not a facet: it is the document itself, and the facets are
// views of it.
type OutputFacet string

const (
	// OutputFacetStatusLine is the ambient projection: current state in
	// one standalone line, no markdown structure.
	OutputFacetStatusLine OutputFacet = "status_line"
	// OutputFacetTeaser is the interest hook: one short paragraph on why
	// a reader would open the full document right now.
	OutputFacetTeaser OutputFacet = "teaser"
	// OutputFacetDigest is the standalone summary: enough detail to act
	// on without opening the full document.
	OutputFacetDigest OutputFacet = "digest"
)

// Frontmatter keys stamped on loop-managed documents wherever one is
// written — the create-time scaffold and every later output-tool write.
// audience is the document layer's projection gate (an internal
// document stays out of search results and tagged-guidance injection);
// managed_by records which generated tool owns the document's
// structure, so an edit arriving through a general document tool can be
// pointed back at the owning interface instead of silently competing
// with it.
const (
	// OutputAudienceFrontmatterKey carries the output's effective
	// audience on the document itself.
	OutputAudienceFrontmatterKey = "audience"
	// OutputManagedByFrontmatterKey names the generated output tool
	// that owns the document.
	OutputManagedByFrontmatterKey = "managed_by"
)

// OutputAudience describes which surfaces may project an output's
// content.
type OutputAudience string

const (
	// OutputAudiencePublished allows projection into any consumer
	// surface: search results, context injection, ambient rails.
	OutputAudiencePublished OutputAudience = "published"
	// OutputAudienceInternal restricts the content to the owning loop's
	// own context and explicit by-ref reads. This is context hygiene,
	// not secrecy: operators and the archive still see the document.
	OutputAudienceInternal OutputAudience = "internal"
)

// OutputSpec declares one durable document surface a loop is allowed to
// maintain. The declaration is persistable; runtime hydration turns it
// into scoped tools and context.
type OutputSpec struct {
	// Name is the stable semantic name for this output within the loop.
	Name string `yaml:"name" json:"name"`
	// Type identifies the output behavior, such as maintained_document.
	Type OutputType `yaml:"type" json:"type"`
	// Ref is the managed document ref, such as core:metacognitive.md.
	Ref string `yaml:"ref" json:"ref"`
	// Mode is the write mode. It defaults from Type when omitted, and
	// with journal_document retired every valid spec resolves to
	// "replace" — so the field is wire compatibility for stored specs
	// that named it explicitly plus the declared extension point for
	// future write shapes, not a live choice. Deprecated for authoring:
	// leave it empty and let Type imply it.
	Mode OutputMode `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Purpose is optional model-facing guidance for this output.
	Purpose string `yaml:"purpose,omitempty" json:"purpose,omitempty"`
	// Facets declares which condensed views this output publishes for a
	// maintained document: any needed subset of status_line, teaser, and
	// digest alongside the full body. status_line and teaser are different
	// shapes of one outward-facing signal role; digest is a context payload.
	// Empty means no facets. The declaration is a set — element order
	// carries no meaning, because presentation order is fixed by the
	// contract itself; renderers and consumers must not read anything into
	// declaration order.
	Facets []FacetSpec `yaml:"facets,omitempty" json:"facets,omitempty"`
	// Audience overrides which surfaces may project this output. Empty
	// defaults from Type: working_notes is internal, every other type
	// is published.
	Audience OutputAudience `yaml:"audience,omitempty" json:"audience,omitempty"`
}

// RuntimeTool is a request-scoped tool hydrated from runtime state. It
// exists so loops can expose narrow interfaces, such as declared output
// mutation tools, without registering those tools globally.
type RuntimeTool struct {
	Name                 string                                                         `yaml:"-" json:"-"`
	Description          string                                                         `yaml:"-" json:"-"`
	Parameters           map[string]any                                                 `yaml:"-" json:"-"`
	Handler              func(ctx context.Context, args map[string]any) (string, error) `yaml:"-" json:"-"`
	SkipContentResolve   bool                                                           `yaml:"-" json:"-"`
	ContentResolveExempt []string                                                       `yaml:"-" json:"-"`
}

// OutputContextBuilder renders model-facing context for a loop's
// declared durable outputs.
type OutputContextBuilder func(ctx context.Context, outputs []OutputSpec) (string, error)

// EffectiveMode returns the explicit mode or the default mode implied by
// the output type.
func (o OutputSpec) EffectiveMode() OutputMode {
	if o.Mode != "" {
		return o.Mode
	}
	switch o.Type {
	case OutputTypeMaintainedDocument:
		return OutputModeReplace
	case OutputTypeWorkingNotes:
		return OutputModeReplace
	default:
		return ""
	}
}

// EffectiveAudience returns the explicit audience or the default
// audience implied by the output type.
func (o OutputSpec) EffectiveAudience() OutputAudience {
	if o.Audience != "" {
		return o.Audience
	}
	if o.Type == OutputTypeWorkingNotes {
		return OutputAudienceInternal
	}
	return OutputAudiencePublished
}

// ToolName returns the scoped mutation tool name generated for this
// output declaration. A faceted output gets a publish verb rather than a
// replace verb because its interface is a set of typed projections, not
// a document body.
func (o OutputSpec) ToolName() string {
	if o.HasFacets() {
		return "publish_output_" + safeOutputToolSuffix(o.Name)
	}
	switch o.EffectiveMode() {
	case OutputModeReplace:
		return "replace_output_" + safeOutputToolSuffix(o.Name)
	default:
		return "write_output_" + safeOutputToolSuffix(o.Name)
	}
}

// Validate checks that one output declaration is internally
// consistent.
func (o OutputSpec) Validate() error {
	if strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if r, ok := firstUnsupportedOutputNameRune(o.Name); ok {
		return fmt.Errorf("name %q contains unsupported character %q; use ASCII letters, digits, spaces, hyphens, or underscores", o.Name, r)
	}
	if safeOutputToolSuffix(o.Name) == "" {
		return fmt.Errorf("name %q cannot produce a tool name", o.Name)
	}
	if len(o.ToolName()) > maxOutputToolNameLength {
		return fmt.Errorf("name %q produces tool name %q longer than %d characters", o.Name, o.ToolName(), maxOutputToolNameLength)
	}
	if strings.TrimSpace(o.Ref) == "" {
		return fmt.Errorf("ref is required")
	}
	if err := validateOutputRefGrammar(o.Ref); err != nil {
		return err
	}
	switch o.Type {
	case OutputTypeMaintainedDocument, OutputTypeWorkingNotes:
	default:
		return fmt.Errorf("unsupported type %q", o.Type)
	}
	mode := o.EffectiveMode()
	switch mode {
	case OutputModeReplace:
		if o.Type != OutputTypeMaintainedDocument && o.Type != OutputTypeWorkingNotes {
			return fmt.Errorf("mode %q is only valid for types %q and %q", mode, OutputTypeMaintainedDocument, OutputTypeWorkingNotes)
		}
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
	switch o.Audience {
	case "", OutputAudiencePublished, OutputAudienceInternal:
	default:
		return fmt.Errorf("unsupported audience %q; use %q or %q", o.Audience, OutputAudiencePublished, OutputAudienceInternal)
	}
	if o.Type == OutputTypeWorkingNotes && o.Audience == OutputAudiencePublished {
		return fmt.Errorf("audience %q contradicts type %q; working notes are a loop's private thinking — declare a maintained_document for anything a reader should see", OutputAudiencePublished, OutputTypeWorkingNotes)
	}
	if err := validateOutputFacets(o); err != nil {
		return err
	}
	return nil
}

// validateOutputFacets checks a declared facet set. Facets are a
// published-projection contract, so they attach only to published
// maintained documents. Each document declares only the projections its
// consumers need; full remains implicit and always present.
func validateOutputFacets(o OutputSpec) error {
	if len(o.Facets) == 0 {
		return nil
	}
	if o.Type != OutputTypeMaintainedDocument {
		return fmt.Errorf("facets are only valid for type %q; %q outputs have none", OutputTypeMaintainedDocument, o.Type)
	}
	if o.EffectiveAudience() == OutputAudienceInternal {
		return fmt.Errorf("facets declare published projections, but audience is %q; an internal output has no consumers to cut a facet for", OutputAudienceInternal)
	}
	seen := make(map[OutputFacet]struct{}, len(o.Facets))
	hasStatusLine := false
	for i, facet := range o.Facets {
		switch facet.Name {
		case OutputFacetStatusLine, OutputFacetTeaser, OutputFacetDigest:
		default:
			return fmt.Errorf("facets[%d]: unsupported facet %q; use %q, %q, or %q (the full body is the document itself, not a declared facet)", i, facet.Name, OutputFacetStatusLine, OutputFacetTeaser, OutputFacetDigest)
		}
		if _, ok := validFacetFormats[facet.EffectiveFormat()]; !ok {
			return fmt.Errorf("facets[%d]: unsupported format %q for %q; use %q, %q, or %q", i, facet.Format, facet.Name, FacetFormatMarkdown, FacetFormatPlain, FacetFormatJSON)
		}
		if _, dup := seen[facet.Name]; dup {
			return fmt.Errorf("facets[%d]: duplicate facet %q", i, facet.Name)
		}
		seen[facet.Name] = struct{}{}
		if facet.Name == OutputFacetStatusLine {
			hasStatusLine = true
		}
	}
	if !hasStatusLine {
		return fmt.Errorf("facets must include %q; the ambient one-line projection is the one every surface can take (teaser and digest are optional)", OutputFacetStatusLine)
	}
	return nil
}

func validateOutputs(outputs []OutputSpec) error {
	seenNames := make(map[string]struct{}, len(outputs))
	seenTools := make(map[string]struct{}, len(outputs))
	workingNotes := ""
	for i, output := range outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("outputs[%d]: %w", i, err)
		}
		if output.Type == OutputTypeWorkingNotes {
			// One private log per loop: the note argument on a faceted
			// publish writes to "the" working notes, and a second
			// declaration would make that target an arbitrary pick
			// between two documents. A loop that wants a second private
			// document can declare a maintained_document with
			// audience: internal, which carries no such implicit target.
			if workingNotes != "" {
				return fmt.Errorf("outputs[%d]: loop already declares the working_notes output %q; a loop has one place for its current thinking, so put it all there — for an additional private document declare a maintained_document with audience: %q", i, workingNotes, OutputAudienceInternal)
			}
			workingNotes = output.Name
		}
		nameKey := strings.ToLower(strings.TrimSpace(output.Name))
		if _, exists := seenNames[nameKey]; exists {
			return fmt.Errorf("outputs[%d]: duplicate name %q", i, output.Name)
		}
		seenNames[nameKey] = struct{}{}
		toolName := output.ToolName()
		if _, exists := seenTools[toolName]; exists {
			return fmt.Errorf("outputs[%d]: duplicate generated tool %q", i, toolName)
		}
		seenTools[toolName] = struct{}{}
	}
	return nil
}

func cloneOutputs(src []OutputSpec) []OutputSpec {
	if len(src) == 0 {
		return nil
	}
	dst := make([]OutputSpec, len(src))
	copy(dst, src)
	for i := range dst {
		dst[i].Facets = append([]FacetSpec(nil), src[i].Facets...)
	}
	return dst
}

func cloneRuntimeTools(src []RuntimeTool) []RuntimeTool {
	if len(src) == 0 {
		return nil
	}
	dst := make([]RuntimeTool, len(src))
	copy(dst, src)
	for i := range dst {
		dst[i].ContentResolveExempt = append([]string(nil), src[i].ContentResolveExempt...)
	}
	return dst
}

func safeOutputToolSuffix(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case r == '_' || r == '-' || r == ' ':
			if b.Len() > 0 && !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	return out
}

// validateOutputRefGrammar rejects an output ref that is not a literal
// root:path document reference. The signature failure it guards against
// is #1068: universal prefix-to-content resolution silently replacing a
// real ref (e.g. projects:foo/bar.md) with that document's body, leaving
// a multi-line markdown blob in Ref. That value passes the non-empty
// check above but is not a reference — it only fails much later at wake
// time with "unknown document root". A real ref is a single-line
// root:path token, so any control character (newlines, NUL, and the rest
// of the C0/C1 range) is an unambiguous tell that content leaked into the
// ref. This check stays syntactic (root membership is enforced at
// hydration, where the document store's configured roots are available)
// so the loop package keeps no dependency on the documents store.
func validateOutputRefGrammar(ref string) error {
	trimmed := strings.TrimSpace(ref)
	if i := strings.IndexFunc(trimmed, unicode.IsControl); i >= 0 {
		return fmt.Errorf("ref must be a single root:path reference, not document content (got a control character at offset %d in a value beginning %q); a ref holding document text is the #1068 content-resolution corruption signature", i, outputRefFirstLine(trimmed))
	}
	root, relPath, ok := strings.Cut(trimmed, ":")
	root = strings.TrimSpace(root)
	relPath = strings.TrimSpace(relPath)
	if !ok || root == "" || relPath == "" {
		return fmt.Errorf("ref %q must be a document reference of the form root:path (for example core:notes.md)", outputRefFirstLine(trimmed))
	}
	if strings.ContainsAny(root, " \t") {
		return fmt.Errorf("ref root %q must be a single identifier; expected root:path like core:notes.md", root)
	}
	return nil
}

// outputRefFirstLine returns the first line of s, trimmed, so error
// messages about a content-corrupted ref stay to one line instead of
// dumping an entire markdown document.
func outputRefFirstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func firstUnsupportedOutputNameRune(name string) (rune, bool) {
	for _, r := range name {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			continue
		case r == '_' || r == '-' || r == ' ':
			continue
		default:
			return r, true
		}
	}
	return 0, false
}
