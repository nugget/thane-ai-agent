package app

import (
	"fmt"
	"path/filepath"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/curator"
	"github.com/nugget/thane-ai-agent/internal/runtime/ego"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
)

// coreServiceRegistration describes one model-facing core service loop
// (ego, metacognitive, curator). The three loops share a parallel
// construction shape — parse a config sub-struct, cache it on the App,
// emit a durable definition spec, and attach runtime hooks at hydration
// time — that previously lived as repeated three-way blocks across
// buildLoopDefinitionBaseSpecs and hydrateLoopDefinitionSpec. Collapsing
// them behind this descriptor means adding a fourth core loop is one new
// entry in [coreServiceLoops] rather than an edit to every wiring site.
//
// The closures intentionally operate on *App: each loop's config has a
// distinct type (ego.Config vs metacognitive.Config vs curator.Config),
// so the descriptor can't hold a typed pointer. Instead ParseAndCache
// writes the typed cache field (a.egoCfg, etc.) and the later closures
// read it back. Metacognitive's extra hydration input (Opts with the
// resolved state-file path) is absorbed by its Hydrate closure, keeping
// the dispatcher uniform across all three.
type coreServiceRegistration struct {
	// Name is the durable loop definition name, matched against
	// spec.Name during hydration and used to dedupe against
	// operator-declared definitions.
	Name string

	// ConfigEnabled reports whether the loop's config block has
	// Enabled set. A disabled loop with no operator-declared override
	// is skipped entirely.
	ConfigEnabled func(*config.Config) bool

	// ParseAndCache parses the loop's config sub-struct and stores the
	// typed result on the App for later DefinitionSpec/Hydrate reads.
	ParseAndCache func(*App, *config.Config) error

	// DefinitionSpec builds the persistable loop definition from the
	// cached config. Only called after ParseAndCache has run.
	DefinitionSpec func(*App) looppkg.Spec

	// Hydrate attaches runtime-only hooks (task builder, post-iterate,
	// etc.) to a stored definition spec. Returns an error when the
	// cached config is missing — a definition can't be hydrated
	// without the config that shapes its runtime behavior.
	Hydrate func(*App, looppkg.Spec) (looppkg.Spec, error)
}

// coreServiceLoops is the registry of model-facing core service loops.
// Order matches the historical append/parse sequence (metacognitive,
// ego, curator) so the resulting base-definition ordering is unchanged.
var coreServiceLoops = []coreServiceRegistration{
	metacognitiveRegistration,
	egoRegistration,
	curatorRegistration,
}

// coreServiceLoopByName indexes coreServiceLoops for O(1) hydration
// dispatch by definition name.
var coreServiceLoopByName = func() map[string]coreServiceRegistration {
	m := make(map[string]coreServiceRegistration, len(coreServiceLoops))
	for _, reg := range coreServiceLoops {
		m[reg.Name] = reg
	}
	return m
}()

var metacognitiveRegistration = coreServiceRegistration{
	Name:          metacognitive.DefinitionName,
	ConfigEnabled: func(c *config.Config) bool { return c.Metacognitive.Enabled },
	ParseAndCache: func(a *App, c *config.Config) error {
		cfg, err := metacognitive.ParseConfig(c.Metacognitive)
		if err != nil {
			return err
		}
		a.metacogCfg = &cfg
		return nil
	},
	DefinitionSpec: func(a *App) looppkg.Spec {
		return metacognitive.DefinitionSpec(*a.metacogCfg)
	},
	Hydrate: func(a *App, spec looppkg.Spec) (looppkg.Spec, error) {
		if a.metacogCfg == nil {
			return looppkg.Spec{}, fmt.Errorf("metacognitive definition requires metacognitive config")
		}
		stateFileName := filepath.Base(a.metacogCfg.StateFile)
		stateFilePath := coreFilePath(a.cfg.Workspace.Path, stateFileName)
		return metacognitive.HydrateSpec(spec, *a.metacogCfg, metacognitive.Opts{
			StateFilePath: stateFilePath,
			StateFileName: stateFileName,
		}), nil
	},
}

var egoRegistration = coreServiceRegistration{
	Name:          ego.DefinitionName,
	ConfigEnabled: func(c *config.Config) bool { return c.Ego.Enabled },
	ParseAndCache: func(a *App, c *config.Config) error {
		cfg, err := ego.ParseConfig(c.Ego)
		if err != nil {
			return err
		}
		a.egoCfg = &cfg
		return nil
	},
	DefinitionSpec: func(a *App) looppkg.Spec {
		return ego.DefinitionSpec(*a.egoCfg)
	},
	Hydrate: func(a *App, spec looppkg.Spec) (looppkg.Spec, error) {
		if a.egoCfg == nil {
			return looppkg.Spec{}, fmt.Errorf("ego definition requires ego config")
		}
		return ego.HydrateSpec(spec, *a.egoCfg), nil
	},
}

var curatorRegistration = coreServiceRegistration{
	Name:          curator.DefinitionName,
	ConfigEnabled: func(c *config.Config) bool { return c.Curator.Enabled },
	ParseAndCache: func(a *App, c *config.Config) error {
		cfg, err := curator.ParseConfig(c.Curator)
		if err != nil {
			return err
		}
		a.curatorCfg = &cfg
		return nil
	},
	DefinitionSpec: func(a *App) looppkg.Spec {
		return curator.DefinitionSpec(*a.curatorCfg)
	},
	Hydrate: func(a *App, spec looppkg.Spec) (looppkg.Spec, error) {
		if a.curatorCfg == nil {
			return looppkg.Spec{}, fmt.Errorf("curator definition requires curator config")
		}
		return curator.HydrateSpec(spec, *a.curatorCfg), nil
	},
}
