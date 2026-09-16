package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestKnowledgeSearchAcrossEvidenceAndDocuments(t *testing.T) {
	registry, _, insert := newArchiveTestRegistry(t)
	insert("decision-conversation", "decision-session", "user", "We chose MQTT because it works offline.", time.Now().Add(-time.Hour))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "decision.md"), []byte("---\ntitle: Network decision\n---\n\nCurrent network notes.\n\nThe MQTT choice preserved offline operation."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	working, err := memory.NewWorkingMemoryStore(db, true)
	if err != nil {
		t.Fatal(err)
	}
	registry.SetWorkingMemoryStore(working)
	store, err := documents.NewStoreWithOptions(db, map[string]string{"dossiers": root}, nil, documents.StoreOptions{
		RootPolicies: map[string]documents.RootPolicy{"dossiers": {Indexing: true, Context: documents.RootContextPolicy{SearchBody: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	RegisterDocumentTools(registry, documents.NewTools(store))
	for _, tc := range []struct {
		name     string
		tools    []string
		args     string
		wantHits int
		partial  bool
	}{
		{name: "both sources", args: `{"query":"MQTT"}`, wantHits: 2},
		{name: "archive only grant", tools: []string{"search", "archive_search"}, args: `{"query":"MQTT"}`, wantHits: 1, partial: true},
		{name: "document only grant", tools: []string{"search", "doc_search"}, args: `{"query":"MQTT"}`, wantHits: 1, partial: true},
		{name: "root implies documents", args: `{"query":"MQTT","root":"dossiers"}`, wantHits: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scoped := registry
			if tc.tools != nil {
				scoped = registry.FilteredCopy(tc.tools)
			}
			raw, err := scoped.Execute(context.Background(), "search", tc.args)
			if err != nil {
				t.Fatal(err)
			}
			var result knowledgeSearchResult
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Hits) != tc.wantHits || result.Partial != tc.partial {
				t.Fatalf("unexpected coverage/hits: %s", raw)
			}
			for _, hit := range result.Hits {
				if hit.Read == nil || !strings.Contains(hit.Excerpt, "MQTT") || !strings.HasPrefix(hit.Age, "-") {
					t.Errorf("incomplete evidence hit: %+v", hit)
				}
				if hit.Source == "documents" && (hit.Evidence != "unspecified" || hit.TimeBasis != "document_modified" || hit.Ref != "dossiers:decision.md") {
					t.Errorf("document provenance: %+v", hit)
				}
				if hit.Source == "archives" && (hit.Evidence != "primary" || hit.Ref != "archive:session:decision-session") {
					t.Errorf("archive provenance: %+v", hit)
				}
			}
		})
	}
	if _, err := registry.FilteredCopy([]string{"search"}).Execute(context.Background(), "search", `{"query":"MQTT"}`); err == nil {
		t.Fatal("search alone granted source access")
	}
	// Composition must work when document tools are registered first too.
	reordered := NewEmptyRegistry()
	RegisterDocumentTools(reordered, documents.NewTools(store))
	reordered.SetArchiveStore(registry.archiveStore)
	raw, err := reordered.Execute(context.Background(), "search", `{"query":"MQTT"}`)
	if err != nil || !strings.Contains(raw, `"source":"archives"`) || !strings.Contains(raw, `"source":"documents"`) {
		t.Fatalf("registration order lost a source: %s, %v", raw, err)
	}
}

type testKnowledgeProvider struct {
	source string
	hits   []knowledgeHit
	err    error
	calls  int
}

func (p *testKnowledgeProvider) name() string       { return p.source }
func (p *testKnowledgeProvider) permission() string { return p.source + "_search" }
func (p *testKnowledgeProvider) search(context.Context, knowledgeSearchRequest) (knowledgeSourceResult, error) {
	p.calls++
	return knowledgeSourceResult{Hits: p.hits}, p.err
}

func TestKnowledgeSearchPartialFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		first   error
		second  error
		cancel  bool
		wantErr bool
	}{
		{name: "one failed", first: errors.New("index unavailable")},
		{name: "all failed", first: errors.New("index unavailable"), second: errors.New("read failed"), wantErr: true},
		{name: "caller cancelled", cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &testKnowledgeProvider{source: "a", err: tc.first}
			b := &testKnowledgeProvider{source: "b", err: tc.second}
			registry := NewEmptyRegistry()
			registry.Register(&Tool{Name: "a_search"})
			registry.Register(&Tool{Name: "b_search"})
			ctx, cancel := context.WithCancel(withToolExecutionScope(context.Background(), registry))
			defer cancel()
			if tc.cancel {
				cancel()
			}
			result, err := runKnowledgeSearch(ctx, knowledgeSearchRequest{Query: "decision", Limit: 3}, []knowledgeSearchProvider{a, b})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, result = %+v", err, result)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if !tc.wantErr && (!result.Partial || result.Coverage[0].Status != "error" || result.Coverage[1].Status != "searched") {
				t.Fatalf("failed source reported as empty: %+v", result)
			}
		})
	}
}

func TestKnowledgeSearchGlobalBudgetAndValidJSON(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&Tool{Name: "a_search"})
	registry.Register(&Tool{Name: "b_search"})
	a := &testKnowledgeProvider{source: "a"}
	b := &testKnowledgeProvider{source: "b"}
	for i := range 12 {
		a.hits = append(a.hits, knowledgeHit{Source: "a", Ref: "a", Excerpt: strings.Repeat("界\"", 300), MessageID: string(rune('a' + i))})
		b.hits = append(b.hits, knowledgeHit{Source: "b", Ref: "b", Excerpt: strings.Repeat("界\"", 300), MessageID: string(rune('a' + i))})
	}
	result, err := runKnowledgeSearch(withToolExecutionScope(context.Background(), registry), knowledgeSearchRequest{Query: "decision", Limit: 20}, []knowledgeSearchProvider{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 20 || result.Hits[0].Source != "a" || result.Hits[1].Source != "b" || !result.Truncated {
		t.Fatalf("global limit/order: %+v", result)
	}
	raw, err := fitKnowledgeSearch(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > archiveResultByteCap || !utf8.ValidString(raw) || !json.Valid([]byte(raw)) {
		t.Fatalf("invalid bounded result: %d bytes", len(raw))
	}
	if len(result.Hits) == 0 || !result.Truncated || result.Coverage[0].Returned+result.Coverage[1].Returned != len(result.Hits) {
		t.Fatalf("truncated coverage lost hits: %+v", result)
	}
}

func TestKnowledgeSearchArgumentErrors(t *testing.T) {
	registry, _, _ := newArchiveTestRegistry(t)
	for _, args := range []string{
		`{}`, `{"query":" "}`, `{"query":"x","limit":0}`, `{"query":"x","limit":2.5}`,
		`{"query":"x","sources":[]}`, `{"query":"x","sources":["email"]}`, `{"query":"x","sources":["archives"],"root":"kb"}`,
		`{"query":"x","root":42}`, `{"query":"x","source":["archives"]}`, `{"query":"x","async":true}`,
	} {
		if _, err := registry.Execute(context.Background(), "search", args); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
}
