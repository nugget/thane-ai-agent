package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// upsertCommitSpec builds a CommitSpec stand-in for tests that only need
// the overlay effect of a commit: it upserts the definition into the
// given registry so downstream lookups see it. It deliberately does NOT
// simulate the persist or reconcile stages of the production chokepoint —
// these tests don't assert on those — so it is not a faithful stand-in,
// just enough to keep the registry populated.
func upsertCommitSpec(reg *looppkg.DefinitionRegistry) func(context.Context, looppkg.Spec, time.Time) error {
	return func(_ context.Context, spec looppkg.Spec, updatedAt time.Time) error {
		if reg == nil {
			return nil
		}
		return reg.Upsert(spec, updatedAt)
	}
}

// mkdirAllForTest is a tiny helper used by the thane_curate tests to
// pre-create the document root directory before invoking the document
// store, which refuses to write into a non-existent root.
func mkdirAllForTest(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func TestParseSleepEnvelope_HappyPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		args             map[string]any
		wantSleepMin     time.Duration
		wantSleepMax     time.Duration
		wantSleepDefault time.Duration
		wantJitter       float64
	}{
		{
			name:             "minimal asymmetric",
			args:             map[string]any{"sleep_min": "5m", "sleep_max": "30m"},
			wantSleepMin:     5 * time.Minute,
			wantSleepMax:     30 * time.Minute,
			wantSleepDefault: 17*time.Minute + 30*time.Second, // midpoint
			wantJitter:       0.1,
		},
		{
			name:             "fixed cadence (min == max)",
			args:             map[string]any{"sleep_min": "30m", "sleep_max": "30m"},
			wantSleepMin:     30 * time.Minute,
			wantSleepMax:     30 * time.Minute,
			wantSleepDefault: 30 * time.Minute,
			wantJitter:       0.1,
		},
		{
			name:             "explicit default and jitter",
			args:             map[string]any{"sleep_min": "5m", "sleep_max": "30m", "sleep_default": "10m", "jitter": 0.0},
			wantSleepMin:     5 * time.Minute,
			wantSleepMax:     30 * time.Minute,
			wantSleepDefault: 10 * time.Minute,
			wantJitter:       0.0,
		},
		{
			name:             "jitter as int",
			args:             map[string]any{"sleep_min": "5m", "sleep_max": "30m", "jitter": 0},
			wantSleepMin:     5 * time.Minute,
			wantSleepMax:     30 * time.Minute,
			wantSleepDefault: 17*time.Minute + 30*time.Second,
			wantJitter:       0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := parseSleepEnvelope(tc.args)
			if err != nil {
				t.Fatalf("parseSleepEnvelope: %v", err)
			}
			if env.sleepMin != tc.wantSleepMin {
				t.Errorf("sleepMin = %v, want %v", env.sleepMin, tc.wantSleepMin)
			}
			if env.sleepMax != tc.wantSleepMax {
				t.Errorf("sleepMax = %v, want %v", env.sleepMax, tc.wantSleepMax)
			}
			if env.sleepDefault != tc.wantSleepDefault {
				t.Errorf("sleepDefault = %v, want %v", env.sleepDefault, tc.wantSleepDefault)
			}
			if env.jitter != tc.wantJitter {
				t.Errorf("jitter = %v, want %v", env.jitter, tc.wantJitter)
			}
		})
	}
}

