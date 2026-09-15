package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestSearchDefaultOrchestratorSourcePermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("agent:\n  delegation_required: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.DelegationRequired || len(cfg.Agent.OrchestratorTools) == 0 {
		t.Fatal("test requires the configured default orchestrator gate")
	}

	for _, tc := range []struct {
		name              string
		tags              []string
		exclude           []string
		orchestratorTools []string
		wantDocuments     bool
		wantArchives      bool
	}{
		{name: "documents only", tags: []string{"documents"}, wantDocuments: true},
		{name: "combined sources", tags: []string{"archive", "documents"}, wantDocuments: true, wantArchives: true},
		{name: "explicit source exclusion", tags: []string{"archive", "documents"}, exclude: []string{"doc_search"}, wantArchives: true},
		{name: "custom orchestrator allowlist", tags: []string{"archive", "documents"}, orchestratorTools: []string{"thane_now", "search", "archive_search"}, wantArchives: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			store, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "thane.db"), 100)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			archive, err := memory.NewArchiveStoreFromDB(store.DB(), nil, logger)
			if err != nil {
				t.Fatal(err)
			}
			working, err := memory.NewWorkingMemoryStore(store.DB(), true)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AddMessage("earlier-conversation", "user", "MQTT was selected for offline operation.", memory.OriginChannel); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "decision.md"), []byte("---\ntitle: Network decision\n---\n\nMQTT remains the maintained network choice."), 0o600); err != nil {
				t.Fatal(err)
			}
			documentStore, err := documents.NewStoreWithOptions(store.DB(), map[string]string{"dossiers": root}, logger, documents.StoreOptions{
				RootPolicies: map[string]documents.RootPolicy{"dossiers": {Indexing: true, Context: documents.RootContextPolicy{SearchBody: true}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewEmptyRegistry()
			registry.SetArchiveStore(archive)
			registry.SetWorkingMemoryStore(working)
			tools.RegisterDocumentTools(registry, documents.NewTools(documentStore))
			// The installed delegation front door activates the production gate.
			registry.Register(&tools.Tool{Name: "thane_now"})
			call := llm.ToolCall{ID: "search-call"}
			call.Function.Name = "search"
			call.Function.Arguments = map[string]any{"query": "MQTT"}
			model := &mockLLM{responses: []*llm.ChatResponse{
				{Model: "test-model", Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}},
				{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "Search complete."}},
			}}
			loop := &Loop{logger: logger, memory: newMockMem(), tools: registry, llm: model, model: "test-model"}
			loop.SetOrchestratorTools(cfg.Agent.OrchestratorTools)
			if tc.orchestratorTools != nil {
				loop.SetOrchestratorTools(tc.orchestratorTools)
			}
			// Use registered catalog membership; keep tag and request filtering
			// inside Run so the test also checks the executing tool's scope.
			tags := make(map[string]config.CapabilityTagConfig)
			for _, name := range registry.AllToolNames() {
				for _, tag := range registry.Get(name).Tags {
					entry := tags[tag]
					entry.Tools = append(entry.Tools, name)
					tags[tag] = entry
				}
			}
			loop.SetCapabilityTags(tags, nil)
			if _, err := loop.Run(context.Background(), &Request{
				ConversationID: "search-caller", Messages: []Message{{Role: "user", Content: "Find our earlier network decision."}},
				InitialTags: tc.tags, ExcludeTools: tc.exclude,
			}, nil); err != nil {
				t.Fatal(err)
			}
			if len(model.calls) != 2 {
				t.Fatalf("model calls = %d, want search followed by its result", len(model.calls))
			}
			for i, invocation := range model.calls {
				names := toolNames(invocation.Tools)
				if !hasName(names, "search") || hasName(names, "doc_search") != tc.wantDocuments || hasName(names, "archive_search") != tc.wantArchives {
					t.Errorf("call %d advertised incorrect source permissions: %v", i, names)
				}
				if hasName(names, "doc_read") {
					t.Errorf("call %d bypassed orchestrator gating: %v", i, names)
				}
			}
			var raw string
			for _, message := range model.calls[1].Messages {
				if message.Role == "tool" && message.ToolCallID == call.ID {
					raw = message.Content
				}
			}
			var result struct {
				Hits []struct {
					Source  string `json:"source"`
					Ref     string `json:"ref"`
					Excerpt string `json:"excerpt"`
				} `json:"hits"`
				Coverage []struct {
					Source string `json:"source"`
					Status string `json:"status"`
				} `json:"coverage"`
				Partial bool `json:"partial"`
			}
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatalf("search did not deliver a result to the model: %v\n%s", err, raw)
			}
			counts := make(map[string]int)
			for _, hit := range result.Hits {
				counts[hit.Source]++
				if !strings.Contains(hit.Excerpt, "MQTT") {
					t.Errorf("hit lacks stored matching evidence: %+v", hit)
				}
				if hit.Source == "documents" && hit.Ref != "dossiers:decision.md" {
					t.Errorf("document reference = %q, want dossiers:decision.md", hit.Ref)
				}
			}
			for source, allowed := range map[string]bool{"documents": tc.wantDocuments, "archives": tc.wantArchives} {
				want := 0
				status := "unavailable"
				if allowed {
					want = 1
					status = "searched"
				}
				if counts[source] != want {
					t.Errorf("%s hits = %d, want %d: %s", source, counts[source], want, raw)
				}
				found := false
				for _, coverage := range result.Coverage {
					if coverage.Source == source {
						found = true
						if coverage.Status != status {
							t.Errorf("%s coverage = %q, want %q", source, coverage.Status, status)
						}
					}
				}
				if !found {
					t.Errorf("missing %s coverage: %s", source, raw)
				}
			}
			if result.Partial != (!tc.wantDocuments || !tc.wantArchives) {
				t.Errorf("incorrect completeness: %s", raw)
			}
		})
	}
}
