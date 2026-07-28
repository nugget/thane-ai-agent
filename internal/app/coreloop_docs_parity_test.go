package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/app/coreloops"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
	"github.com/nugget/thane-ai-agent/internal/runtime/ego"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
)

// TestCoreLoopDocsMatchTheGoSpecs is what makes deleting the Go
// definitions safe.
//
// The documents in loops/ were generated from these functions rather
// than transcribed, and this asserts the round trip: parsing the shipped
// document must reproduce exactly the spec the Go function builds for a
// default install. Anything the port changed — a dropped field, a
// mangled prompt, a default that did not survive — shows up here as a
// diff rather than as behaviour nobody notices until a loop runs wrong.
//
// It also keeps them in step afterwards. While both exist, editing one
// without the other fails.
func TestCoreLoopDocsMatchTheGoSpecs(t *testing.T) {
	cfg := defaultedServiceLoopConfig(t)

	metacogCfg, err := metacognitive.ParseConfig(cfg.Metacognitive)
	if err != nil {
		t.Fatalf("metacognitive.ParseConfig: %v", err)
	}
	egoCfg, err := ego.ParseConfig(cfg.Ego)
	if err != nil {
		t.Fatalf("ego.ParseConfig: %v", err)
	}
	archivistCfg, err := archivist.ParseConfig(cfg.Archivist)
	if err != nil {
		t.Fatalf("archivist.ParseConfig: %v", err)
	}

	tests := []struct {
		file string
		want looppkg.Spec
	}{
		{"metacognitive.md", metacognitive.DefinitionSpec(metacogCfg)},
		{"ego.md", ego.DefinitionSpec(egoCfg)},
		{"archivist.md", archivist.DefinitionSpec(archivistCfg)},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := decodeCoreLoopDefinition(filepath.Join("..", "..", "loops", tt.file))
			if err != nil {
				t.Fatalf("decodeCoreLoopDefinition: %v", err)
			}
			// ParentName is applied by the caller, not carried by either
			// source, so it is not part of what the document owns.
			got.ParentName = tt.want.ParentName
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("document and Go spec disagree.\nfrom document: %#v\nfrom Go:       %#v", got, tt.want)
			}
		})
	}
}

// TestEveryCoreServiceLoopHasADocument stops a fourth core service loop
// from being added in Go without a shipped document, which would leave
// one loop on the old path with nothing saying so.
func TestEveryCoreServiceLoopHasADocument(t *testing.T) {
	for _, reg := range coreServiceLoops {
		path := filepath.Join("..", "..", "loops", reg.Name+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("core service loop %q has no loops/%s.md", reg.Name, reg.Name)
		}
	}
}

// defaultedServiceLoopConfig mirrors the table in config.applyDefaults.
// The shipped documents encode a default install, so a field this misses
// is a zero value baked into what every new install boots with.
func defaultedServiceLoopConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	for _, d := range []struct {
		c                                *config.ServiceLoopConfig
		minSleep, maxSleep, defaultSleep string
		jitter, supervisorProb           float64
		floor, supervisorFloor           int
	}{
		{&cfg.Metacognitive, "2m", "30m", "10m", 0.2, 0.1, 3, 8},
		{&cfg.Ego, "30m", "24h", "6h", 0.2, 0.2, 5, 8},
		{&cfg.Archivist, "15m", "12h", "1h", 0.2, 0.1, 5, 8},
	} {
		d.c.Enabled = true
		d.c.MinSleep, d.c.MaxSleep, d.c.DefaultSleep = d.minSleep, d.maxSleep, d.defaultSleep
		jitter, prob := d.jitter, d.supervisorProb
		d.c.Jitter, d.c.SupervisorProbability = &jitter, &prob
		d.c.Router.QualityFloor = d.floor
		d.c.SupervisorRouter.QualityFloor = d.supervisorFloor
	}
	return cfg
}

// TestBuiltInSpecsNowComeFromTheShippedDocuments closes the loop the
// parity test opened. The Go builders are no longer the runtime source
// — the shipped document is — so this asserts the definition a default
// boot actually produces is the document's, and that it still equals
// what the Go builder would have made.
//
// Both halves matter. The first says the swap took effect; the second
// says it changed nothing.
func TestBuiltInSpecsNowComeFromTheShippedDocuments(t *testing.T) {
	cfg := defaultedServiceLoopConfig(t)
	cfg.Paths = map[string]string{"core": t.TempDir()} // no override present
	app := &App{cfg: cfg}

	specs, err := app.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}
	byName := make(map[string]looppkg.Spec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}

	for _, name := range []string{"ego", "metacognitive", "archivist"} {
		t.Run(name, func(t *testing.T) {
			got, ok := byName[name]
			if !ok {
				t.Fatalf("%s is absent from a default boot", name)
			}
			want, err := decodeCoreLoopDefinition(filepath.Join("..", "..", "loops", name+".md"))
			if err != nil {
				t.Fatalf("decodeCoreLoopDefinition: %v", err)
			}
			// ParentName is applied by the caller after the spec is built.
			want.ParentName = got.ParentName
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("the booted spec is not the shipped document's:\ngot  %#v\nwant %#v", got, want)
			}
			if got.Task == "" {
				t.Error("booted with an empty task")
			}
		})
	}
}

// TestEmbeddedDocumentsMatchTheRepoSources guards the generated mirror.
// internal/app/coreloops/defaults is a copy made by go:generate, so an
// edit to loops/ that never ran generate would ship the old prompt while
// the repo shows the new one.
func TestEmbeddedDocumentsMatchTheRepoSources(t *testing.T) {
	for _, name := range []string{"ego", "metacognitive", "archivist"} {
		source, err := os.ReadFile(filepath.Join("..", "..", "loops", name+".md"))
		if err != nil {
			t.Fatalf("read source: %v", err)
		}
		embedded, err := coreloops.Documents.ReadFile("defaults/" + name + ".md")
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if string(source) != string(embedded) {
			t.Errorf("loops/%s.md and its embedded copy differ; run `just generate`", name)
		}
	}
}