func TestParseSleepEnvelope_Rejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    map[string]any
		wantMsg string
	}{
		{"missing sleep_min", map[string]any{"sleep_max": "30m"}, "sleep_min is required"},
		{"missing sleep_max", map[string]any{"sleep_min": "5m"}, "sleep_max is required"},
		{"empty sleep_min", map[string]any{"sleep_min": "  ", "sleep_max": "30m"}, "sleep_min is required"},
		{"unparseable sleep_min", map[string]any{"sleep_min": "when the cows come home", "sleep_max": "30m"}, "sleep_min"},
		{"unparseable sleep_max", map[string]any{"sleep_min": "5m", "sleep_max": "garbage"}, "sleep_max"},
		{"below 1m floor", map[string]any{"sleep_min": "30s", "sleep_max": "30m"}, "below the 1 minute floor"},
		{"max less than min", map[string]any{"sleep_min": "30m", "sleep_max": "5m"}, "must be >= sleep_min"},
		{"sleep_default outside envelope", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "sleep_default": "1h"}, "must lie in"},
		{"unparseable sleep_default", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "sleep_default": "bad"}, "sleep_default"},
		// Non-string types for duration args must surface a typed
		// error, not be silently dropped. The JSON schema isn't
		// enforced at handler entry, so a caller sending
		// {sleep_default: 300} would otherwise have the value
		// ignored and the loop launched with the midpoint instead
		// of what they requested.
		{"sleep_default as number", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "sleep_default": 300}, "sleep_default must be a Go duration string"},
		{"sleep_default as object", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "sleep_default": map[string]any{"x": 1}}, "sleep_default must be a Go duration string"},
		{"sleep_min as number", map[string]any{"sleep_min": 300, "sleep_max": "30m"}, "sleep_min must be a Go duration string"},
		{"sleep_max as bool", map[string]any{"sleep_min": "5m", "sleep_max": true}, "sleep_max must be a Go duration string"},
		{"jitter out of range high", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "jitter": 1.5}, "must be in [0, 1]"},
		{"jitter out of range low", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "jitter": -0.1}, "must be in [0, 1]"},
		{"jitter non-numeric", map[string]any{"sleep_min": "5m", "sleep_max": "30m", "jitter": "fast"}, "must be a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSleepEnvelope(tc.args)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestThaneCurate_EndToEnd exercises the full happy path: scaffold
// the output document with frontmatter recording loop ownership,
// register and reconcile a service-loop definition, and launch it.
// The launch path is stubbed via fake registry helpers so the test
// stays in-process.
func TestThaneCurate_EndToEnd(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}

	// Build an in-memory document store with a single kb root.
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)

	// Build an empty loop definition registry and capture launches.
	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	var launchedName string
	launchFn := func(_ context.Context, name string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
		launchedName = name
		return looppkg.LaunchResult{LoopID: "loop-test-1"}, nil
	}

	reg := NewEmptyRegistry()
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools: docTools,
		Registry: defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error {
			return nil
		},
		CommitSpec:       upsertCommitSpec(defRegistry),
		LaunchDefinition: launchFn,
	})

	tool := reg.Get("thane_curate")
	if tool == nil {
		t.Fatal("thane_curate tool not registered after ConfigureLoopIntentTools")
	}

	result, err := tool.Handler(context.Background(), map[string]any{
		"name":      "test_pr_watchlist",
		"intent":    "Track v1.0 PR activity for the upcoming release.",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "maintain",
			"document": "kb:dashboards/pr-watchlist.md",
			"title":    "PR Watchlist",
		},
		"tags": []any{"forge"},
	})
	if err != nil {
		t.Fatalf("thane_curate handler: %v", err)
	}

	// Verify the response shape includes the document, loop, and cadence.
	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok; full = %v", resp["status"], resp)
	}
	if resp["loop_id"] != "loop-test-1" {
		t.Errorf("loop_id = %v, want loop-test-1", resp["loop_id"])
	}
	if resp["loop_definition_name"] != "test_pr_watchlist" {
		t.Errorf("loop_definition_name = %v, want test_pr_watchlist", resp["loop_definition_name"])
	}
	if resp["output_mode"] != "maintain" {
		t.Errorf("output_mode = %v, want maintain", resp["output_mode"])
	}
	if resp["output_tool"] != "replace_output_test_pr_watchlist" {
		t.Errorf("output_tool = %v, want replace_output_test_pr_watchlist", resp["output_tool"])
	}

	// Verify the launch fired against the right name.
	if launchedName != "test_pr_watchlist" {
		t.Errorf("LaunchDefinition name = %q, want test_pr_watchlist", launchedName)
	}

	// Verify the definition is in the registry.
	snap := defRegistry.Snapshot()
	var found *looppkg.DefinitionSnapshot
	for i := range snap.Definitions {
		if snap.Definitions[i].Spec.Name == "test_pr_watchlist" {
			found = &snap.Definitions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("definition not registered in DefinitionRegistry")
	}
	if found.Spec.Operation != looppkg.OperationService {
		t.Errorf("Operation = %q, want service", found.Spec.Operation)
	}
	if found.Spec.Profile.DelegationGating != "disabled" {
		t.Errorf("DelegationGating = %q, want disabled", found.Spec.Profile.DelegationGating)
	}
	// Only caller-supplied tags; the scope-tag mechanism is gone.
	if len(found.Spec.Tags) != 1 || found.Spec.Tags[0] != "forge" {
		t.Errorf("Tags = %v, want [forge]", found.Spec.Tags)
	}
	if _, ok := found.Spec.Metadata["scope_tag"]; ok {
		t.Errorf("scope_tag metadata should be gone, found %v", found.Spec.Metadata["scope_tag"])
	}

	// Verify the declared output rides on the spec so the hydration
	// layer can generate the scoped output tool and inject document
	// context on each iteration.
	if len(found.Spec.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1: %+v", len(found.Spec.Outputs), found.Spec.Outputs)
	}
	out := found.Spec.Outputs[0]
	if out.Name != "test_pr_watchlist" {
		t.Errorf("Outputs[0].Name = %q, want test_pr_watchlist", out.Name)
	}
	if out.Type != looppkg.OutputTypeMaintainedDocument {
		t.Errorf("Outputs[0].Type = %q, want maintained_document", out.Type)
	}
	if out.Mode != looppkg.OutputModeReplace {
		t.Errorf("Outputs[0].Mode = %q, want replace", out.Mode)
	}
	if out.Ref != "kb:dashboards/pr-watchlist.md" {
		t.Errorf("Outputs[0].Ref = %q, want kb:dashboards/pr-watchlist.md", out.Ref)
	}
	if out.Purpose == "" {
		t.Errorf("Outputs[0].Purpose should carry the intent, got empty")
	}
	if got, want := out.ToolName(), "replace_output_test_pr_watchlist"; got != want {
		t.Errorf("Outputs[0].ToolName = %q, want %q", got, want)
	}
	// The task prompt should point the model at the scoped tool rather
	// than the generic doc_write / doc_journal_update pair.
	if !strings.Contains(found.Spec.Task, "replace_output_test_pr_watchlist") {
		t.Errorf("task prompt should reference scoped output tool, got: %s", found.Spec.Task)
	}
	// Maintain mode must warn the model about the 16 KiB head truncation
	// applied by renderLoopOutputContext, or it will rewrite only the
	// visible prefix and silently drop everything past the boundary.
	if !strings.Contains(found.Spec.Task, "truncated") {
		t.Errorf("maintain-mode task prompt must warn about truncation, got: %s", found.Spec.Task)
	}
	if !strings.Contains(found.Spec.Task, "doc_read") {
		t.Errorf("maintain-mode task prompt must instruct doc_read on truncation, got: %s", found.Spec.Task)
	}

	// Verify the scaffold document was written with loop-ownership frontmatter.
	doc, err := docTools.Read(context.Background(), documents.RefArgs{Ref: "kb:dashboards/pr-watchlist.md"})
	if err != nil {
		t.Fatalf("read scaffold doc: %v", err)
	}
	if !strings.Contains(doc, "loop_definition_name") {
		t.Errorf("scaffold missing loop_definition_name frontmatter:\n%s", doc)
	}
	if !strings.Contains(doc, "test_pr_watchlist") {
		t.Errorf("scaffold missing loop name in frontmatter:\n%s", doc)
	}
	if !strings.Contains(doc, "output_mode") {
		t.Errorf("scaffold missing output_mode frontmatter:\n%s", doc)
	}
	rawDoc, err := os.ReadFile(filepath.Join(kbDir, "dashboards", "pr-watchlist.md"))
	if err != nil {
		t.Fatalf("read raw scaffold doc: %v", err)
	}
	if strings.Contains(string(rawDoc), "created_at:") {
		t.Errorf("scaffold should use canonical created frontmatter, got created_at:\n%s", rawDoc)
	}
	if !strings.Contains(string(rawDoc), "created:") {
		t.Errorf("scaffold missing canonical created frontmatter:\n%s", rawDoc)
	}
	if !strings.Contains(doc, "Current State") {
		t.Errorf("maintain scaffold should include Current State heading:\n%s", doc)
	}
}

