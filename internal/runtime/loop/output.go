package loop

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
)

const (
	maxOutputToolNameLength = 64

	// structuredPayloadSinkMQTT is the only ref sink a structured
	// payload output can address today. It is a named constant so the
	// error text and the check cannot drift apart, and so adding a
	// second sink is a visible change here rather than a new string
	// literal somewhere else.
	structuredPayloadSinkMQTT = "mqtt"

	// maxStructuredPayloadSuffixLength bounds the entity suffix. Home
	// Assistant tolerates far longer entity IDs, but a complication
	// binding is typed by hand into the companion app.
	maxStructuredPayloadSuffixLength = 48
)

// OutputType names a durable output contract declared by a loop.
type OutputType string

const (
	// OutputTypeMaintainedDocument describes a document the loop owns
	// as a current complete state.
	OutputTypeMaintainedDocument OutputType = "maintained_document"
	// OutputTypeJournalDocument describes an append-only journal
	// document maintained by the loop.
	OutputTypeJournalDocument OutputType = "journal_document"
	// OutputTypeStructuredPayload describes a slotted payload rendered
	// by an external surface rather than written as a document. The
	// surface is named by [OutputSpec.Target] and its slot contract
	// lives in the outputtargets registry.
	OutputTypeStructuredPayload OutputType = "structured_payload"
)

// OutputMode describes the allowed write mode for a loop output.
type OutputMode string

const (
	// OutputModeReplace requires complete replacement content.
	OutputModeReplace OutputMode = "replace"
	// OutputModeAppend requires append-only journal entries.
	OutputModeAppend OutputMode = "append"
	// OutputModeSet requires a complete slot set on every write. It has
	// no partial-update form: a rendered surface shows exactly the last
	// payload, so an omitted slot is a cleared slot.
	OutputModeSet OutputMode = "set"
)

// OutputSpec declares one durable document surface a loop is allowed to
// maintain. The declaration is persistable; runtime hydration turns it
// into scoped tools and context.
type OutputSpec struct {
	// Name is the stable semantic name for this output within the loop.
	Name string `yaml:"name" json:"name"`
	// Type identifies the output behavior, such as maintained_document.
	Type OutputType `yaml:"type" json:"type"`
	// Ref addresses the destination in the form sink:path. Document
	// outputs use a managed document ref such as core:metacognitive.md;
	// structured payload outputs use mqtt:<entity_suffix>, which
	// becomes a Home Assistant sensor entity.
	Ref string `yaml:"ref" json:"ref"`
	// Target names the rendering target for a structured payload
	// output, such as apple_watch.rectangular. It selects the slot
	// contract the generated tool advertises and validates against, and
	// must be empty for document outputs.
	Target string `yaml:"target,omitempty" json:"target,omitempty"`
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
	case OutputTypeJournalDocument:
		return OutputModeAppend
	case OutputTypeStructuredPayload:
		return OutputModeSet
	default:
		return ""
	}
}

// ToolName returns the scoped mutation tool name generated for this
// output declaration.
func (o OutputSpec) ToolName() string {
	switch o.EffectiveMode() {
	case OutputModeReplace:
		return "replace_output_" + safeOutputToolSuffix(o.Name)
	case OutputModeAppend:
		return "append_output_" + safeOutputToolSuffix(o.Name)
	case OutputModeSet:
		return "set_output_" + safeOutputToolSuffix(o.Name)
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
	case OutputTypeMaintainedDocument, OutputTypeJournalDocument, OutputTypeStructuredPayload:
	default:
		return fmt.Errorf("unsupported type %q", o.Type)
	}
	if err := o.validateTarget(); err != nil {
		return err
	}
	mode := o.EffectiveMode()
	switch mode {
	case OutputModeReplace:
		if o.Type != OutputTypeMaintainedDocument {
			return fmt.Errorf("mode %q is only valid for type %q", mode, OutputTypeMaintainedDocument)
		}
	case OutputModeAppend:
		if o.Type != OutputTypeJournalDocument {
			return fmt.Errorf("mode %q is only valid for type %q", mode, OutputTypeJournalDocument)
		}
	case OutputModeSet:
		if o.Type != OutputTypeStructuredPayload {
			return fmt.Errorf("mode %q is only valid for type %q", mode, OutputTypeStructuredPayload)
		}
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
	if o.MaxWindows < 0 {
		return fmt.Errorf("max_windows must be >= 0")
	}
	return nil
}

// validateTarget enforces the target/type pairing and, for a structured
// payload, that the declared target actually exists and the ref names the
// sink that can render it. A target ID typo is otherwise invisible until
// wake time, when the loop discovers it has no output tool at all.
func (o OutputSpec) validateTarget() error {
	target := strings.TrimSpace(o.Target)
	if o.Type != OutputTypeStructuredPayload {
		if target != "" {
			return fmt.Errorf("target %q is only valid for type %q; document outputs render through the document layer", o.Target, OutputTypeStructuredPayload)
		}
		return nil
	}
	if target == "" {
		return fmt.Errorf("target is required for type %q; choose one of %s", OutputTypeStructuredPayload, strings.Join(outputtargets.IDs(), ", "))
	}
	if _, ok := outputtargets.Lookup(target); !ok {
		return fmt.Errorf("unknown target %q; registered targets are %s", o.Target, strings.Join(outputtargets.IDs(), ", "))
	}
	return validateStructuredPayloadRef(o.Ref)
}

// validateStructuredPayloadRef checks that a structured payload ref names
// a supported sink and a usable entity suffix. The suffix travels into an
// MQTT topic path and a Home Assistant entity ID, so the grammar is the
// intersection of what both accept rather than what either tolerates.
func validateStructuredPayloadRef(ref string) error {
	sink, suffix, _ := strings.Cut(strings.TrimSpace(ref), ":")
	sink = strings.TrimSpace(sink)
	suffix = strings.TrimSpace(suffix)
	if sink != structuredPayloadSinkMQTT {
		return fmt.Errorf("ref %q must address the %q sink for type %q (for example %s:watch_status)", ref, structuredPayloadSinkMQTT, OutputTypeStructuredPayload, structuredPayloadSinkMQTT)
	}
	if len(suffix) > maxStructuredPayloadSuffixLength {
		return fmt.Errorf("ref entity suffix %q is %d characters; keep it to %d or fewer", suffix, len(suffix), maxStructuredPayloadSuffixLength)
	}
	for i, r := range suffix {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9', r == '_':
			if i == 0 {
				return fmt.Errorf("ref entity suffix %q must start with a lowercase letter", suffix)
			}
		default:
			return fmt.Errorf("ref entity suffix %q contains unsupported character %q; use lowercase letters, digits, and underscores so it is valid in both an MQTT topic and a Home Assistant entity ID", suffix, r)
		}
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
