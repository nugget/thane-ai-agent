package app

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/app/coreloops"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// The Go-vs-document parity test that used to live here
// (TestCoreLoopDocsMatchTheGoSpecs) existed to make deleting the Go
// definition builders safe, and it has served its purpose: all three
// core service loops — metacognitive first (#1341), then ego and
// archivist — now define themselves solely through their shipped
// loops/ documents, and the builders are gone. What remains below is
// the document-side gate: every core service loop has a document, a
// default boot produces exactly the document's spec, and the embedded
// mirror matches the repo source.

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
		{&cfg.Metacognitive, "15m", "60m", "30m", 0.2, 0.1, 3, 8},
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
			if got.ParentName != selfContainerName {
				t.Errorf("parent_name = %q, want %q from the document itself — nothing assigns it afterwards", got.ParentName, selfContainerName)
			}
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

// TestArchivistUsesCanonicalContactDossiers pins the model-facing routing
// contract that keeps one person's history out of the generic dossier root.
// A configured runtime exposes the canonical contact dossier read/write pair
// through the archivist's contacts tag; the shipped task must teach the loop
// to choose them.
func TestArchivistUsesCanonicalContactDossiers(t *testing.T) {
	spec, err := decodeCoreLoopDefinition(filepath.Join("..", "..", "loops", "archivist.md"))
	if err != nil {
		t.Fatalf("decode archivist definition: %v", err)
	}
	for _, tag := range []string{"contacts", "documents"} {
		if !slices.Contains(spec.Tags, tag) {
			t.Errorf("archivist tags = %#v, want %q so the canonical dossier tools are reachable", spec.Tags, tag)
		}
	}
	for _, tool := range []string{"contact_dossier_read", "contact_dossier_write"} {
		if slices.Contains(spec.ExcludeTools, tool) {
			t.Errorf("archivist excludes %s despite teaching it as a canonical contact door", tool)
		}
	}
	normalizedTask := strings.Join(strings.Fields(spec.Task), " ")
	for _, want := range []string{
		"contacts:<uuid>.md",
		"contact_dossier_read",
		"An absent dossier is a successful, actionable result",
		"A `contact:<uuid>` item from `contact_save` names the exact current contact",
		"contact_dossier_write",
		"Never create or maintain a contact dossier under `dossiers:`",
		"`dossiers:entity-binary_sensor-game_room_door.md`",
		"retired `kb:dossiers/` namespace",
		"complete status-line, teaser, digest, and full projections",
		"archive:session:<full-session-uuid>",
		"never an 8-character prefix",
		"response's `truncated` marker",
		"use the returned canonical ref with `doc_outline`, verify that outline is not truncated",
		"recover every top-level section with `doc_section`",
		"call `queue_defer`",
	} {
		if !strings.Contains(normalizedTask, want) {
			t.Errorf("archivist task does not teach %q", want)
		}
	}
}

// TestCoreLoopDocumentsLoadWithoutPopulatedPaths is the regression test
// for the boot-ordering defect found in production 2026-08-12: the
// definition registry is built before App wiring populates Paths, so a
// loader reading only Paths["core"] sees an empty string and silently
// no-ops. The legacy config enabled flags masked this by seeding the
// embedded defaults; the day the last flag was removed, ego,
// metacognitive, archivist, and their self container vanished from a
// healthy-looking boot. The loader must resolve the core root with the
// same workspace fallback the validate pre-flight uses — this test is
// tonight's production shape: no Paths, no enabled flags, a real
// document on disk that must load anyway.
func TestCoreLoopDocumentsLoadWithoutPopulatedPaths(t *testing.T) {
	workspace := t.TempDir()
	loopsDir := filepath.Join(workspace, "core", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "# Watch\n\n## Spec\n\n```yaml\nname: doc_defined_watch\nenabled: true\noperation: service\nsleep_min: 5m0s\nsleep_max: 1h0m0s\nsleep_default: 10m0s\n```\n\n## Task\n\nWatch something.\n"
	if err := os.WriteFile(filepath.Join(loopsDir, "watch.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Workspace.Path = workspace
	// Paths deliberately nil and every legacy enabled flag deliberately
	// false: nothing may seed but the document itself.
	app := &App{cfg: cfg}

	specs, err := app.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}
	found := false
	for _, spec := range specs {
		if spec.Name == "doc_defined_watch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("document-defined loop did not load without populated Paths; specs: %d", len(specs))
	}
}
