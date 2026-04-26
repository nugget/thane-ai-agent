package loop

import (
	"context"
	"fmt"
	"strings"
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
)

// OutputMode describes the allowed write mode for a loop output.
type OutputMode string

const (
	// OutputModeReplace requires complete replacement content.
	OutputModeReplace OutputMode = "replace"
	// OutputModeAppend requires append-only journal entries.
	OutputModeAppend OutputMode = "append"
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
	switch o.Type {
	case OutputTypeMaintainedDocument, OutputTypeJournalDocument:
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
		if o.Type != OutputTypeJournalDocument {
			return fmt.Errorf("mode %q is only valid for type %q", mode, OutputTypeJournalDocument)
		}
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
	if o.MaxWindows < 0 {
		return fmt.Errorf("max_windows must be >= 0")
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
