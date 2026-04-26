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

// mkdirAllForTest is a tiny helper used by the thane_curate tests to
// pre-create the document root directory before invoking the document
// store, which refuses to write into a non-existent root.
func mkdirAllForTest(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func TestParseCadence_AcceptedForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input               string
		wantSleepDefault    time.Duration
		wantSleepMinAtMost  time.Duration
		wantSleepMaxAtLeast time.Duration
	}{
		{"hourly", time.Hour, 55 * time.Minute, 65 * time.Minute},
		{"daily", 24 * time.Hour, 23*time.Hour - time.Second, 25 * time.Hour},
		{"every 30 minutes", 30 * time.Minute, 28 * time.Minute, 32 * time.Minute},
		{"30m", 30 * time.Minute, 28 * time.Minute, 32 * time.Minute},
		{"1h", time.Hour, 55 * time.Minute, 65 * time.Minute},
		{"every 2 hours", 2 * time.Hour, time.Hour + 50*time.Minute, 2*time.Hour + 10*time.Minute},
		{"1 day", 24 * time.Hour, 23*time.Hour - time.Second, 25 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			c, err := parseCadence(tc.input)
			if err != nil {
				t.Fatalf("parseCadence(%q): %v", tc.input, err)
			}
			if c.sleepDefault != tc.wantSleepDefault {
				t.Errorf("sleepDefault = %v, want %v", c.sleepDefault, tc.wantSleepDefault)
			}
			if c.sleepMin > tc.wantSleepMinAtMost {
				t.Errorf("sleepMin = %v, want ≤ %v", c.sleepMin, tc.wantSleepMinAtMost)
			}
			if c.sleepMax < tc.wantSleepMaxAtLeast {
				t.Errorf("sleepMax = %v, want ≥ %v", c.sleepMax, tc.wantSleepMaxAtLeast)
			}
		})
	}
}

func TestParseCadence_RejectsTooFast(t *testing.T) {
	t.Parallel()
	if _, err := parseCadence("30s"); err == nil {
		t.Fatal("expected error for sub-minute cadence")
	}
}

func TestParseCadence_RejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := parseCadence("when the cows come home"); err == nil {
		t.Fatal("expected error for unparseable cadence")
	}
}

func TestParseCadence_RejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := parseCadence("   "); err == nil {
		t.Fatal("expected error for empty cadence")
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
		Reconcile: func(_ context.Context, _ string) error {
			return nil
		},
		LaunchDefinition: launchFn,
	})

	tool := reg.Get("thane_curate")
	if tool == nil {
		t.Fatal("thane_curate tool not registered after ConfigureLoopIntentTools")
	}

	result, err := tool.Handler(context.Background(), map[string]any{
		"name":    "test_pr_watchlist",
		"intent":  "Track v1.0 PR activity for the upcoming release.",
		"cadence": "hourly",
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
	if len(found.Spec.Tags) != 1 || found.Spec.Tags[0] != "forge" {
		t.Errorf("Tags = %v, want [forge]", found.Spec.Tags)
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
		Reconcile:   func(_ context.Context, _ string) error { return nil },
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
	})

	tool := reg.Get("thane_curate")
	_, err = tool.Handler(context.Background(), map[string]any{
		"name":    "existing_loop",
		"intent":  "Replace the prior loop without permission.",
		"cadence": "hourly",
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
