package llm

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestWithCallKind pins that a stand-in call keeps the iteration of the
// call it replaces, and that a context with no call site is not given a
// made-up one: iteration 0 on an unplaced call would be a false join.
func TestWithCallKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ctx      context.Context
		kind     CallKind
		want     CallSite
		wantSite bool
	}{
		{
			name:     "keeps the iteration and replaces the kind",
			ctx:      WithCallSite(context.Background(), CallSite{Iteration: 3, Kind: CallKindIteration}),
			kind:     CallKindRetry,
			want:     CallSite{Iteration: 3, Kind: CallKindRetry},
			wantSite: true,
		},
		{
			name:     "restamps an already restamped site",
			ctx:      WithCallKind(WithCallSite(context.Background(), CallSite{Iteration: 1, Kind: CallKindIteration}), CallKindRetry),
			kind:     CallKindFailover,
			want:     CallSite{Iteration: 1, Kind: CallKindFailover},
			wantSite: true,
		},
		{
			name:     "an unplaced context stays unplaced",
			ctx:      context.Background(),
			kind:     CallKindRetry,
			wantSite: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := CallSiteFromContext(WithCallKind(tt.ctx, tt.kind))
			if ok != tt.wantSite || got != tt.want {
				t.Fatalf("CallSiteFromContext = %+v, %v; want %+v, %v", got, ok, tt.want, tt.wantSite)
			}
		})
	}
}

// TestCarryCallScope pins what crosses onto a detached context: the
// values that bound and place the call, and never the source's deadline.
// A recovery that inherited the expired deadline would be cancelled
// before it started, which is the reason the context is detached.
func TestCarryCallScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		budget     int
		site       *CallSite
		wantBudget int
	}{
		{name: "budget and call site cross", budget: 500, site: &CallSite{Iteration: 2, Kind: CallKindIteration}, wantBudget: 500},
		{name: "call site crosses without a budget", site: &CallSite{Iteration: 4, Kind: CallKindRetry}},
		{name: "budget crosses without a call site", budget: 64, wantBudget: 64},
		{name: "nothing to carry attaches nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			src = WithMaxOutputTokens(src, tt.budget)
			if tt.site != nil {
				src = WithCallSite(src, *tt.site)
			}

			got := CarryCallScope(context.Background(), src)

			if budget := MaxOutputTokensFromContext(got); budget != tt.wantBudget {
				t.Errorf("budget = %d, want %d", budget, tt.wantBudget)
			}
			site, ok := CallSiteFromContext(got)
			if tt.site == nil {
				if ok {
					t.Errorf("call site = %+v, want none", site)
				}
			} else if !ok || site != *tt.site {
				t.Errorf("call site = %+v, %v; want %+v", site, ok, *tt.site)
			}
			if _, hasDeadline := got.Deadline(); hasDeadline {
				t.Error("source deadline crossed onto the detached context")
			}
			cancel()
			if err := got.Err(); err != nil {
				t.Errorf("cancelling the source cancelled the carried context: %v", err)
			}
		})
	}
}

// TestCallSiteJSON pins the serialized field names. CallSite crosses
// package boundaries into capture and evaluation, so its wire names are
// part of the contract and must not drift to Go's default spelling.
func TestCallSiteJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		site CallSite
		want string
	}{
		{
			name: "ordinary iteration",
			site: CallSite{Iteration: 0, Kind: CallKindIteration},
			want: `{"iteration":0,"kind":"iteration"}`,
		},
		{
			name: "recovery keeps its iteration",
			site: CallSite{Iteration: 4, Kind: CallKindContextRecoveryNoTools},
			want: `{"iteration":4,"kind":"context_recovery_no_tools"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tt.site)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("json = %s, want %s", got, tt.want)
			}
			var back CallSite
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if back != tt.site {
				t.Fatalf("round trip = %+v, want %+v", back, tt.site)
			}
		})
	}
}
