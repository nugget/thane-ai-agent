package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// writeCoreLoop puts one definition file in <core>/loops and returns the
// core root, which is what the loader is given.
func writeCoreLoop(t *testing.T, files map[string]string) string {
	t.Helper()
	core := t.TempDir()
	dir := filepath.Join(core, coreLoopsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return core
}

const minimalCoreLoop = `name: ranch_watch
enabled: true
task: Watch the ranch.
intent: Keep ranch conditions legible.
operation: service
completion: none
sleep_min: 15m
sleep_max: 12h
`

func TestCoreLoopDefinitionsLoadFromYAML(t *testing.T) {
	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.yaml": minimalCoreLoop}))
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("loaded %d specs, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Name != "ranch_watch" || spec.Intent != "Keep ranch conditions legible." {
		t.Fatalf("decoded spec = %#v", spec)
	}
	if spec.SleepMin != 15*time.Minute {
		t.Errorf("sleep_min = %v, want 15m — a duration string has to survive the decode", spec.SleepMin)
	}
}

func TestMissingCoreLoopsDirectoryIsNotAnError(t *testing.T) {
	// An install with no core-defined loops is ordinary.
	specs, err := loadCoreLoopDefinitions(t.TempDir())
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("loaded %d specs from an absent directory", len(specs))
	}
	if specs, err = loadCoreLoopDefinitions(""); err != nil || specs != nil {
		t.Fatalf("empty core path: specs = %v, err = %v", specs, err)
	}
}

// TestMisspelledKeyIsRefused is why KnownFields is on. A typo that
// decodes to nothing produces a loop that boots, ignores the setting the
// operator wrote, and offers no evidence beyond behaviour that never
// changes.
func TestMisspelledKeyIsRefused(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"ranch.yaml": minimalCoreLoop + "sleep_maximum: 4h\n",
	})
	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for an unknown key")
	}
	for _, want := range []string{"ranch.yaml", "sleep_maximum"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestInvalidCoreLoopNamesItsFile(t *testing.T) {
	// One bad file among several has to be identifiable, or an operator
	// is left bisecting a directory.
	core := writeCoreLoop(t, map[string]string{
		"good.yaml": minimalCoreLoop,
		"bad.yaml":  "name: broken\nenabled: true\ntask: Do a thing.\noperation: nonsense\n",
	})
	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for an invalid spec")
	}
	if !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("error = %v, want it to name the offending file", err)
	}
}

func TestTwoFilesCannotDefineTheSameLoop(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"a.yaml": minimalCoreLoop,
		"b.yaml": minimalCoreLoop,
	})
	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for a duplicate loop name")
	}
	for _, want := range []string{"a.yaml", "b.yaml", "ranch_watch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// TestDirectHumanEgressTokenExpands is the one exclusion a YAML file
// cannot spell out honestly: which tools reach a human is decided by
// what they do, so a written-out list goes stale the first time one is
// added.
func TestDirectHumanEgressTokenExpands(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"ranch.yaml": minimalCoreLoop + "exclude_tools:\n  - exec\n  - " + excludeToolsDirectHumanEgress + "\n",
	})
	specs, err := loadCoreLoopDefinitions(core)
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	got := specs[0].ExcludeTools
	if len(got) == 0 || got[0] != "exec" {
		t.Fatalf("exclude_tools = %v, want the literal entry kept in place", got)
	}
	egress := tools.DirectHumanEgressToolNames()
	if len(egress) == 0 {
		t.Fatal("no direct human egress tools registered; this test proves nothing")
	}
	for _, name := range egress {
		if !slicesContain(got, name) {
			t.Errorf("exclude_tools = %v, want it to contain %q", got, name)
		}
	}
	if slicesContain(got, excludeToolsDirectHumanEgress) {
		t.Errorf("the token survived into the spec: %v", got)
	}
}

