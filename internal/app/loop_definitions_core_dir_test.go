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

// minimalCoreLoop is a definition document in the shape an operator or
// agent authors: ordinary frontmatter, the spec in a fenced block under
// its heading, the prompt as prose.
const minimalCoreLoop = "---\n" +
	"title: Ranch Watch\n" +
	"tags: [loops]\n" +
	"---\n\n" +
	"# Ranch Watch\n\n" +
	"## Spec\n\n" +
	"```yaml\n" +
	"name: ranch_watch\n" +
	"enabled: true\n" +
	"intent: Keep ranch conditions legible.\n" +
	"operation: service\n" +
	"completion: none\n" +
	"sleep_min: 15m\n" +
	"sleep_max: 12h\n" +
	"```\n\n" +
	"## Task\n\n" +
	"Watch the ranch.\n"

func TestCoreLoopDefinitionsLoadFromYAML(t *testing.T) {
	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": minimalCoreLoop}))
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
		"ranch.md": strings.Replace(minimalCoreLoop, "sleep_max: 12h", "sleep_max: 12h\nsleep_maximum: 4h", 1),
	})
	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for an unknown key")
	}
	for _, want := range []string{"ranch.md", "sleep_maximum"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestInvalidCoreLoopNamesItsFile(t *testing.T) {
	// One bad file among several has to be identifiable, or an operator
	// is left bisecting a directory.
	core := writeCoreLoop(t, map[string]string{
		"good.md": minimalCoreLoop,
		"bad.md":  strings.Replace(minimalCoreLoop, "operation: service", "operation: nonsense", 1),
	})
	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for an invalid spec")
	}
	if !strings.Contains(err.Error(), "bad.md") {
		t.Errorf("error = %v, want it to name the offending file", err)
	}
}

