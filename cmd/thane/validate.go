package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/app"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/coreintegrity"
	"github.com/nugget/thane-ai-agent/internal/server/edge"
)

// runValidate parses and validates the config that would be loaded by
// `thane serve`. It does not start any services or open any sockets —
// this is purely a pre-flight gate for scripts and operators.
//
// The configPath argument is the -insecure-config escape hatch: when
// set, that exact file is loaded from outside the trust boundary. When
// empty, the config comes from {workspace}/core/config.yaml — the one
// location the runtime reads — with the workspace resolved from
// workspacePath or its default. The returned error signals "config is
// invalid" so the binary exits non-zero, which makes `thane validate
// && thane serve` a usable deploy guard.
//
// Output mode "text" prints a one-line confirmation followed by a
// short structural summary. Mode "json" emits a single object with
// path, valid, error (if any), and summary fields, suitable for
// piping into jq.
func runValidate(w io.Writer, configPath, workspacePath, outputFmt string) error {
	cfg, cfgPath, loadErr := loadConfig(configPath, workspacePath)
	integrity := checkCoreIntegrity(cfg, configPath, workspacePath)
	// When discovery fails before a path is resolved, fall back to
	// the operator's explicit -insecure-config value so the JSON report
	// still names the file that was at fault. Stays empty when neither
	// resolution nor an explicit flag was provided.
	if cfgPath == "" {
		cfgPath = configPath
	}
	// Admission needs a loaded config to know which roots exist and whose
	// signatures may establish them, so it only runs once the config parses.
	var admission []app.RootAdmission
	var coreLoops []app.CoreLoopDefinition
	var tlsErr error
	if loadErr == nil {
		admission = app.CheckRootAdmission(context.Background(), cfg)
		coreLoops = app.CheckCoreLoopDefinitions(cfg)
		tlsErr = tlsPreflight(cfg)
	}
	if outputFmt == "json" {
		// Always emit JSON to stdout, even on failure — scripts may
		// want the structured error. The error is still returned so
		// the exit code reflects validity.
		if err := writeValidateJSON(w, cfgPath, cfg, loadErr, integrity, admission, coreLoops, tlsErr); err != nil {
			return err
		}
		if loadErr != nil {
			return terminal(loadErr)
		}
		return firstError(integrityError(integrity), admissionError(admission), coreLoopError(coreLoops), tlsErr)
	}
	if loadErr != nil {
		// Config could not load, but the integrity report is often the
		// reason and always the next place to look, so print it rather
		// than leaving the operator with a bare parse error.
		if integrity != nil {
			writeIntegrityText(w, *integrity)
		}
		return terminal(loadErr)
	}
	fmt.Fprintf(w, "✓ Config valid: %s\n\n", cfgPath)
	writeValidateText(w, cfg)
	if cfg.TLS.Enabled {
		fmt.Fprintln(w)
		writeTLSText(w, cfg, tlsErr)
	}
	if integrity != nil {
		fmt.Fprintln(w)
		writeIntegrityText(w, *integrity)
	}
	if len(admission) > 0 {
		fmt.Fprintln(w)
		writeAdmissionText(w, admission)
	}
	if len(coreLoops) > 0 {
		fmt.Fprintln(w)
		writeCoreLoopText(w, coreLoops)
	}
	return firstError(integrityError(integrity), admissionError(admission), coreLoopError(coreLoops), tlsErr)
}

// tlsPreflight runs the HTTPS front door's boot-time checks without
// touching the network: the DNS provider resolves and its settings
// decode, certificate storage is writable, and every client CA parses.
// Nothing is printed by this function; the caller reports it.
func tlsPreflight(cfg *config.Config) error {
	if cfg == nil || !cfg.TLS.Enabled {
		return nil
	}
	if err := edge.Preflight(cfg.TLS, cfg.CoreRoot()); err != nil {
		return terminal(fmt.Errorf("https front door: %w (thane serve will refuse to start)", err))
	}
	return nil
}

// writeTLSText prints the front door's hostnames and the preflight
// verdict. A token is never printed, only whether it decoded.
func writeTLSText(w io.Writer, cfg *config.Config, err error) {
	if err != nil {
		fmt.Fprintf(w, "✗ HTTPS front door: %v\n", err)
		return
	}
	fmt.Fprintf(w, "✓ HTTPS front door: %d hostname(s) via %s DNS-01, storage %s\n",
		len(cfg.TLS.Hostnames), cfg.TLS.CertMagic.DNS.Provider, cfg.TLS.CertMagic.Storage)
}

