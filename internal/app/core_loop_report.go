package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// CoreAuthoringError marks a boot failure caused by authored content in
// the core root rather than by the environment around it.
//
// The distinction decides whether a supervisor should retry. A database
// that will not open, a model endpoint that is unreachable, a port
// already bound — those resolve on their own or after something else
// changes, and restarting is the right response. A loop definition that
// does not parse resolves only when a person edits the document, and
// every restart between now and then re-reads the same bytes and fails
// the same way. Retrying it is a crash loop with the appearance of
// activity.
type CoreAuthoringError struct{ Err error }

func (e *CoreAuthoringError) Error() string { return e.Err.Error() }

func (e *CoreAuthoringError) Unwrap() error { return e.Err }

// coreAuthoring wraps err as authored-content failure, or returns nil
// for a nil error so call sites can wrap unconditionally.
func coreAuthoring(err error) error {
	if err == nil {
		return nil
	}
	return &CoreAuthoringError{Err: err}
}

// CoreLoopDefinition is one definition document as a pre-flight check
// sees it: what loaded, what it declares, and what is worth saying about
// it before the loop ever runs.
type CoreLoopDefinition struct {
	// File is the document's base name, which is how every parse error
	// this package raises identifies it.
	File string `json:"file"`
	// Name is the loop the document defines, empty when it did not parse.
	Name string `json:"name,omitempty"`
	// ParentName is where the loop will hang in the graph. Empty means
	// the root, which for a core service loop is almost always a mistake
	// and gets a warning below.
	ParentName string `json:"parent_name,omitempty"`
	// Tools are the output tools this definition generates, which is the
	// most direct evidence that a facet or working-notes declaration
	// landed the way its author meant.
	Tools []string `json:"tools,omitempty"`
	// SleepMin and SleepMax report the self-pacing envelope.
	SleepMin time.Duration `json:"sleep_min,omitempty"`
	SleepMax time.Duration `json:"sleep_max,omitempty"`
	// Warnings are advisory: the document loaded and the loop will run.
	Warnings []string `json:"warnings,omitempty"`
	// Err is the parse failure, which serve would refuse to start over.
	Err error `json:"-"`
	// Error carries Err for the JSON report.
	Error string `json:"error,omitempty"`
}

// OK reports whether this document would load at boot.
func (d CoreLoopDefinition) OK() bool { return d.Err == nil }

// newFailedCoreLoopReport builds an entry for something that never got
// as far as parsing. Err and Error carry the same value: one is what
// callers branch on, the other is what the JSON report renders.
func newFailedCoreLoopReport(file string, err error) CoreLoopDefinition {
	return CoreLoopDefinition{File: file, Err: err, Error: err.Error()}
}

// CheckCoreLoopDefinitions reads every definition document under
// <core>/loops and reports what each one would produce.
//
// It differs from [loadCoreLoopDefinitions] in one deliberate way: that
// function stops at the first bad document, because a boot with a
// definition silently absent is worse than a boot that refuses. This one
// parses every document and reports all of them, because an operator
// fixing a directory wants the whole list rather than one error at a
// time. The two are checking the same contract for different audiences,
// and the parser they share is what keeps them honest.
//
// A core with no loops directory returns nil: an install that defines no
// loops in its root is ordinary, not a finding.
func CheckCoreLoopDefinitions(cfg *config.Config) []CoreLoopDefinition {
	if cfg == nil {
		return nil
	}
	// Paths first, then the derivation. Boot reads Paths["core"], but
	// that map is assembled inside App.New and a pre-flight check runs
	// with nothing but a loaded config. The two cannot disagree —
	// documentRootPaths overwrites any configured core entry with this
	// same derivation — so falling back to it reports on the directory
	// serve would read rather than on nothing at all.
	dir := strings.TrimSpace(cfg.Paths["core"])
	if dir == "" {
		dir = strings.TrimSpace(cfg.CoreRoot())
	}
	if dir == "" {
		return nil
	}
	dir = filepath.Join(dir, coreLoopsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A missing directory is ordinary and reports nothing. Anything
		// else — unreadable, a permissions problem, a broken mount — is
		// a finding: the loader refuses to boot over it, so a pre-flight
		// check that stayed silent would certify a directory serve
		// cannot read.
		if os.IsNotExist(err) {
			return nil
		}
		return []CoreLoopDefinition{newFailedCoreLoopReport(coreLoopsDirName, fmt.Errorf("read %s: %w", dir, err))}
	}

	names := make([]string, 0, len(entries))
	// Recorded rather than skipped. The loader refuses a symlink here —
	// it would read content from outside the signed root while looking
	// like part of it — so passing over one would let this check
	// certify a directory serve is about to reject.
	irregular := make(map[string]os.FileMode, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		names = append(names, entry.Name())
		if !entry.Type().IsRegular() {
			irregular[entry.Name()] = entry.Type()
		}
	}
	sort.Strings(names)

	// Reported rather than merely counted: two documents defining one
	// loop is a boot failure, and the operator needs to know which two.
	definedIn := make(map[string]string, len(names))

	results := make([]CoreLoopDefinition, 0, len(names))
	for _, name := range names {
		if mode, bad := irregular[name]; bad {
			results = append(results, newFailedCoreLoopReport(name, errNotRegularCoreLoopFile(filepath.Join(dir, name), mode)))
			continue
		}
		result := CoreLoopDefinition{File: name}
		spec, err := decodeCoreLoopDefinition(filepath.Join(dir, name))
		if err != nil {
			result.Err = err
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		result.Name = strings.TrimSpace(spec.Name)
		result.ParentName = spec.ParentName
		result.SleepMin, result.SleepMax = spec.SleepMin, spec.SleepMax
		for _, output := range spec.Outputs {
			result.Tools = append(result.Tools, output.ToolName())
		}
		if other, dup := definedIn[result.Name]; dup {
			result.Warnings = append(result.Warnings,
				"defines the loop "+result.Name+", which "+other+" also defines; one document is one loop, and this directory will not load")
		}
		definedIn[result.Name] = name
		result.Warnings = append(result.Warnings, coreLoopDefinitionWarnings(spec)...)
		results = append(results, result)
	}
	return results
}

// coreLoopDefinitionWarnings collects the advisory findings for one
// parsed definition: the general authoring warnings every persistable
// spec is checked against, plus the one that only applies to a document
// in the core root.
func coreLoopDefinitionWarnings(spec looppkg.Spec) []string {
	var warnings []string
	if _, isCoreService := coreServiceLoopByName[strings.TrimSpace(spec.Name)]; isCoreService && strings.TrimSpace(spec.ParentName) == "" {
		// The built-in path parents these three under cognition after
		// building the spec, and a document takes precedence over that
		// path entirely — so a document that says nothing about its
		// parent does not inherit the default, it lands at the root.
		// Silent re-parenting empties the container an operator can see
		// and gives no sign why.
		warnings = append(warnings, "declares no parent_name, so this core service loop will hang at the graph root rather than under "+cognitionContainerName+"; a definition document does not inherit the built-in parent")
	}
	for _, warning := range looppkg.BuildDefinitionWarnings(spec) {
		warnings = append(warnings, warning.Message)
	}
	return warnings
}
