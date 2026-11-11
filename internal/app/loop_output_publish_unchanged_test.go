package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// TestPublishReportsAPublishThatChangedNothing pins the signal a loop that
// republished its own last digest never got. Through a signed git document
// root, a publish byte-identical to the stored document still succeeds, but
// its result says "unchanged": true with a warning naming when the
// document last changed, the store logs it, and no commit lands. A first
// publish and a changed one say neither and each land one commit, and the
// tool's description says what an unchanged result means.
func TestPublishReportsAPublishThatChangedNothing(t *testing.T) {
	kbDir := filepath.Join(t.TempDir(), "kb")
	signingKey, _ := writeTestSigningKey(t)
	resolver := paths.New(map[string]string{"kb": kbDir})
	app := &App{
		logger: slog.Default(),
		cfg: &config.Config{DocRoots: map[string]config.DocumentRootConfig{
			"kb": {Authoring: "managed", Git: config.DocumentRootGitConfig{Enabled: true, SignCommits: true, VerifySignatures: "required", SigningKey: signingKey}},
		}},
	}
	opts, err := app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err != nil {
		t.Fatalf("buildDocumentStoreOptions: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	var logs bytes.Buffer
	store, err := documents.NewStoreWithOptions(db, buildDocumentRoots(resolver), slog.New(slog.NewJSONHandler(&logs, nil)), opts)
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	app.documentStore, app.documentTools = store, documents.NewTools(store, nil)
	spec := facetedSpec()
	spec.Outputs = []looppkg.OutputSpec{{
		Name:   "office status",
		Type:   looppkg.OutputTypeMaintainedDocument,
		Ref:    "kb:office.md",
		Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}, {Name: looppkg.OutputFacetTeaser}},
	}}
	hydrated, err := app.hydrateLoopOutputs(spec)
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")
	if !strings.Contains(publish.Description, "would save exactly the stored document succeeds but creates no revision, and its result says unchanged: true") {
		t.Errorf("publish description = %q, want it to say what an unchanged publish returns", publish.Description)
	}

	commits := func() string {
		out, err := exec.CommandContext(t.Context(), "git", "-C", kbDir, "rev-list", "--count", "HEAD").Output()
		if err != nil {
			t.Fatalf("count commits: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	first := map[string]any{"status_line": "Printer online; two packages wait.", "teaser": "Open for the desk queue.", "full": "# Office\n\nTwo packages."}
	changed := map[string]any{"status_line": "Printer online; desk queue clear.", "teaser": "Open for the desk queue.", "full": "# Office\n\nNo packages."}
	for _, step := range []struct {
		name          string
		args          map[string]any
		wantUnchanged bool
	}{
		{name: "first publish", args: first},
		{name: "the same publish again", args: first, wantUnchanged: true},
		{name: "and again", args: first, wantUnchanged: true},
		{name: "a changed publish", args: changed},
	} {
		before := commits()
		result, err := publish.Handler(context.Background(), step.args)
		if err != nil {
			t.Fatalf("%s: publish error = %v", step.name, err)
		}
		var decoded struct {
			Published struct {
				Applied   bool     `json:"applied"`
				Unchanged bool     `json:"unchanged"`
				Warnings  []string `json:"warnings"`
			} `json:"published"`
		}
		if err := json.Unmarshal([]byte(result), &decoded); err != nil {
			t.Fatalf("%s: result is not JSON: %v\n%s", step.name, err, result)
		}
		got := decoded.Published
		warned := strings.Contains(strings.Join(got.Warnings, "\n"), "unchanged: the document this write would save is byte for byte the one already stored, so no new revision was created and it still reads as last changed ")
		if !got.Applied || got.Unchanged != step.wantUnchanged || warned != step.wantUnchanged {
			t.Errorf("%s: result = %s, want applied with unchanged and its warning = %v", step.name, result, step.wantUnchanged)
		}
		if landed := commits() != before; landed == step.wantUnchanged {
			t.Errorf("%s: a commit landed = %v, want %v", step.name, landed, !step.wantUnchanged)
		}
	}
	if got := strings.Count(logs.String(), `"msg":"document write changed nothing"`); got != 2 {
		t.Errorf("store logged %d unchanged writes, want 2:\n%s", got, logs.String())
	}
}