// coreLoopError converts a definition document that will not parse into
// the error that stops `thane validate && thane serve`.
//
// Warnings deliberately do not produce one. A loop that hangs in the
// wrong place still runs, and a check that refuses to certify a working
// instance is one an operator learns to skip.
func coreLoopError(definitions []app.CoreLoopDefinition) error {
	var bad []string
	for _, definition := range definitions {
		if !definition.OK() {
			bad = append(bad, definition.File)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return terminal(fmt.Errorf("core loop definitions failed to load: %s (see the report above; thane serve will refuse to start)",
		strings.Join(bad, ", ")))
}

// writeCoreLoopText prints the per-document report. A failing document
// prints its whole parse error: these errors already name the file and
// the field, and that detail is the entire repair.
func writeCoreLoopText(w io.Writer, definitions []app.CoreLoopDefinition) {
	failed := 0
	for _, definition := range definitions {
		if !definition.OK() {
			failed++
		}
	}
	if failed == 0 {
		fmt.Fprintf(w, "✓ Core loop definitions: %d\n", len(definitions))
	} else {
		fmt.Fprintf(w, "✗ Core loop definitions: %d of %d failed to load\n", failed, len(definitions))
	}
	for _, definition := range definitions {
		if !definition.OK() {
			// Indented as a block: a yaml decode error runs to several
			// lines, and unindented continuations read as a new section
			// rather than as the rest of this file's failure.
			fmt.Fprintf(w, "  ✗ %s\n%s\n", definition.File, indentBlock(definition.Err.Error(), "      "))
			continue
		}
		parent := definition.ParentName
		if parent == "" {
			parent = "(root)"
		}
		fmt.Fprintf(w, "  ✓ %-24s %s → %s\n", definition.File, definition.Name, parent)
		if len(definition.Tools) > 0 {
			fmt.Fprintf(w, "      tools: %s\n", strings.Join(definition.Tools, ", "))
		}
		for _, warning := range definition.Warnings {
			fmt.Fprintf(w, "      ! %s\n", warning)
		}
	}
}

// firstError returns the first non-nil error, so validate reports every
// section it can and still exits on the earliest thing serve would refuse
// over.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// admissionError converts failing root admission into the error that stops
// `thane validate && thane serve`.
//
// Only roots under verify_signatures: required produce it. That is not
// leniency — it mirrors what serve does, which is refuse on required and log
// on warn. Failing here for a warn root would make validate stricter than the
// gate it reports on, which breaks the guard just as surely as being more
// permissive.
func admissionError(results []app.RootAdmission) error {
	var fatal []string
	for _, result := range results {
		if result.Fatal() {
			fatal = append(fatal, result.Root)
		}
	}
	if len(fatal) == 0 {
		return nil
	}
	sort.Strings(fatal)
	return terminal(fmt.Errorf("document root admission failed for %s (see the report above; thane serve will refuse to start)",
		strings.Join(fatal, ", ")))
}

// writeAdmissionText prints the per-root admission report. A root that fails
// prints the full reason rather than a summary, because admission failures are
// repaired from the detail — which key signed, and what to declare.
func writeAdmissionText(w io.Writer, results []app.RootAdmission) {
	sort.Slice(results, func(i, j int) bool { return results[i].Root < results[j].Root })

	admitted := true
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Root)
		if !result.Admitted() {
			admitted = false
		}
	}
	if admitted {
		fmt.Fprintf(w, "✓ Root admission: %s\n", strings.Join(names, ", "))
		return
	}
	fmt.Fprint(w, "✗ Root admission\n\n")
	for _, result := range results {
		if result.Admitted() {
			fmt.Fprintf(w, "  ✓ %s\n", result.Root)
			continue
		}
		marker := "✗"
		if !result.Fatal() {
			// A warn root is reported without claiming serve will refuse,
			// because it will not.
			marker = "!"
		}
		// Reasons here are not always one line: anything wrapping a git failure
		// carries git's stderr, which is an error followed by usage advice, and
		// printed raw those continuation lines start at column zero — losing the
		// shape that says which root a reason belongs to.
		fmt.Fprintf(w, "  %s %s (%s)\n%s\n", marker, result.Root, result.Mode, indentBlock(result.Err.Error(), "      "))
	}
}

