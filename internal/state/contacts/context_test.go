package contacts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/contacts/contextfmt"
)

// parseContactsBody extracts the JSON payload that follows the
// "### Relevant Contacts" heading and unmarshals it for assertion.
func parseContactsBody(t *testing.T, out string) []contextfmt.Match {
	t.Helper()
	const heading = "### Relevant Contacts\n\n"
	if !strings.HasPrefix(out, heading) {
		t.Fatalf("output missing heading prefix\nGot:\n%s", out)
	}
	body := strings.TrimPrefix(out, heading)
	var env struct {
		Contacts []contextfmt.Match `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("body not parseable JSON: %v\nBody: %s", err, body)
	}
	return env.Contacts
}

func TestContextProvider_NilEmbedder(t *testing.T) {
	store := newTestStore(t)
	cp := NewContextProvider(store, nil)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result with nil embedder, got %q", result)
	}
}

func TestContextProvider_EmptyMessage(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{embedding: []float32{1, 0, 0}}
	cp := NewContextProvider(store, emb)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("TagContext() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for empty message, got %q", result)
	}
}

func TestContextProvider_NoContacts(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{embedding: []float32{1, 0, 0}}
	cp := NewContextProvider(store, emb)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("TagContext() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result with no contacts, got %q", result)
	}
}

func TestContextProvider_ReturnsRelevant(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Alice Relevant", Kind: "individual", AISummary: "Works at TechCo"}
	created1, _ := store.Upsert(c1)
	_ = store.SetEmbedding(created1.ID, []float32{0.9, 0.1, 0.0})
	_ = store.AddProperty(created1.ID, &Property{Property: "EMAIL", Value: "alice@techco.com"})
	_ = store.AddProperty(created1.ID, &Property{Property: "timezone", Value: "America/Chicago"})

	c2 := &Contact{FormattedName: "Bob Irrelevant", Kind: "individual", AISummary: "Completely unrelated"}
	created2, _ := store.Upsert(c2)
	_ = store.SetEmbedding(created2.ID, []float32{0.0, 0.0, 1.0})

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMinScore(0.5)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "tell me about Alice"})
	if err != nil {
		t.Fatalf("TagContext() error = %v", err)
	}
	matches := parseContactsBody(t, result)

	if len(matches) != 1 {
		t.Fatalf("expected 1 match (Alice only), got %d", len(matches))
	}
	m := matches[0]
	if m.Name != "Alice Relevant" {
		t.Errorf("name = %q, want %q", m.Name, "Alice Relevant")
	}
	if m.Summary != "Works at TechCo" {
		t.Errorf("summary = %q, want %q", m.Summary, "Works at TechCo")
	}
	if m.TrustZone != "known" {
		t.Errorf("trust_zone = %q, want %q", m.TrustZone, "known")
	}
	props := propsByLabel(m.Properties)
	if _, ok := props["EMAIL"]; !ok {
		t.Errorf("expected EMAIL property, got %+v", m.Properties)
	}
	if _, ok := props["timezone"]; !ok {
		t.Errorf("expected timezone property, got %+v", m.Properties)
	}
}

func TestContextProvider_ScoreFiltering(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Low Score Contact", Kind: "individual"}
	created, _ := store.Upsert(c)
	_ = store.SetEmbedding(created.ID, []float32{0.0, 1.0, 0.0})

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMinScore(0.5)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "query"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result for low-score contact, got %q", result)
	}
}

func TestContextProvider_MaxContacts(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 5; i++ {
		c := &Contact{FormattedName: strings.Repeat("X", i+1), Kind: "individual"}
		created, _ := store.Upsert(c)
		_ = store.SetEmbedding(created.ID, []float32{0.8, 0.2, 0.0})
	}

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMaxContacts(2)
	cp.SetMinScore(0.1)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "query"})
	if err != nil {
		t.Fatal(err)
	}
	matches := parseContactsBody(t, result)
	if len(matches) != 2 {
		t.Errorf("expected 2 matches under SetMaxContacts(2), got %d", len(matches))
	}
}

func TestContextProvider_TrustZoneTag(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Trusted Alice", Kind: "individual", TrustZone: "trusted", AISummary: "A trusted friend"}
	created, _ := store.Upsert(c)
	_ = store.SetEmbedding(created.ID, []float32{0.9, 0.1, 0.0})

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMinScore(0.1)

	result, err := cp.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "Alice"})
	if err != nil {
		t.Fatalf("TagContext() error = %v", err)
	}
	matches := parseContactsBody(t, result)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].TrustZone != "trusted" {
		t.Errorf("trust_zone = %q, want %q", matches[0].TrustZone, "trusted")
	}
}

func TestSetMaxContacts_ClampsToMin(t *testing.T) {
	store := newTestStore(t)
	cp := NewContextProvider(store, nil)

	cp.SetMaxContacts(0)
	if cp.maxContacts != 1 {
		t.Errorf("SetMaxContacts(0) = %d, want 1", cp.maxContacts)
	}

	cp.SetMaxContacts(-5)
	if cp.maxContacts != 1 {
		t.Errorf("SetMaxContacts(-5) = %d, want 1", cp.maxContacts)
	}

	cp.SetMaxContacts(10)
	if cp.maxContacts != 10 {
		t.Errorf("SetMaxContacts(10) = %d, want 10", cp.maxContacts)
	}
}

func propsByLabel(props []contextfmt.Property) map[string]contextfmt.Property {
	m := make(map[string]contextfmt.Property, len(props))
	for _, p := range props {
		m[p.Label] = p
	}
	return m
}
