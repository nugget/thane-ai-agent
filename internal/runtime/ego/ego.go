// Package ego carries the config-parsing and hydration seam for the
// ego self-reflection loop. The loop definition itself — prompt, spec,
// cadence, tool exclusions — lives in the shipped document loops/ego.md
// (embedded via internal/app/coreloops and overridable from the core
// root's loops/ directory); the Go definition builder was deleted once
// the docs-parity gate proved the document byte-for-byte equivalent.
// What remains here is what a document cannot carry: parsing the YAML
// config shape into durations and defaulting the definition name at
// hydration.
//
// The agent's core context provider reads ego.md every turn and injects
// it into the system prompt; the ego loop is its sole writer.
package ego

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// DefinitionName is the durable loop definition name for the ego
// service.
const DefinitionName = "ego"

// stateFileName is the fixed filename of the ego self-reflection
// document. The runtime places it under workspace/core.
const stateFileName = "ego.md"

// Config holds the parsed ego loop configuration with time.Duration
// fields (as opposed to the YAML string representation in
// [config.EgoConfig]).
type Config struct {
	Enabled                bool
	StateFile              string
	MinSleep               time.Duration
	MaxSleep               time.Duration
	DefaultSleep           time.Duration
	Jitter                 float64
	SupervisorProbability  float64
	QualityFloor           int
	SupervisorQualityFloor int
}

// ParseConfig converts a [config.EgoConfig] (string durations) into a
// [Config] (time.Duration fields). Call after config validation has
// passed.
func ParseConfig(raw config.EgoConfig) (Config, error) {
	minSleep, err := time.ParseDuration(raw.MinSleep)
	if err != nil {
		return Config{}, fmt.Errorf("min_sleep %q: %w", raw.MinSleep, err)
	}
	maxSleep, err := time.ParseDuration(raw.MaxSleep)
	if err != nil {
		return Config{}, fmt.Errorf("max_sleep %q: %w", raw.MaxSleep, err)
	}
	defaultSleep, err := time.ParseDuration(raw.DefaultSleep)
	if err != nil {
		return Config{}, fmt.Errorf("default_sleep %q: %w", raw.DefaultSleep, err)
	}
	return Config{
		Enabled:                raw.Enabled,
		StateFile:              stateFileName,
		MinSleep:               minSleep,
		MaxSleep:               maxSleep,
		DefaultSleep:           defaultSleep,
		Jitter:                 derefFloat(raw.Jitter, 0.2),
		SupervisorProbability:  derefFloat(raw.SupervisorProbability, 0.2),
		QualityFloor:           raw.Router.QualityFloor,
		SupervisorQualityFloor: raw.SupervisorRouter.QualityFloor,
	}, nil
}

// derefFloat returns *p when non-nil, otherwise def. Used to apply
// the package-level fallback when ParseConfig is called with a raw
// config struct that bypassed [config.Config.applyDefaults] (typically
// in tests).
func derefFloat(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// HydrateSpec fills in what a durable loop definition cannot carry —
// which is now only defaulting the name. It plays no part in output
// wiring: the write tool (replace_output_ego_state) and the read-side
// document tools are generated from the spec's declared outputs during
// app hydration, and the model is the document's only author, through
// that generated tool.
func HydrateSpec(spec loop.Spec, _ Config) loop.Spec {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = DefinitionName
	}
	return spec
}
