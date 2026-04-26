package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func TestHydrateLoopOutputsBuildsScopedToolsAndContext(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	spec := looppkg.Spec{
		Name:       "metacognitive",
		Enabled:    true,
		Task:       "Maintain state.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{
			{
				Name:    "metacognitive_state",
				Type:    looppkg.OutputTypeMaintainedDocument,
				Ref:     "core:metacognitive.md",
				Purpose: "Current metacognitive state.",
			},
			{
				Name:          "metacognitive_journal",
				Type:          looppkg.OutputTypeJournalDocument,
				Ref:           "core:metacognitive-journal.md",
				JournalWindow: "day",
				MaxWindows:    2,
			},
		},
	}

	hydrated, err := app.hydrateLoopOutputs(spec)
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	if len(hydrated.RuntimeTools) != 2 {
		t.Fatalf("RuntimeTools len = %d, want 2", len(hydrated.RuntimeTools))
	}
	if hydrated.RuntimeTools[0].Name != "replace_output_metacognitive_state" {
		t.Fatalf("RuntimeTools[0].Name = %q", hydrated.RuntimeTools[0].Name)
	}
	if hydrated.RuntimeTools[1].Name != "append_output_metacognitive_journal" {
		t.Fatalf("RuntimeTools[1].Name = %q", hydrated.RuntimeTools[1].Name)
	}

	_, err = hydrated.RuntimeTools[0].Handler(context.Background(), map[string]any{
		"content": "## Current Sense\n\nEverything is calm.",
	})
	if err != nil {
		t.Fatalf("replace output handler: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(coreDir, "metacognitive.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), "Everything is calm.") {
		t.Fatalf("metacognitive.md = %s, want replacement content", string(raw))
	}

	_, err = hydrated.RuntimeTools[1].Handler(context.Background(), map[string]any{
		"entry": "Observed quiet conditions.",
	})
	if err != nil {
		t.Fatalf("append output handler: %v", err)
	}
	journal, err := os.ReadFile(filepath.Join(coreDir, "metacognitive-journal.md"))
	if err != nil {
		t.Fatalf("ReadFile journal: %v", err)
	}
	if !strings.Contains(string(journal), "Observed quiet conditions.") {
		t.Fatalf("journal = %s, want appended entry", string(journal))
	}

	ctx, err := hydrated.OutputContextBuilder(context.Background(), hydrated.Outputs)
	if err != nil {
		t.Fatalf("OutputContextBuilder: %v", err)
	}
	for _, want := range []string{
		"Declared Durable Outputs",
		"replace_output_metacognitive_state",
		"append_output_metacognitive_journal",
		"Everything is calm.",
		"Observed quiet conditions.",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("output context missing %q:\n%s", want, ctx)
		}
	}
}

func TestHydrateLoopOutputsRequiresDocumentStore(t *testing.T) {
	t.Parallel()

	_, err := (&App{}).hydrateLoopOutputs(looppkg.Spec{
		Name: "writer",
		Task: "Maintain output.",
		Outputs: []looppkg.OutputSpec{
			{Name: "status", Type: looppkg.OutputTypeMaintainedDocument, Ref: "core:status.md"},
		},
	})
	if err == nil {
		t.Fatal("hydrateLoopOutputs error = nil, want missing document roots error")
	}
	if !strings.Contains(err.Error(), "managed document roots are not configured") {
		t.Fatalf("error = %v", err)
	}
}

func newLoopOutputDocumentStore(t *testing.T) (*documents.Store, string) {
	t.Helper()

	rootDir := t.TempDir()
	coreDir := filepath.Join(rootDir, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := documents.NewStore(db, map[string]string{"core": coreDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, coreDir
}