// TestThaneCurate_RefusesToClobber verifies the safety check that
// an existing loop definition of the same name cannot be overwritten
// without an explicit replace=true.
func TestThaneCurate_RefusesToClobber(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)

	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	// Pre-seed a definition with the same name.
	if err := defRegistry.Upsert(looppkg.Spec{
		Name:      "existing_loop",
		Enabled:   true,
		Task:      "preexisting",
		Operation: looppkg.OperationService,
		SleepMin:  time.Hour,
		SleepMax:  time.Hour,
	}, time.Now()); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	reg := NewEmptyRegistry()
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:    docTools,
		Registry:    defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error { return nil },
		CommitSpec:  upsertCommitSpec(defRegistry),
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
	})

	tool := reg.Get("thane_curate")
	_, err = tool.Handler(context.Background(), map[string]any{
		"name":      "existing_loop",
		"intent":    "Replace the prior loop without permission.",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:journal/existing.md",
		},
	})
	if err == nil {
		t.Fatal("expected thane_curate to refuse to clobber existing definition")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should mention name collision", err)
	}
}

// TestThaneCurate_RefusesToClobberDocument verifies the document
// scaffold preflight: an existing document must not be overwritten
// without an explicit replace=true. This is a separate safety from
// the loop-definition collision check above; either trigger should
// block the call.
func TestThaneCurate_RefusesToClobberDocument(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)

	// Pre-seed a document at the target ref via the same writer the
	// tool uses; simulates a user-authored doc that the loop would
	// otherwise stomp on.
	preBody := "# Existing Notes\n\nDo not overwrite.\n"
	if _, err := docTools.Write(context.Background(), documents.WriteArgs{
		Ref:   "kb:notes/existing.md",
		Title: "Existing Notes",
		Body:  &preBody,
	}); err != nil {
		t.Fatalf("seed existing document: %v", err)
	}

	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	reg := NewEmptyRegistry()
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:    docTools,
		Registry:    defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error { return nil },
		CommitSpec:  upsertCommitSpec(defRegistry),
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{LoopID: "should-not-fire"}, nil
		},
	})

	tool := reg.Get("thane_curate")
	_, err = tool.Handler(context.Background(), map[string]any{
		"name":      "fresh_loop",
		"intent":    "Track something the doc already covers.",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "maintain",
			"document": "kb:notes/existing.md",
		},
	})
	if err == nil {
		t.Fatal("expected thane_curate to refuse to clobber existing document")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should mention document collision", err)
	}

	// The original body must still be on disk untouched.
	doc, readErr := docTools.Read(context.Background(), documents.RefArgs{Ref: "kb:notes/existing.md"})
	if readErr != nil {
		t.Fatalf("read pre-existing doc: %v", readErr)
	}
	if !strings.Contains(doc, "Do not overwrite") {
		t.Errorf("pre-existing document was modified despite refusal:\n%s", doc)
	}
}

