// Package archivist implements the memory archivist loop — a baked-in
// service loop that tends thane's accumulated understanding of its own
// corpus. Where the ego loop maintains self-reflection and the
// metacognitive loop watches in-flight behavior, the archivist
// synthesizes durable knowledge across the memory silos (archive,
// session summaries, working memory, facts, documents, contacts) into
// long-lived dossiers keyed by subject.
//
// The archivist is self-paced and pull-based. It is NOT woken by events;
// producers (session close, frontier expansion, future MQTT) enqueue
// work into a durable, deduped work queue (internal/state/loopqueue)
// keyed to this loop, and the archivist drains its own partition on its
// own sleep envelope. That decoupling is the structural point: trigger
// rate never drives work rate, so a burst of closed sessions can't
// amplify into a burst of expensive iterations (issue #1024). A
// librarian working through an in-tray, not a clerk paged on every bell.
//
// State persists across iterations via a markdown file (archivist.md by
// default). Each iteration is one fresh conversation: the model pulls a
// batch from its queue, walks the silos, writes or refreshes dossiers
// via the documents tools, acks what it finished, and enqueues any
// newly-discovered related subjects (frontier-as-enqueue, never a spawn).
package archivist

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// DefinitionName is the durable loop definition name for the archivist
// service. Used as both the loop registry name and the routing factor
// ("source: archivist"), and as the work-queue partition key.
const DefinitionName = "archivist"

// stateFileName is the fixed filename of the archivist's state
// document. The runtime places it under workspace/core, alongside
// ego.md and metacognitive.md. The interactive agent doesn't read
// this file the way it reads ego.md — it's the archivist's own
// memory between iterations.
const stateFileName = "archivist.md"

// Config holds the parsed archivist loop configuration with
// time.Duration fields (the YAML representation in
// [config.ArchivistConfig] is strings).
//
// Sleep envelope chosen deliberately wider than metacog and narrower
// than ego: the archivist's work is real synthesis (slower than
// metacog's quick observations) but doesn't need to wait the long
// stretches ego does for genuine introspective drift. A default cadence
// around an hour gives the corpus time to accumulate new evidence
// between passes without the archivist running stale.
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

// ParseConfig converts a [config.ArchivistConfig] (string durations)
// into a [Config] (time.Duration fields). Call after config validation
// has passed.
func ParseConfig(raw config.ArchivistConfig) (Config, error) {
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
// archivist service. Runtime hooks are attached later by [HydrateSpec]
// (and the work-queue tools by the app-level hydration) so the
// definition can live in the durable registry alongside metacognitive
// and ego.
func DefinitionSpec(cfg Config) loop.Spec {
	return loop.Spec{
		Name:       DefinitionName,
		Enabled:    cfg.Enabled,
		Task:       prompts.ArchivistBaseTemplate,
		Operation:  loop.OperationService,
		Completion: loop.CompletionNone,
		Outputs: []loop.OutputSpec{
			{
				Name: "archivist_state",
				Type: loop.OutputTypeMaintainedDocument,
				Ref:  "core:archivist.md",
				Mode: loop.OutputModeReplace,
				Purpose: "Archivist working state written by the archivist loop, for the archivist. Tracks: " +
					"the subjects worked this pass, dossier pointers (which dossiers exist and where), " +
					"and notes from the last few iterations. Read each turn so the archivist picks up where " +
					"it left off. The durable work queue (not this file) holds pending subjects. NOT a " +
					"public-facing document; the dossiers themselves are the model-facing output.",
			},
		},
		SleepMin:     cfg.MinSleep,
		SleepMax:     cfg.MaxSleep,
		SleepDefault: cfg.DefaultSleep,
		Jitter:       loop.Float64Ptr(cfg.Jitter),
		ExcludeTools: archivistExcludeTools,
		// Tags = capability surfaces the archivist boots with active.
		// `archivist` is the loop's own identity tag; the rest match the
		// tool families the archivist's prompt promises it can use
		// (archive_search, recall_fact, contact_lookup, the documents
		// tools for dossier writes). Since ExcludeTools blocks
		// tag_activate, the archivist can't expand its surface at
		// runtime — it has to ship with the right initial set. The
		// work-queue tools are injected as loop-private RuntimeTools at
		// app hydration, not via a capability tag.
		Tags: []string{"archivist", "documents", "archive", "memory", "contacts"},
		Profile: router.LoopProfile{
			Mission:          "archivist",
			DelegationGating: "disabled",
			QualityFloor:     cfg.QualityFloor,
			ExtraHints:       map[string]string{"source": "archivist"},
		},
		SupervisorProfile: supervisorProfile(cfg.SupervisorQualityFloor),
		Supervisor:        cfg.SupervisorProbability > 0,
		SupervisorProb:    cfg.SupervisorProbability,
		Metadata: map[string]string{
			"subsystem": "archivist",
			"category":  "service",
		},
	}
}

// supervisorProfile builds the archivist's supervisor-turn overlay: the
// frontier-review prompt prefix (always) plus a higher quality floor when
// one is configured. Unset fields fall back to the normal Profile during
// supervisor turns; the prefix is prepended to the Task.
func supervisorProfile(qualityFloor int) *router.LoopProfile {
	p := &router.LoopProfile{Instructions: prompts.ArchivistSupervisorInstructions}
	if qualityFloor > 0 {
		p.QualityFloor = qualityFloor
	}
	return p
}

// HydrateSpec defaults the definition name. The archivist's prompt is
// declarative (the spec Task and SupervisorProfile.Instructions); its one
// genuine runtime-only dependency — the loop-private work-queue tools — is
// attached separately at app hydration, not here.
func HydrateSpec(spec loop.Spec, _ Config) loop.Spec {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = DefinitionName
	}
	return spec
}

// BuildSpec returns a [loop.Spec] that implements the archivist loop as
// a service loop. The returned spec declares the durable output document
// and uses runtime hooks to build prompts.
func BuildSpec(cfg Config) loop.Spec {
	return HydrateSpec(DefinitionSpec(cfg), cfg)
}

// BuildLoopConfig returns the engine-facing [loop.Config] view of the
// archivist loop. Kept as a compatibility shim for callers that work
// directly with [loop.Config] rather than [loop.Spec].
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

// archivistExcludeTools lists tools the archivist loop should not have
// access to. The archivist's writes go through the managed output
// (`core:archivist.md`) and the documents tools (for dossiers in the
// `dossiers/` namespace) — bare workspace file tools, exec, session
// management, tag manipulation, and direct human-egress channels are
// out of scope for a synthesis loop.
//
// Spawn tools are excluded too: the archivist is a background-class
// consumer with zero spawn rights (issue #1024). It self-feeds its work
// queue via queue_enqueue; it never launches loops. This is the
// per-class half of the fork-bomb guard.
//
// Read tools are deliberately left in: archive_search, recall_fact,
// contact_lookup, working memory access — the archivist needs to walk
// across silos to do its job.
var archivistExcludeTools = append([]string{
	"file_read", "file_write", "file_edit", "file_list",
	"file_search", "file_grep", "file_stat", "file_tree",
	"exec",
	"conversation_reset", "session_close", "session_split", "session_checkpoint",
	"create_temp_file",
	"tag_activate", "tag_deactivate",
	"spawn_loop", "thane_now", "thane_assign", "thane_curate", "thane_create_container",
}, tools.DirectHumanEgressToolNames()...)
