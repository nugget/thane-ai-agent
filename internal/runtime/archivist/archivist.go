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
//
// The loop definition itself — prompt, spec, cadence, tool exclusions —
// lives in the shipped document loops/archivist.md (embedded via
// internal/app/coreloops and overridable from the core root's loops/
// directory); the Go definition builder was deleted once the
// docs-parity gate proved the document byte-for-byte equivalent. What
// remains here is what a document cannot carry: parsing the YAML config
// shape into durations and defaulting the definition name at hydration.
// The loop-private work-queue tools are attached separately at app
// hydration.
package archivist

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
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