// TestThaneCurate_JournalDeclaresAppendOutput verifies that journal-mode
// loops carry a journal_document OutputSpec with append mode, so the
// hydration layer generates an append_output_* scoped tool instead of
// the replace_output_* tool used by maintain-mode loops.
func TestThaneCurate_JournalDeclaresAppendOutput(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)

	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	reg := NewEmptyRegistry()
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:    docTools,
		Registry:    defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error { return nil },
		CommitSpec:  upsertCommitSpec(defRegistry),
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{LoopID: "loop-journal-1"}, nil
		},
	})

	tool := reg.Get("thane_curate")
	if _, err := tool.Handler(context.Background(), map[string]any{
		"name":      "release_journal",
		"intent":    "Capture forge releases as a dated log.",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:journal/releases.md",
		},
	}); err != nil {
		t.Fatalf("thane_curate handler: %v", err)
	}

	snap := defRegistry.Snapshot()
	var found *looppkg.DefinitionSnapshot
	for i := range snap.Definitions {
		if snap.Definitions[i].Spec.Name == "release_journal" {
			found = &snap.Definitions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("definition not registered")
	}
	if len(found.Spec.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(found.Spec.Outputs))
	}
	out := found.Spec.Outputs[0]
	if out.Type != looppkg.OutputTypeJournalDocument {
		t.Errorf("Type = %q, want journal_document", out.Type)
	}
	if out.Mode != looppkg.OutputModeAppend {
		t.Errorf("Mode = %q, want append", out.Mode)
	}
	if got, want := out.ToolName(), "append_output_release_journal"; got != want {
		t.Errorf("ToolName = %q, want %q", got, want)
	}
	if !strings.Contains(found.Spec.Task, "append_output_release_journal") {
		t.Errorf("task prompt should reference scoped output tool, got: %s", found.Spec.Task)
	}
	// Journal mode is append-only: the recent tail in the context block
	// is enough, so the prompt should explicitly say no separate read is
	// needed before appending.
	if !strings.Contains(found.Spec.Task, "no separate read") {
		t.Errorf("journal-mode task prompt should reassure no read needed, got: %s", found.Spec.Task)
	}
	// Conversely, journal mode must NOT carry the maintain-mode truncation
	// warning — appending doesn't need the full history.
	if strings.Contains(found.Spec.Task, "truncated") {
		t.Errorf("journal-mode task prompt should not carry maintain-mode truncation warning, got: %s", found.Spec.Task)
	}
}