func TestExpandExcludeToolTokensDropsDuplicates(t *testing.T) {
	egress := tools.DirectHumanEgressToolNames()
	if len(egress) == 0 {
		t.Skip("no direct human egress tools registered")
	}
	// The same name arriving both literally and by expansion must not
	// produce two entries.
	got := expandExcludeToolTokens([]string{egress[0], excludeToolsDirectHumanEgress})
	count := 0
	for _, name := range got {
		if name == egress[0] {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%q appears %d times in %v, want once", egress[0], count, got)
	}
}

func slicesContain(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// TestExcludeTokenNeedsNoQuoting is a property of the token's spelling
// rather than of the expansion: YAML reserves @ and ` as indicator
// characters, so a token starting with one is a parse error unless the
// author remembers to quote it. A marker that fails only when written
// the obvious way is worse than no marker.
func TestExcludeTokenNeedsNoQuoting(t *testing.T) {
	if strings.ContainsAny(excludeToolsDirectHumanEgress[:1], "@`*&!|>%?-,[]{}#'\"") {
		t.Fatalf("token %q starts with a reserved YAML indicator; it cannot be written unquoted", excludeToolsDirectHumanEgress)
	}
	// Written bare, exactly as an operator would.
	core := writeCoreLoop(t, map[string]string{
		"ranch.yaml": minimalCoreLoop + "exclude_tools:\n  - " + excludeToolsDirectHumanEgress + "\n",
	})
	if _, err := loadCoreLoopDefinitions(core); err != nil {
		t.Fatalf("an unquoted token did not parse: %v", err)
	}
}

// TestCoreDefinitionWinsOverTheBuiltIn is the authority rule. A loop
// defined in the signed core root is the definition — the built-in Go
// spec of the same name is a default, not a floor — so an operator who
// edits the file and restarts gets what the file says.
func TestCoreDefinitionWinsOverTheBuiltIn(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"ego.yaml": `name: ego
enabled: true
intent: Overridden by the operator.
task: Reflect, but differently.
operation: service
completion: none
sleep_min: 1h
sleep_max: 6h
`,
	})
	app := &App{cfg: &config.Config{
		Paths: map[string]string{"core": core},
		Ego:   testServiceLoopConfig(),
	}}

	specs, err := app.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}

	var found []looppkg.Spec
	for _, spec := range specs {
		if spec.Name == "ego" {
			found = append(found, spec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ego appears %d times, want exactly once — the built-in must not be appended alongside", len(found))
	}
	if found[0].Task != "Reflect, but differently." {
		t.Errorf("task = %q, want the file's — the built-in won", found[0].Task)
	}
	if found[0].SleepMin != time.Hour {
		t.Errorf("sleep_min = %v, want the file's 1h", found[0].SleepMin)
	}
}

// TestBuiltInStillAppliesWithoutAFile is the other half: the override is
// opt-in per loop, so an install with no ego.yaml still gets an ego.
func TestBuiltInStillAppliesWithoutAFile(t *testing.T) {
	app := &App{cfg: &config.Config{
		Paths: map[string]string{"core": t.TempDir()},
		Ego:   testServiceLoopConfig(),
	}}
	specs, err := app.buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("buildLoopDefinitionBaseSpecs: %v", err)
	}
	for _, spec := range specs {
		if spec.Name == "ego" {
			if spec.Task == "" {
				t.Error("built-in ego has no task")
			}
			return
		}
	}
	t.Fatal("ego is absent with no core file and ego enabled")
}

// testServiceLoopConfig is a valid ego/metacog/archivist config block.
//
// It is needed even when the definition comes from a file: the core
// service registrations still parse and cache their config block so
// Hydrate has something to run against. That coupling is why the config
// blocks cannot be dropped until the Go registrations are, and it is the
// next step in this migration rather than a property worth keeping.
func testServiceLoopConfig() config.ServiceLoopConfig {
	return config.ServiceLoopConfig{
		Enabled:      true,
		MinSleep:     "15m",
		MaxSleep:     "12h",
		DefaultSleep: "1h",
	}
}