func TestTwoFilesCannotDefineTheSameLoop(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"a.md": minimalCoreLoop,
		"b.md": minimalCoreLoop,
	})
	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for a duplicate loop name")
	}
	for _, want := range []string{"a.md", "b.md", "ranch_watch"} {
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
		"ranch.md": strings.Replace(minimalCoreLoop, "sleep_max: 12h", "sleep_max: 12h\nexclude_tools:\n  - exec\n  - "+excludeToolsDirectHumanEgress, 1),
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
		"ranch.md": strings.Replace(minimalCoreLoop, "sleep_max: 12h", "sleep_max: 12h\nexclude_tools:\n  - "+excludeToolsDirectHumanEgress, 1),
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
		"ego.md": strings.NewReplacer(
			"name: ranch_watch", "name: ego",
			"intent: Keep ranch conditions legible.", "intent: Overridden by the operator.",
			"sleep_min: 15m", "sleep_min: 1h",
			"Watch the ranch.", "Reflect, but differently.",
		).Replace(minimalCoreLoop),
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
// opt-in per loop, so an install with no ego.md still gets an ego.
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

// TestProseSectionsBecomeThePrompts is the point of the format: the
// prompt is markdown in a markdown document, not a string escaped into a
// data structure.
func TestProseSectionsBecomeThePrompts(t *testing.T) {
	doc := strings.Replace(minimalCoreLoop,
		"## Task\n\nWatch the ranch.\n",
		"## Task\n\nWatch the ranch.\n\n### What to look at\n\nThe things that matter.\n\n## Supervisor Review\n\nReview it harder.\n", 1)
	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	spec := specs[0]
	// A deeper heading inside a prompt is that prompt's own structure.
	if !strings.Contains(spec.Task, "### What to look at") {
		t.Errorf("task lost its subsection: %q", spec.Task)
	}
	if strings.Contains(spec.Task, "Review it harder") {
		t.Errorf("task swallowed the next section: %q", spec.Task)
	}
	if spec.SupervisorProfile.Instructions != "Review it harder." {
		t.Errorf("supervisor instructions = %q", spec.SupervisorProfile.Instructions)
	}
}

// TestYAMLExampleInAProseSectionIsNotTheSpec is why the fence needs a
// heading. Prompts teach structure, so one may legitimately contain a
// yaml block; only the one under "## Spec" is the definition.
func TestYAMLExampleInAProseSectionIsNotTheSpec(t *testing.T) {
	doc := strings.Replace(minimalCoreLoop,
		"Watch the ranch.\n",
		"Watch the ranch. Publish like this:\n\n```yaml\nname: not_the_spec\noperation: nonsense\n```\n", 1)
	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err != nil {
		t.Fatalf("an example block in prose broke the load: %v", err)
	}
	if specs[0].Name != "ranch_watch" {
		t.Fatalf("name = %q, want the spec section's — an example was read as the definition", specs[0].Name)
	}
	if !strings.Contains(specs[0].Task, "name: not_the_spec") {
		t.Errorf("the example was stripped out of the prompt: %q", specs[0].Task)
	}
}

func TestMissingOrUnfencedSpecSectionIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			name:    "no spec section",
			doc:     "---\ntitle: X\n---\n\n## Task\n\nDo a thing.\n",
			wantErr: "no \"## Spec\" section",
		},
		{
			name:    "spec section is prose",
			doc:     strings.Replace(minimalCoreLoop, "```yaml\nname: ranch_watch", "name: ranch_watch", 1),
			wantErr: "not a single ```yaml block",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": tt.doc}))
			if err == nil {
				t.Fatalf("loadCoreLoopDefinitions error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestTaskDeclaredTwiceIsRefused: a silent precedence rule means an
// author edits the prompt they can read and the loop keeps running the
// one they cannot.
func TestTaskDeclaredTwiceIsRefused(t *testing.T) {
	doc := strings.Replace(minimalCoreLoop, "sleep_max: 12h", "sleep_max: 12h\ntask: The hidden one.", 1)
	_, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for a task declared in both places")
	}
	if !strings.Contains(err.Error(), "declare it once") {
		t.Errorf("error = %v, want it to say to declare it once", err)
	}
}

// TestSpecInFrontmatterIsIgnored guards the choice itself: frontmatter
// is ordinary document metadata here, so a spec key there must not be
// read as part of the definition.
func TestSpecInFrontmatterIsIgnored(t *testing.T) {
	doc := strings.Replace(minimalCoreLoop, "tags: [loops]", "tags: [loops]\nsleep_min: 99h", 1)
	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	if specs[0].SleepMin != 15*time.Minute {
		t.Errorf("sleep_min = %v, want the spec block's 15m — frontmatter is document metadata, not spec", specs[0].SleepMin)
	}
}

// TestRealPromptHeadingsSurvive is the case that would have broken the
// port. The three prompts being moved carry seventeen H2 headings
// between them, so a splitter that treated every "## " as a boundary
// would shred each one into fragments at load.
func TestRealPromptHeadingsSurvive(t *testing.T) {
	prompt := strings.Join([]string{
		"Ego loop iteration.",
		"",
		"## Your Durable Output",
		"",
		"Your contract is injected above.",
		"",
		"## What To Do This Iteration",
		"",
		"1. Read your current ego.md",
		"2. Reflect honestly",
		"",
		"## Guidelines",
		"",
		"Quality of thought matters more than coverage.",
	}, "\n")
	doc := strings.Replace(minimalCoreLoop, "Watch the ranch.\n", prompt+"\n", 1)

	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	if specs[0].Task != prompt {
		t.Fatalf("the prompt was shredded at its own headings:\n got %q\nwant %q", specs[0].Task, prompt)
	}
}

// TestReservedHeadingInsideAFenceIsQuoted covers a prompt that teaches
// this very format — a plausible thing for these prompts to do.
func TestReservedHeadingInsideAFenceIsQuoted(t *testing.T) {
	prompt := "Write a definition like this:\n\n```markdown\n## Spec\n\nname: example\n\n## Task\n\nDo the thing.\n```\n\nThen restart."
	doc := strings.Replace(minimalCoreLoop, "Watch the ranch.\n", prompt+"\n", 1)

	specs, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err != nil {
		t.Fatalf("loadCoreLoopDefinitions: %v", err)
	}
	if specs[0].Name != "ranch_watch" {
		t.Errorf("name = %q — a quoted heading was read as a section", specs[0].Name)
	}
	if !strings.Contains(specs[0].Task, "## Task") || !strings.Contains(specs[0].Task, "Then restart.") {
		t.Errorf("the quoted example did not survive intact: %q", specs[0].Task)
	}
}

func TestSymlinkedDefinitionIsRefused(t *testing.T) {
	// A symlink reads content from outside the signed root while looking
	// like part of it, which is the one thing a core definition rules out.
	core := writeCoreLoop(t, map[string]string{"ranch.md": minimalCoreLoop})
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(outside, []byte(minimalCoreLoop), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(core, coreLoopsDirName, "sneaky.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := loadCoreLoopDefinitions(core)
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for a symlinked definition")
	}
	if !strings.Contains(err.Error(), "sneaky.md") || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("error = %v, want it to name the entry and why it was refused", err)
	}
}

func TestDuplicateNamesAreComparedTrimmed(t *testing.T) {
	// "ego" and "ego " are two entries here and one loop downstream.
	padded := strings.Replace(minimalCoreLoop, "name: ranch_watch", `name: "ranch_watch "`, 1)
	_, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{
		"a.md": minimalCoreLoop,
		"b.md": padded,
	}))
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for names differing only in whitespace")
	}
	if !strings.Contains(err.Error(), "both define the loop") {
		t.Errorf("error = %v, want the duplicate-loop error", err)
	}
}

func TestSecondYAMLDocumentInTheSpecBlockIsRefused(t *testing.T) {
	// A stray "---" starts a second document; everything after it would
	// be dropped without a word.
	doc := strings.Replace(minimalCoreLoop, "sleep_max: 12h", "sleep_max: 12h\n---\nname: ignored", 1)
	_, err := loadCoreLoopDefinitions(writeCoreLoop(t, map[string]string{"ranch.md": doc}))
	if err == nil {
		t.Fatal("loadCoreLoopDefinitions error = nil for a two-document spec block")
	}
	if !strings.Contains(err.Error(), "more than one yaml document") {
		t.Errorf("error = %v, want it to explain the stray separator", err)
	}
}