// TestThaneCurate_InstructionsFlowToProfile verifies that the
// `instructions` tool arg lands on Spec.Profile.Instructions (the
// canonical iteration-prepend surface) and NOT on the Spec.Task via
// the older "Guidance: ..." fold. Two failure modes to guard against:
// (1) the arg silently dropped, (2) the arg still being concatenated
// into Task — both would mean a caller's steering text shows up in
// the wrong place or twice if anything restores the old code path.
func TestThaneCurate_InstructionsFlowToProfile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)

	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	reg := NewEmptyRegistry()
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:    docTools,
		Registry:    defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error { return nil },
		CommitSpec:  upsertCommitSpec(defRegistry),
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{LoopID: "loop-inst-1"}, nil
		},
	})

	const steering = "Focus on UPS load trends; ignore brief transients under 5 seconds."
	tool := reg.Get("thane_curate")
	if tool == nil {
		t.Fatal("thane_curate tool not registered after ConfigureLoopIntentTools")
	}
	if _, err := tool.Handler(context.Background(), map[string]any{
		"name":         "instructions_test",
		"intent":       "Watch the rack.",
		"sleep_min":    "5m",
		"sleep_max":    "30m",
		"instructions": "  " + steering + "  ", // whitespace trimmed
		"output": map[string]any{
			"mode":     "maintain",
			"document": "kb:dashboards/rack.md",
		},
	}); err != nil {
		t.Fatalf("thane_curate handler: %v", err)
	}

	snap := defRegistry.Snapshot()
	var found *looppkg.DefinitionSnapshot
	for i := range snap.Definitions {
		if snap.Definitions[i].Spec.Name == "instructions_test" {
			found = &snap.Definitions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("definition not registered")
	}
	if found.Spec.Profile.Instructions != steering {
		t.Errorf("Profile.Instructions = %q, want %q (whitespace trimmed)", found.Spec.Profile.Instructions, steering)
	}
	if strings.Contains(found.Spec.Task, "Guidance:") {
		t.Errorf("Spec.Task should not carry legacy \"Guidance:\" fold, got: %s", found.Spec.Task)
	}
	if strings.Contains(found.Spec.Task, steering) {
		t.Errorf("Spec.Task should not carry the steering text directly; it belongs on Profile.Instructions. Task: %s", found.Spec.Task)
	}
}

// TestThaneCurate_InstructionsOmitted verifies that omitting
// `instructions` results in an empty Profile.Instructions (not nil-vs-
// empty-string weirdness, not a default value), and the Task text
// stays minimal.
func TestThaneCurate_InstructionsOmitted(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)

	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	reg := NewEmptyRegistry()
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:    docTools,
		Registry:    defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error { return nil },
		CommitSpec:  upsertCommitSpec(defRegistry),
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{LoopID: "loop-inst-2"}, nil
		},
	})

	tool := reg.Get("thane_curate")
	if _, err := tool.Handler(context.Background(), map[string]any{
		"name":      "no_instructions",
		"intent":    "Watch.",
		"sleep_min": "5m",
		"sleep_max": "30m",
		"output": map[string]any{
			"mode":     "maintain",
			"document": "kb:dashboards/no-inst.md",
		},
	}); err != nil {
		t.Fatalf("thane_curate handler: %v", err)
	}

	snap := defRegistry.Snapshot()
	var found *looppkg.DefinitionSnapshot
	for i := range snap.Definitions {
		if snap.Definitions[i].Spec.Name == "no_instructions" {
			found = &snap.Definitions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("definition not registered")
	}
	if found.Spec.Profile.Instructions != "" {
		t.Errorf("Profile.Instructions = %q, want empty when omitted", found.Spec.Profile.Instructions)
	}
}