// integrityError converts a failing report into the error that makes
// `thane validate && thane serve` a real guard.
//
// Validate is documented as the pre-flight check for serve, so it has to
// fail on everything serve would refuse over. Returning success here
// while serve refuses would make the guard worse than useless: it would
// certify an instance that is about to be rejected, and it would do so
// in exactly the deploy scripts that trust it most.
func integrityError(report *coreintegrity.Report) error {
	if report == nil || report.OK() {
		return nil
	}
	names := make([]string, 0, len(report.Checks))
	for _, check := range report.Failures() {
		names = append(names, check.Name)
	}
	return terminal(fmt.Errorf("core integrity check failed for %s: %s (see the report above; thane serve will refuse to start)",
		report.CorePath, strings.Join(names, ", ")))
}

// checkCoreIntegrity runs the core checks for whichever instance this
// invocation targets, or returns nil when there is no instance to check.
//
// An explicit -config pointing outside any core names a file, not an
// instance. Reporting on the default workspace in that case would answer
// a question the operator did not ask, and would do it while they are
// looking at a different file entirely.
func checkCoreIntegrity(cfg *config.Config, configPath, workspacePath string) *coreintegrity.Report {
	// The flag is expanded before use. Taking it raw would check a
	// literal "~" directory whenever the shell did not expand it for us,
	// while the config had already loaded from the correct path — a
	// report about somewhere the instance does not live.
	workspace := ""
	if strings.TrimSpace(workspacePath) != "" {
		resolved, err := config.ExpandWorkspace(workspacePath)
		if err != nil {
			return nil
		}
		workspace = resolved
	}
	if workspace == "" && cfg != nil {
		workspace = cfg.Workspace.Path
	}
	if workspace == "" {
		if configPath != "" {
			return nil
		}
		resolved, err := config.ExpandWorkspace("")
		if err != nil {
			return nil
		}
		workspace = resolved
	}
	report, err := coreintegrity.Run(context.Background(), workspace, coreintegrity.Options{
		ConfigFileName: config.ConfigFileName,
		SeedSigners:    app.CoreSeedSigners(cfg),
	})
	if err != nil {
		return nil
	}
	return &report
}

// writeIntegrityText prints the core integrity report. Failures carry
// the command that resolves them, because the common case for reading
// this report is an instance that will not start and an operator who
// needs the next action rather than a diagnosis to interpret.
func writeIntegrityText(w io.Writer, report coreintegrity.Report) {
	if report.OK() {
		fmt.Fprintf(w, "✓ Core integrity: %s\n", report.CorePath)
		return
	}
	fmt.Fprintf(w, "✗ Core integrity: %s\n\n", report.CorePath)
	for _, check := range report.Checks {
		switch check.Status {
		case coreintegrity.StatusPass:
			fmt.Fprintf(w, "  ✓ %s\n", check.Name)
		case coreintegrity.StatusSkipped:
			fmt.Fprintf(w, "  - %s (not checked: %s)\n", check.Name, check.Detail)
		default:
			fmt.Fprintf(w, "  ✗ %s\n      %s\n", check.Name, check.Detail)
			if check.Fix != "" {
				fmt.Fprintf(w, "      fix: %s\n", check.Fix)
			}
		}
	}
}

// writeValidateText prints the per-section structural summary used by
// the default text output mode. Counts and presence checks are enough
// to confirm "is the config I edited really the one that loaded?"
// without dumping the parsed struct.
func writeValidateText(w io.Writer, cfg *config.Config) {
	fmt.Fprintf(w, "  Default model:        %s\n", cfg.Models.Default)
	fmt.Fprintf(w, "  Model resources:      %d\n", len(cfg.Models.Resources))
	fmt.Fprintf(w, "  Models available:     %d\n", len(cfg.Models.Available))
	// cfg.Roots is normalized into cfg.Paths during Load; count there.
	fmt.Fprintf(w, "  Document roots:       %d\n", len(cfg.Paths))
	fmt.Fprintf(w, "  Capability tags:      %d\n", len(cfg.CapabilityTags))
	fmt.Fprintf(w, "  Channel→tag binds:    %d\n", len(cfg.ChannelTags))
	fmt.Fprintf(w, "  MCP servers:          %d\n", len(cfg.MCP.Servers))
	fmt.Fprintf(w, "  Home Assistant:       %v\n", cfg.HomeAssistant.Configured())
	fmt.Fprintf(w, "  Signal bridge:        %v\n", cfg.Signal.Enabled)
	fmt.Fprintf(w, "  Embeddings:           %v\n", cfg.Embeddings.Enabled)
	// The core service loops are deliberately absent here: their config
	// enabled flags stopped stating whether they run once definition
	// documents became authoritative (#1361), and the Core loop
	// definitions section below reports what actually governs.
}

