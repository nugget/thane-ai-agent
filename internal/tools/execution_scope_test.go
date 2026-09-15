package tools

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func newExecutionScopeTestRegistry(handler func(context.Context, map[string]any) (string, error)) *Registry {
	r := NewEmptyRegistry()
	r.Register(&Tool{Name: "scope_probe", Handler: handler})
	r.Register(&Tool{Name: "archive_search"})
	r.Register(&Tool{Name: "doc_search"})
	r.SetTagIndex(map[string][]string{
		"archive": {"scope_probe", "archive_search"},
		"docs":    {"scope_probe", "doc_search"},
	})
	return r
}

func TestToolExecutionScopeHonorsFilters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		filter func(*Registry) *Registry
		want   string
	}{
		{"all", func(r *Registry) *Registry { return r }, "true/true"},
		{"allowlist", func(r *Registry) *Registry {
			return r.FilteredCopy([]string{"scope_probe", "archive_search"})
		}, "true/false"},
		{"exclusion", func(r *Registry) *Registry {
			return r.FilteredCopyExcluding([]string{"archive_search"})
		}, "false/true"},
		{"tag", func(r *Registry) *Registry { return r.FilterByTags([]string{"archive"}) }, "true/false"},
		{"combined", func(r *Registry) *Registry {
			return r.FilterByTags([]string{"archive"}).FilteredCopyExcluding([]string{"archive_search"})
		}, "false/false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newExecutionScopeTestRegistry(func(ctx context.Context, _ map[string]any) (string, error) {
				if got := RequestIDFromContext(ctx); got != "scope-request" {
					return "", fmt.Errorf("request ID = %q, want scope-request", got)
				}
				return fmt.Sprintf("%t/%t", toolAvailableInExecution(ctx, "archive_search"), toolAvailableInExecution(ctx, "doc_search")), nil
			})
			ctx := WithRequestID(context.Background(), "scope-request")
			got, err := tt.filter(r).Execute(ctx, "scope_probe", "{}")
			if err != nil || got != tt.want {
				t.Fatalf("source availability = %q, error = %v; want %q", got, err, tt.want)
			}
			if toolAvailableInExecution(ctx, "archive_search") || toolAvailableInExecution(ctx, "doc_search") {
				t.Fatal("execution granted source access to the parent context")
			}
		})
	}
}

func TestToolExecutionScopeSnapshot(t *testing.T) {
	t.Parallel()
	var captured context.Context
	r := newExecutionScopeTestRegistry(func(ctx context.Context, _ map[string]any) (string, error) {
		captured = ctx
		return "", nil
	})
	if _, err := r.Execute(context.Background(), "scope_probe", "{}"); err != nil {
		t.Fatal(err)
	}
	delete(r.tools, "doc_search")
	r.Register(&Tool{Name: "later_tool"})
	if !toolAvailableInExecution(captured, "doc_search") || toolAvailableInExecution(captured, "later_tool") {
		t.Fatal("registry changes altered an existing execution scope")
	}
}

func TestToolExecutionScopeConcurrentIsolation(t *testing.T) {
	t.Parallel()
	r := newExecutionScopeTestRegistry(func(ctx context.Context, args map[string]any) (string, error) {
		want, _ := args["source"].(string)
		for range 100 {
			for _, source := range []string{"archive_search", "doc_search"} {
				if toolAvailableInExecution(ctx, source) != (source == want) {
					return "", fmt.Errorf("scope for %s exposed incorrect access to %s", want, source)
				}
			}
		}
		return "", nil
	})
	registries := []*Registry{
		r.FilteredCopy([]string{"scope_probe", "archive_search"}),
		r.FilteredCopy([]string{"scope_probe", "doc_search"}),
	}
	args := []string{`{"source":"archive_search"}`, `{"source":"doc_search"}`}
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			<-start
			if _, err := registries[i%2].Execute(ctx, "scope_probe", args[i%2]); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if toolAvailableInExecution(ctx, "archive_search") || toolAvailableInExecution(ctx, "doc_search") {
		t.Fatal("missing execution scope granted source access")
	}
}