// newCurateTestRig builds the minimum machinery needed for
// thane_curate-handler tests: in-memory document store, empty
// definition registry, fake subscription store, stub launch + reconcile,
// and a registered-tag recorder. Returned values let each test inspect
// post-call state without re-running the setup boilerplate.
type curateTestRig struct {
	reg         *Registry
	tool        *Tool
	defRegistry *looppkg.DefinitionRegistry
	docTools    *documents.Tools
}

func newCurateTestRig(t *testing.T) *curateTestRig {
	t.Helper()
	tempDir := t.TempDir()
	kbDir := filepath.Join(tempDir, "kb")
	if err := mkdirAllForTest(kbDir); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	docStore, err := documents.NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("documents.NewStore: %v", err)
	}
	docTools := documents.NewTools(docStore)
	defRegistry, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	rig := &curateTestRig{
		defRegistry: defRegistry,
		docTools:    docTools,
	}
	rig.reg = NewEmptyRegistry()
	rig.reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:    docTools,
		Registry:    defRegistry,
		PersistSpec: func(_ looppkg.Spec, _ time.Time) error { return nil },
		CommitSpec:  upsertCommitSpec(defRegistry),
		LaunchDefinition: func(_ context.Context, name string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{LoopID: "loop-test-" + name}, nil
		},
	})
	rig.tool = rig.reg.Get("thane_curate")
	if rig.tool == nil {
		t.Fatal("thane_curate not registered")
	}
	return rig
}

// findCurateSpec is a small helper for asserting on a definition's spec.
func (rig *curateTestRig) findCurateSpec(t *testing.T, name string) looppkg.Spec {
	t.Helper()
	snap := rig.defRegistry.Snapshot()
	for i := range snap.Definitions {
		if snap.Definitions[i].Spec.Name == name {
			return snap.Definitions[i].Spec
		}
	}
	t.Fatalf("definition %q not in registry", name)
	return looppkg.Spec{}
}

// TestThaneCurate_PersistsSubscriptions covers the create-time path:
// entities land on Spec.Subscriptions in the new structural shape, no
// scope_tag is minted, and the response reports the count.
func TestThaneCurate_PersistsSubscriptions(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	result, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "thermostat_journal",
		"intent":    "Daily HVAC summary.",
		"sleep_min": "21h",
		"sleep_max": "27h",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:home/hvac.md",
		},
		"entities": []any{
			map[string]any{
				"entity_id": "climate.upstairs",
				"history":   []any{3600, 86400},
			},
			map[string]any{
				"entity_id":   "weather.home",
				"forecast":    "hourly",
				"ttl_seconds": 86400,
			},
		},
	})
	if err != nil {
		t.Fatalf("thane_curate: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := resp["scope_tag"]; present {
		t.Errorf("response carries dead scope_tag field: %v", resp["scope_tag"])
	}
	if got := resp["entity_subscriptions"]; got != float64(2) {
		t.Errorf("entity_subscriptions = %v, want 2", got)
	}

	spec := rig.findCurateSpec(t, "thermostat_journal")
	if _, ok := spec.Metadata["scope_tag"]; ok {
		t.Errorf("scope_tag metadata should be gone, found %q", spec.Metadata["scope_tag"])
	}
	if len(spec.Subscriptions) != 2 {
		t.Fatalf("Subscriptions len = %d, want 2: %+v", len(spec.Subscriptions), spec.Subscriptions)
	}
	if spec.Subscriptions[0].EntityID != "climate.upstairs" {
		t.Errorf("Subscriptions[0].EntityID = %q, want climate.upstairs", spec.Subscriptions[0].EntityID)
	}
	if h := spec.Subscriptions[0].History; len(h) != 2 || h[0] != 3600 || h[1] != 86400 {
		t.Errorf("Subscriptions[0].History = %v, want [3600 86400]", h)
	}
	if spec.Subscriptions[1].Forecast != "hourly" {
		t.Errorf("Subscriptions[1].Forecast = %q, want hourly", spec.Subscriptions[1].Forecast)
	}
	if spec.Subscriptions[1].TTLSeconds != 86400 {
		t.Errorf("Subscriptions[1].TTLSeconds = %d, want 86400", spec.Subscriptions[1].TTLSeconds)
	}
}

