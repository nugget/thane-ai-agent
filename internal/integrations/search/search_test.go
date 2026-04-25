package search

import (
	"context"
	"testing"
)

// mockProvider is a simple test provider.
type mockProvider struct {
	name    string
	results []Result
	err     error
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Search(_ context.Context, _ string, _ Options) ([]Result, error) {
	return m.results, m.err
}

func TestManagerSearch(t *testing.T) {
	mgr := NewManager("mock")
	mgr.Register(&mockProvider{
		name: "mock",
		results: []Result{
			{Title: "Test", URL: "https://example.com", Snippet: "A test result"},
		},
	})

	results, err := mgr.Search(context.Background(), "test", Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Test" {
		t.Errorf("expected title 'Test', got %q", results[0].Title)
	}
}

func TestManagerSearchWith(t *testing.T) {
	mgr := NewManager("primary")
	mgr.Register(&mockProvider{name: "primary", results: []Result{{Title: "Primary"}}})
	mgr.Register(&mockProvider{name: "secondary", results: []Result{{Title: "Secondary"}}})

	results, err := mgr.SearchWith(context.Background(), "secondary", "test", Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Title != "Secondary" {
		t.Errorf("expected 'Secondary', got %q", results[0].Title)
	}
}

func TestManagerUnconfigured(t *testing.T) {
	mgr := NewManager("missing")
	_, err := mgr.Search(context.Background(), "test", Options{})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestFormatResults(t *testing.T) {
	results := []Result{
		{Title: "First", URL: "https://a.com", Snippet: "Snippet A"},
		{Title: "Second", URL: "https://b.com"},
	}
	out := FormatResults(results, 2)
	if out == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestFormatResultsEmpty(t *testing.T) {
	out := FormatResults(nil, 0)
	if out != "No results found." {
		t.Errorf("expected 'No results found.', got %q", out)
	}
}

func TestConfigured(t *testing.T) {
	mgr := NewManager("test")
	if mgr.Configured() {
		t.Error("empty manager should not be configured")
	}
	mgr.Register(&mockProvider{name: "test"})
	if !mgr.Configured() {
		t.Error("manager with provider should be configured")
	}
}
