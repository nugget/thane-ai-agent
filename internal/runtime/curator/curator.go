// Package curator implements the memory curator loop — a baked-in
// service loop that tends thane's accumulated understanding of its
// own corpus. Where the ego loop maintains self-reflection and the
// metacognitive loop watches in-flight behavior, the curator
// synthesizes durable knowledge across the memory silos (archive,
// session summaries, working memory, facts, documents, contacts)
// into long-lived dossiers keyed by subject.
//
// The curator is self-paced. Unlike the Go-side session summarizer
// worker (which is event-triggered on session close), it has its
// own sleep envelope and picks its own target each iteration —
// "what aspect of memory does the corpus want me to refine right
// now?" That autonomy is the structural point: a librarian, not a
// clerk.
//
// State persists across iterations via a markdown file
// (curator.md by default). The current iteration is one fresh
// conversation; the model reads its prior queue and dossier
// pointers from the state file, picks a subject, walks the
// silos, and writes a dossier — or updates an existing one — via
// the documents tools.
package curator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// DefinitionName is the durable loop definition name for the
// curator service. Used as both the loop registry name and the
// routing factor ("source: curator").
const DefinitionName = "curator"

// stateFileName is the fixed filename of the curator's state
// document. The runtime places it under workspace/core, alongside
// ego.md and metacognitive.md. The interactive agent doesn't read
// this file the way it reads ego.md — it's the curator's own
// memory between iterations.
const stateFileName = "curator.md"

// Config holds the parsed curator loop configuration with
// time.Duration fields (the YAML representation in
// [config.CuratorConfig] is strings).
//
// Sleep envelope chosen deliberately wider than metacog and
// narrower than ego: the curator's work is real synthesis (slower
// than metacog's quick observations) but doesn't need to wait the
// long stretches ego does for genuine introspective drift. A
// default cadence around an hour gives the corpus time to
// accumulate new evidence between passes without the curator
// running stale.
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

// ParseConfig converts a [config.CuratorConfig] (string durations)
// into a [Config] (time.Duration fields). Call after config
// validation has passed.
func ParseConfig(raw config.CuratorConfig) (Config, error) {
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
		SupervisorProbability:  derefFloat(raw.SupervisorProbability, 0.1),
		QualityFloor:           raw.Router.QualityFloor,
		SupervisorQualityFloor: raw.SupervisorRouter.QualityFloor,
	}, nil
}

// derefFloat returns *p when non-nil, otherwise def. Used to apply
// package-level fallbacks when ParseConfig is called with a raw
// config struct that bypassed [config.Config.applyDefaults]
// (typically in tests).
func derefFloat(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// DefinitionSpec returns the persistable loop definition for the
// curator service. Runtime hooks are attached later by [HydrateSpec]
// so the definition can live in the durable registry alongside
// metacognitive and ego.
func DefinitionSpec(cfg Config) loop.Spec {
	return loop.Spec{
		Name:       DefinitionName,
		Enabled:    cfg.Enabled,
		Task:       "Tend thane's understanding across memory silos. Pick one subject per iteration and refine its dossier.",
		Operation:  loop.OperationService,
		Completion: loop.CompletionNone,
		Outputs: []loop.OutputSpec{
			{
				Name: "curator_state",
				Type: loop.OutputTypeMaintainedDocument,
				Ref:  "core:curator.md",
				Mode: loop.OutputModeReplace,
				Purpose: "Curator working state written by the curator loop, for the curator. Tracks: " +
					"the subject worked on this pass, the queue of subjects pending or worth revisiting, " +
					"dossier pointers (which dossiers exist and where), and notes from the last few " +
					"iterations. Read each turn so the curator picks up where it left off. NOT a " +
					"public-facing document; the dossiers themselves are the model-facing output.",
			},
		},
		SleepMin:     cfg.MinSleep,
		SleepMax:     cfg.MaxSleep,
		SleepDefault: cfg.DefaultSleep,
		Jitter:       loop.Float64Ptr(cfg.Jitter),
		ExcludeTools: curatorExcludeTools,
		Tags:         []string{"curator"},
		Profile: router.LoopProfile{
			Mission:          "curator",
			DelegationGating: "disabled",
			QualityFloor:     cfg.QualityFloor,
			ExtraHints:       map[string]string{"source": "curator"},
		},
		SupervisorProfile: supervisorProfile(cfg.SupervisorQualityFloor),
		Supervisor:        cfg.SupervisorProbability > 0,
		SupervisorProb:    cfg.SupervisorProbability,
		Metadata: map[string]string{
			"subsystem": "curator",
			"category":  "service",
		},
	}
}

// supervisorProfile builds the curator's per-turn-mode overrides
// from the supervisor router config. Returns nil when no overrides
// are declared, signaling the loop runtime to reuse the normal
// Profile during supervisor turns.
func supervisorProfile(qualityFloor int) *router.LoopProfile {
	if qualityFloor <= 0 {
		return nil
	}
	return &router.LoopProfile{
		QualityFloor: qualityFloor,
	}
}

// HydrateSpec attaches the runtime-only hooks needed to execute the
// curator service from a durable loop definition. The TaskBuilder
// emits [prompts.CuratorPrompt] for each iteration; the prior
// state file content is injected via the managed-output context
// provider, not this prompt.
func HydrateSpec(spec loop.Spec, _ Config) loop.Spec {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = DefinitionName
	}
	spec.TaskBuilder = func(_ context.Context, isSupervisor bool) (string, error) {
		return prompts.CuratorPrompt(isSupervisor), nil
	}
	return spec
}

// BuildSpec returns a [loop.Spec] that implements the curator loop
// as a service loop. The returned spec declares the durable output
// document and uses runtime hooks to build prompts.
func BuildSpec(cfg Config) loop.Spec {
	return HydrateSpec(DefinitionSpec(cfg), cfg)
}

// BuildLoopConfig returns the engine-facing [loop.Config] view of
// the curator loop. Kept as a compatibility shim for callers that
// work directly with [loop.Config] rather than [loop.Spec].
func BuildLoopConfig(cfg Config) loop.Config {
	spec := BuildSpec(cfg)
	out := spec.ToConfig()
	profileHints := spec.Profile.RoutingFactors()
	if len(profileHints) > 0 {
		if out.RoutingFactors == nil {
			out.RoutingFactors = make(map[string]string, len(profileHints))
		}
		for k, v := range profileHints {
			out.RoutingFactors[k] = v
		}
	}
	if spec.Profile.DelegationGating != "" {
		out.DelegationGating = spec.Profile.DelegationGating
	}
	return out
}

// curatorExcludeTools lists tools the curator loop should not have
// access to. The curator's writes go through the managed output
// (`core:curator.md`) and the documents tools (for dossiers in the
// `dossiers/` namespace) — bare workspace file tools, exec, session
// management, tag manipulation, and direct human-egress channels
// are out of scope for a synthesis loop.
//
// Read tools are deliberately left in: archive_search, recall_fact,
// contact_lookup, working memory access — the curator needs to walk
// across silos to do its job.
var curatorExcludeTools = append([]string{
	"file_read", "file_write", "file_edit", "file_list",
	"file_search", "file_grep", "file_stat", "file_tree",
	"exec",
	"conversation_reset", "session_close", "session_split", "session_checkpoint",
	"create_temp_file",
	"tag_activate", "tag_deactivate",
}, tools.DirectHumanEgressToolNames()...)
