package llm

import (
	"context"
	"testing"
)

// TestClampMaxOutputTokens pins the precedence a provider depends on: a
// budget may tighten a ceiling and never raise it. Raising it would send
// a value the provider has already decided it cannot accept, which is
// how a turn dies for asking rather than for running long.
func TestClampMaxOutputTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		budget  int
		ceiling int
		want    int
	}{
		{name: "no budget leaves the ceiling alone", budget: 0, ceiling: 16384, want: 16384},
		{name: "tighter budget wins", budget: 500, ceiling: 16384, want: 500},
		{name: "looser budget does not raise the ceiling", budget: 99999, ceiling: 16384, want: 16384},
		{name: "equal budget is a no-op", budget: 16384, ceiling: 16384, want: 16384},
		{name: "no ceiling of its own takes the budget", budget: 500, ceiling: 0, want: 500},
		{name: "neither set stays unset", budget: 0, ceiling: 0, want: 0},
		{name: "negative budget is not a ceiling", budget: -5, ceiling: 16384, want: 16384},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := WithMaxOutputTokens(context.Background(), tt.budget)
			if got := ClampMaxOutputTokens(ctx, tt.ceiling); got != tt.want {
				t.Errorf("ClampMaxOutputTokens(budget=%d, ceiling=%d) = %d, want %d", tt.budget, tt.ceiling, got, tt.want)
			}
		})
	}
}

// TestWithMaxOutputTokensRejectsNonPositive pins that "no ceiling" and
// "no budget left" never arrive as the same value. A caller with nothing
// left stops the turn; a provider must never read that as unlimited.
func TestWithMaxOutputTokensRejectsNonPositive(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1} {
		ctx := WithMaxOutputTokens(context.Background(), n)
		if got := MaxOutputTokensFromContext(ctx); got != 0 {
			t.Errorf("WithMaxOutputTokens(%d) then read = %d, want 0", n, got)
		}
	}
	if got := MaxOutputTokensFromContext(context.Background()); got != 0 {
		t.Errorf("bare context = %d, want 0", got)
	}
}
