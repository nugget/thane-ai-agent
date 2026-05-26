package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
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
func runValidate(w io.Writer, configPath, outputFmt string) error {
	cfg, cfgPath, loadErr := loadConfig(configPath)
	if outputFmt == "json" {
		// Always emit JSON to stdout, even on failure — scripts may
		// want the structured error. The error is still returned so
		// the exit code reflects validity.
		if err := writeValidateJSON(w, cfgPath, cfg, loadErr); err != nil {
			return err
		}
		return loadErr
	}
	if loadErr != nil {
		return loadErr
	}
	fmt.Fprintf(w, "✓ Config valid: %s\n\n", cfgPath)
	writeValidateText(w, cfg)
	return nil
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
func writeValidateJSON(w io.Writer, cfgPath string, cfg *config.Config, loadErr error) error {
	result := struct {
		Path    string         `json:"path,omitempty"`
		Valid   bool           `json:"valid"`
		Error   string         `json:"error,omitempty"`
		Summary map[string]any `json:"summary,omitempty"`
	}{
		Path:  cfgPath,
		Valid: loadErr == nil,
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
