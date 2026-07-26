package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/coreintegrity"
)

// runValidate parses and validates the config that would be loaded by
// `thane serve`. It does not start any services or open any sockets —
// this is purely a pre-flight gate for scripts and operators.
//
// The configPath argument follows the same convention as other
// subcommands: when empty, [config.FindConfig] walks the standard
// search order; when set, that exact path is used. The returned error
// signals "config is invalid" so the binary exits non-zero, which
// makes `thane validate && thane serve` a usable deploy guard.
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
	if outputFmt == "json" {
		// Always emit JSON to stdout, even on failure — scripts may
		// want the structured error. The error is still returned so
		// the exit code reflects validity.
		if err := writeValidateJSON(w, cfgPath, cfg, loadErr, integrity); err != nil {
			return err
		}
		return loadErr
	}
	if loadErr != nil {
		// Config could not load, but the integrity report is often the
		// reason and always the next place to look, so print it rather
		// than leaving the operator with a bare parse error.
		if integrity != nil {
			writeIntegrityText(w, *integrity)
		}
		return loadErr
	}
	fmt.Fprintf(w, "✓ Config valid: %s\n\n", cfgPath)
	writeValidateText(w, cfg)
	if integrity != nil {
		fmt.Fprintln(w)
		writeIntegrityText(w, *integrity)
	}
	return nil
}

// checkCoreIntegrity runs the core checks for whichever instance this
// invocation targets, or returns nil when there is no instance to check.
//
// An explicit -config pointing outside any core names a file, not an
// instance. Reporting on the default workspace in that case would answer
// a question the operator did not ask, and would do it while they are
// looking at a different file entirely.
func checkCoreIntegrity(cfg *config.Config, configPath, workspacePath string) *coreintegrity.Report {
	workspace := workspacePath
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
	fmt.Fprintf(w, "  Metacognitive loop:   %v\n", cfg.Metacognitive.Enabled)
	fmt.Fprintf(w, "  Ego loop:             %v\n", cfg.Ego.Enabled)
}

// writeValidateJSON emits the structured validation report. cfg may be
// nil when load failed; loadErr is non-nil when validation failed.
func writeValidateJSON(w io.Writer, cfgPath string, cfg *config.Config, loadErr error, integrity *coreintegrity.Report) error {
	// Path always emits (no omitempty) so the JSON schema is stable
	// for scripts piping into jq — even discovery-failure cases get
	// a path field, possibly empty.
	result := struct {
		Path      string                `json:"path"`
		Valid     bool                  `json:"valid"`
		Error     string                `json:"error,omitempty"`
		Summary   map[string]any        `json:"summary,omitempty"`
		Integrity *coreintegrity.Report `json:"integrity,omitempty"`
	}{
		Path:      cfgPath,
		Valid:     loadErr == nil,
		Integrity: integrity,
	}
	if loadErr != nil {
		result.Error = loadErr.Error()
	} else if cfg != nil {
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
			"metacognitive_enabled":    cfg.Metacognitive.Enabled,
			"ego_enabled":              cfg.Ego.Enabled,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