// writeValidateJSON emits the structured validation report. cfg may be
// nil when load failed; loadErr is non-nil when validation failed.
func writeValidateJSON(w io.Writer, cfgPath string, cfg *config.Config, loadErr error, integrity *coreintegrity.Report, admission []app.RootAdmission, coreLoops []app.CoreLoopDefinition, tlsErr error) error {
	type tlsJSON struct {
		Enabled   bool   `json:"enabled"`
		Hostnames int    `json:"hostnames,omitempty"`
		Provider  string `json:"dns_provider,omitempty"`
		Storage   string `json:"storage,omitempty"`
		OK        bool   `json:"ok"`
		Error     string `json:"error,omitempty"`
	}
	var tlsReport *tlsJSON
	if loadErr == nil && cfg != nil && cfg.TLS.Enabled {
		tlsReport = &tlsJSON{
			Enabled:   true,
			Hostnames: len(cfg.TLS.Hostnames),
			Provider:  cfg.TLS.CertMagic.DNS.Provider,
			Storage:   cfg.TLS.CertMagic.Storage,
			OK:        tlsErr == nil,
		}
		if tlsErr != nil {
			tlsReport.Error = tlsErr.Error()
		}
	}
	type rootAdmissionJSON struct {
		Root     string `json:"root"`
		RepoPath string `json:"repo_path,omitempty"`
		Mode     string `json:"mode"`
		Admitted bool   `json:"admitted"`
		Error    string `json:"error,omitempty"`
	}
	roots := make([]rootAdmissionJSON, 0, len(admission))
	for _, entry := range admission {
		row := rootAdmissionJSON{
			Root:     entry.Root,
			RepoPath: entry.RepoPath,
			Mode:     string(entry.Mode),
			Admitted: entry.Admitted(),
		}
		if entry.Err != nil {
			row.Error = entry.Err.Error()
		}
		roots = append(roots, row)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Root < roots[j].Root })

	// Path always emits (no omitempty) so the JSON schema is stable
	// for scripts piping into jq — even discovery-failure cases get
	// a path field, possibly empty.
	result := struct {
		Path      string                   `json:"path"`
		Valid     bool                     `json:"valid"`
		Error     string                   `json:"error,omitempty"`
		Summary   map[string]any           `json:"summary,omitempty"`
		Integrity *coreintegrity.Report    `json:"integrity,omitempty"`
		Roots     []rootAdmissionJSON      `json:"root_admission,omitempty"`
		CoreLoops []app.CoreLoopDefinition `json:"core_loop_definitions,omitempty"`
		TLS       *tlsJSON                 `json:"tls,omitempty"`
	}{
		Path:      cfgPath,
		Valid:     loadErr == nil && tlsErr == nil,
		Integrity: integrity,
		Roots:     roots,
		CoreLoops: coreLoops,
		TLS:       tlsReport,
	}
	if loadErr != nil {
		result.Error = loadErr.Error()
	} else if tlsErr != nil {
		// A failed front-door preflight is a config serve refuses, so it
		// reads as invalid here the way a parse failure does, not as a
		// valid file with a footnote.
		result.Error = tlsErr.Error()
	}
	if loadErr == nil && cfg != nil {
		result.Summary = map[string]any{
			"default_model":            cfg.Models.Default,
			"model_resources":          len(cfg.Models.Resources),
			"models_available":         len(cfg.Models.Available),
			"roots":                    len(cfg.Paths),
			"capability_tags":          len(cfg.CapabilityTags),
			"channel_tags":             len(cfg.ChannelTags),
			"mcp_servers":              len(cfg.MCP.Servers),
			"homeassistant_configured": cfg.HomeAssistant.Configured(),
			"signal_enabled":           cfg.Signal.Enabled,
			"embeddings_enabled":       cfg.Embeddings.Enabled,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// indentBlock prefixes every line of s, so a multi-line error stays
// visibly part of the entry it belongs to.
//
// It prefixes and nothing else. An earlier version trimmed each line
// first, which is the same mistake as trimming porcelain output: a yaml
// decode error indents its own detail lines under the message they
// belong to, and flattening that throws away structure the error author
// put there deliberately. Only the trailing newline goes, and only so
// the caller's own newline does not double.
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