// TestThaneCurate_ReplaceOverwritesSubscriptions verifies that
// replace=true on a re-creation swaps the subscription list entirely
// to whatever the new entities argument supplies — the prior set is
// not merged or otherwise leaked through.
func TestThaneCurate_ReplaceOverwritesSubscriptions(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	create := func(extra map[string]any) {
		args := map[string]any{
			"name":      "hvac_curate",
			"intent":    "HVAC summary.",
			"sleep_min": "21h",
			"sleep_max": "27h",
			"output": map[string]any{
				"mode":     "journal",
				"document": "kb:home/hvac.md",
			},
		}
		for k, v := range extra {
			args[k] = v
		}
		if _, err := rig.tool.Handler(context.Background(), args); err != nil {
			t.Fatalf("thane_curate: %v", err)
		}
	}

	create(map[string]any{
		"entities": []any{
			map[string]any{"entity_id": "climate.upstairs"},
		},
	})
	first := rig.findCurateSpec(t, "hvac_curate")
	if len(first.Subscriptions) != 1 || first.Subscriptions[0].EntityID != "climate.upstairs" {
		t.Fatalf("first create Subscriptions = %+v", first.Subscriptions)
	}

	create(map[string]any{
		"replace": true,
		"entities": []any{
			map[string]any{"entity_id": "sensor.upstairs_temp"},
			map[string]any{"entity_id": "weather.home", "forecast": "daily"},
		},
	})
	second := rig.findCurateSpec(t, "hvac_curate")
	if len(second.Subscriptions) != 2 {
		t.Fatalf("after replace Subscriptions len = %d, want 2: %+v", len(second.Subscriptions), second.Subscriptions)
	}
	if second.Subscriptions[0].EntityID != "sensor.upstairs_temp" || second.Subscriptions[1].EntityID != "weather.home" {
		t.Errorf("replace-side Subscriptions = %+v", second.Subscriptions)
	}
	if second.Subscriptions[1].Forecast != "daily" {
		t.Errorf("replace-side Subscriptions[1].Forecast = %q, want daily", second.Subscriptions[1].Forecast)
	}
}

// TestThaneCurate_RejectsDuplicateEntityID guards parseCurateEntities's
// duplicate detection — a model that lists the same entity twice
// should see an actionable error rather than have the second write
// silently no-op via the store's ON CONFLICT clause.
func TestThaneCurate_RejectsDuplicateEntityID(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	_, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "dup_test",
		"intent":    "x",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:dup.md",
		},
		"entities": []any{
			map[string]any{"entity_id": "sensor.foo"},
			map[string]any{"entity_id": "sensor.foo"},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate entity_id to be rejected")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should mention duplicate", err)
	}
}

// TestThaneCurate_RejectsFractionalInteger guards coerceInt's
// integer-not-float guard. JSON decoders deliver every number as
// float64, so a model that types `"ttl_seconds": 3600.5` would be
// silently truncated to 3600 without this check. Whole-number
// floats (3600.0) must still be accepted since that's the realistic
// post-decode shape of an integer literal.
func TestThaneCurate_RejectsFractionalInteger(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)

	_, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "frac_test",
		"intent":    "x",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:frac.md",
		},
		"entities": []any{
			map[string]any{
				"entity_id":   "sensor.foo",
				"ttl_seconds": 3600.5,
			},
		},
	})
	if err == nil {
		t.Fatal("expected fractional ttl_seconds to be rejected")
	}
	if !strings.Contains(err.Error(), "fractional") {
		t.Errorf("error %q should mention fractional", err)
	}

	// Whole-number float (post-JSON-decode shape of an integer literal)
	// must still pass — coerceInt accepts float64 when n == int64(n).
	_, err = rig.tool.Handler(context.Background(), map[string]any{
		"name":      "whole_float_test",
		"intent":    "x",
		"sleep_min": "54m",
		"sleep_max": "66m",
		"output": map[string]any{
			"mode":     "journal",
			"document": "kb:whole.md",
		},
		"entities": []any{
			map[string]any{
				"entity_id":   "sensor.foo",
				"ttl_seconds": 3600.0,
				"history":     []any{3600.0, 86400.0},
			},
		},
	})
	if err != nil {
		t.Fatalf("whole-number floats should be accepted: %v", err)
	}
}
