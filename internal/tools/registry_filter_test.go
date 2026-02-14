package tools

import (
	"context"
	"sort"
	"testing"
)

func newTestRegistry() *Registry {
	r := &Registry{tools: make(map[string]*Tool)}
	r.Register(&Tool{
		Name:        "alpha",
		Description: "Tool alpha",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "alpha-result", nil
		},
	})
	r.Register(&Tool{
		Name:        "beta",
		Description: "Tool beta",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "beta-result", nil
		},
	})
	r.Register(&Tool{
		Name:        "gamma",
		Description: "Tool gamma",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "gamma-result", nil
		},
	})
	return r
}

func TestAllToolNames(t *testing.T) {
	r := newTestRegistry()
	names := r.AllToolNames()
	sort.Strings(names)

	want := []string{"alpha", "beta", "gamma"}
	if len(names) != len(want) {
		t.Fatalf("AllToolNames() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("AllToolNames()[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestFilteredCopy(t *testing.T) {
	r := newTestRegistry()

	tests := []struct {
		name      string
		include   []string
		wantNames []string
		wantExec  map[string]string // tool name → expected result
	}{
		{
			name:      "subset",
			include:   []string{"alpha", "gamma"},
			wantNames: []string{"alpha", "gamma"},
			wantExec:  map[string]string{"alpha": "alpha-result", "gamma": "gamma-result"},
		},
		{
			name:      "single",
			include:   []string{"beta"},
			wantNames: []string{"beta"},
			wantExec:  map[string]string{"beta": "beta-result"},
		},
		{
			name:      "empty list",
			include:   []string{},
			wantNames: []string{},
		},
		{
			name:      "nonexistent tools skipped",
			include:   []string{"alpha", "nonexistent"},
			wantNames: []string{"alpha"},
			wantExec:  map[string]string{"alpha": "alpha-result"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := r.FilteredCopy(tt.include)

			names := filtered.AllToolNames()
			sort.Strings(names)
			sort.Strings(tt.wantNames)

			if len(names) != len(tt.wantNames) {
				t.Fatalf("FilteredCopy(%v) has %d tools, want %d: got %v", tt.include, len(names), len(tt.wantNames), names)
			}
			for i := range tt.wantNames {
				if names[i] != tt.wantNames[i] {
					t.Errorf("tool[%d] = %q, want %q", i, names[i], tt.wantNames[i])
				}
			}

			// Verify handlers still work
			for toolName, wantResult := range tt.wantExec {
				result, err := filtered.Execute(context.Background(), toolName, "")
				if err != nil {
					t.Errorf("Execute(%q) error = %v", toolName, err)
				}
				if result != wantResult {
					t.Errorf("Execute(%q) = %q, want %q", toolName, result, wantResult)
				}
			}

			// Verify excluded tools are absent
			if _, err := filtered.Execute(context.Background(), "beta", ""); tt.name == "subset" {
				if err == nil {
					t.Error("expected error executing excluded tool 'beta'")
				}
			}
		})
	}
}

func TestFilteredCopyExcluding(t *testing.T) {
	r := newTestRegistry()

	tests := []struct {
		name      string
		exclude   []string
		wantNames []string
	}{
		{
			name:      "exclude one",
			exclude:   []string{"beta"},
			wantNames: []string{"alpha", "gamma"},
		},
		{
			name:      "exclude all",
			exclude:   []string{"alpha", "beta", "gamma"},
			wantNames: []string{},
		},
		{
			name:      "exclude none",
			exclude:   []string{},
			wantNames: []string{"alpha", "beta", "gamma"},
		},
		{
			name:      "exclude nonexistent",
			exclude:   []string{"nonexistent"},
			wantNames: []string{"alpha", "beta", "gamma"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := r.FilteredCopyExcluding(tt.exclude)

			names := filtered.AllToolNames()
			sort.Strings(names)
			sort.Strings(tt.wantNames)

			if len(names) != len(tt.wantNames) {
				t.Fatalf("FilteredCopyExcluding(%v) has %d tools, want %d: got %v", tt.exclude, len(names), len(tt.wantNames), names)
			}
			for i := range tt.wantNames {
				if names[i] != tt.wantNames[i] {
					t.Errorf("tool[%d] = %q, want %q", i, names[i], tt.wantNames[i])
				}
			}
		})
	}
}

func TestFilteredCopy_DoesNotMutateSource(t *testing.T) {
	r := newTestRegistry()
	origCount := len(r.AllToolNames())

	filtered := r.FilteredCopy([]string{"alpha"})
	filtered.Register(&Tool{Name: "new_tool", Handler: func(ctx context.Context, args map[string]any) (string, error) {
		return "", nil
	}})

	if len(r.AllToolNames()) != origCount {
		t.Error("FilteredCopy mutated the source registry")
	}
}
