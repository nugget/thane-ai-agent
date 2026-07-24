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
	// OutputTypeJournalDocument describes an append-only journal
	// document maintained by the loop.
	OutputTypeJournalDocument OutputType = "journal_document"
	// OutputTypeWorkingNotes describes a loop-private process journal:
	// append-only like a journal document, internal-audience by default.
	// It holds the loop's own timeline — drift, refinement, and curation
	// rationale — and is never projected into consumer surfaces.
	OutputTypeWorkingNotes OutputType = "working_notes"
)

// OutputMode describes the allowed write mode for a loop output.
type OutputMode string

const (
	// OutputModeReplace requires complete replacement content.
	OutputModeReplace OutputMode = "replace"
	// OutputModeAppend requires append-only journal entries.
	OutputModeAppend OutputMode = "append"
)

// OutputTier names one declared fidelity level in a tiered output's
// published projection ladder. Full fidelity is not a tier: it is the
// document body itself.
type OutputTier string

const (
	// OutputTierStatusLine is the ambient projection: current state in
	// one standalone line, no markdown structure.
	OutputTierStatusLine OutputTier = "status_line"
	// OutputTierTeaser is the interest hook: one short paragraph on why
	// a reader would open the full document right now.
	OutputTierTeaser OutputTier = "teaser"
	// OutputTierDigest is the standalone summary: enough detail to act
	// on without opening the full document.
	OutputTierDigest OutputTier = "digest"
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
	// Mode is the write mode. It defaults from Type when omitted.
	Mode OutputMode `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Purpose is optional model-facing guidance for this output.
	Purpose string `yaml:"purpose,omitempty" json:"purpose,omitempty"`
	// JournalWindow is the default rolling window for journal outputs:
	// day, week, or month. Empty uses the document layer default.
	JournalWindow string `yaml:"journal_window,omitempty" json:"journal_window,omitempty"`
	// MaxWindows caps retained journal windows. Zero uses the document
	// layer default for the selected window.
	MaxWindows int `yaml:"max_windows,omitempty" json:"max_windows,omitempty"`
	// Tiers declares the published projection ladder for a maintained
	// document: which of status_line, teaser, and digest the loop
	// publishes alongside the full body. Empty means untiered.
	Tiers []OutputTier `yaml:"tiers,omitempty" json:"tiers,omitempty"`
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
	case OutputTypeJournalDocument, OutputTypeWorkingNotes:
		return OutputModeAppend
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
// output declaration.
func (o OutputSpec) ToolName() string {
	switch o.EffectiveMode() {
	case OutputModeReplace:
		return "replace_output_" + safeOutputToolSuffix(o.Name)
	case OutputModeAppend:
		return "append_output_" + safeOutputToolSuffix(o.Name)
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
	case OutputTypeMaintainedDocument, OutputTypeJournalDocument, OutputTypeWorkingNotes:
	default:
		return fmt.Errorf("unsupported type %q", o.Type)
	}
	mode := o.EffectiveMode()
	switch mode {
	case OutputModeReplace:
		if o.Type != OutputTypeMaintainedDocument {
			return fmt.Errorf("mode %q is only valid for type %q", mode, OutputTypeMaintainedDocument)
		}
	case OutputModeAppend:
		if o.Type != OutputTypeJournalDocument && o.Type != OutputTypeWorkingNotes {
			return fmt.Errorf("mode %q is only valid for types %q and %q", mode, OutputTypeJournalDocument, OutputTypeWorkingNotes)
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
		return fmt.Errorf("audience %q contradicts type %q; working notes are internal by definition — declare a journal_document instead for a published journal", OutputAudiencePublished, OutputTypeWorkingNotes)
	}
	if err := validateOutputTiers(o); err != nil {
		return err
	}
	if o.MaxWindows < 0 {
		return fmt.Errorf("max_windows must be >= 0")
	}
	return nil
}

// validateOutputTiers checks a declared projection ladder. Tiers are a
// published-projection contract, so they attach only to published
// maintained documents, and status_line anchors the ladder whenever any
// tier is declared.
func validateOutputTiers(o OutputSpec) error {
	if len(o.Tiers) == 0 {
		return nil
	}
	if o.Type != OutputTypeMaintainedDocument {
		return fmt.Errorf("tiers are only valid for type %q; %q outputs are not tiered", OutputTypeMaintainedDocument, o.Type)
	}
	if o.EffectiveAudience() == OutputAudienceInternal {
		return fmt.Errorf("tiers declare published projections, but audience is %q; an internal output has no consumers to tier for", OutputAudienceInternal)
	}
	seen := make(map[OutputTier]struct{}, len(o.Tiers))
	hasStatusLine := false
	for i, tier := range o.Tiers {
		switch tier {
		case OutputTierStatusLine, OutputTierTeaser, OutputTierDigest:
		default:
			return fmt.Errorf("tiers[%d]: unsupported tier %q; use %q, %q, or %q (full fidelity is the document body, not a declared tier)", i, tier, OutputTierStatusLine, OutputTierTeaser, OutputTierDigest)
		}
		if _, dup := seen[tier]; dup {
			return fmt.Errorf("tiers[%d]: duplicate tier %q", i, tier)
		}
		seen[tier] = struct{}{}
		if tier == OutputTierStatusLine {
			hasStatusLine = true
		}
	}
	if !hasStatusLine {
		return fmt.Errorf("tiers must include %q; the ambient one-line projection anchors the ladder (teaser and digest are optional)", OutputTierStatusLine)
	}
	return nil
}

func validateOutputs(outputs []OutputSpec) error {
	seenNames := make(map[string]struct{}, len(outputs))
	seenTools := make(map[string]struct{}, len(outputs))
	for i, output := range outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("outputs[%d]: %w", i, err)
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
		dst[i].Tiers = append([]OutputTier(nil), src[i].Tiers...)
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
