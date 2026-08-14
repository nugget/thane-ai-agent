package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateBindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		bindings map[string]string
		wantErr  string
	}{
		{
			name:     "no bindings is valid",
			bindings: nil,
		},
		{
			name:     "registered key",
			bindings: map[string]string{BindingForgeAccount: "github-readonly"},
		},
		{
			// A binding that quietly did nothing would read like a
			// boundary while being none, which is worse than declaring
			// no binding at all.
			name:     "unregistered key refuses",
			bindings: map[string]string{"forge_acount": "github-readonly"},
			wantErr:  "unknown binding",
		},
		{
			name:     "empty value refuses",
			bindings: map[string]string{BindingForgeAccount: "   "},
			wantErr:  "empty value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBindings(tt.bindings)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateBindings() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateBindings() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateBindingsNamesTheRegisteredKeys(t *testing.T) {
	t.Parallel()

	// An unknown-key error that does not say what the known keys are
	// leaves the author guessing at a closed set.
	err := ValidateBindings(map[string]string{"nope": "x"})
	if err == nil {
		t.Fatal("ValidateBindings() = nil, want an error")
	}
	if !strings.Contains(err.Error(), BindingForgeAccount) {
		t.Errorf("error = %q, want it to list the registered keys", err.Error())
	}
}

func TestMergeBindingsAncestorsWin(t *testing.T) {
	t.Parallel()

	ancestor := map[string]string{BindingForgeAccount: "github-readonly"}
	own := map[string]string{BindingForgeAccount: "github-primary"}

	tests := []struct {
		name string
		sets []map[string]string
		want map[string]string
	}{
		{
			name: "nothing declared stays nil",
			sets: []map[string]string{nil, nil},
			want: nil,
		},
		{
			name: "own binding applies when no ancestor declares one",
			sets: []map[string]string{nil, own},
			want: map[string]string{BindingForgeAccount: "github-primary"},
		},
		{
			// The load-bearing case. If the child won here, a container's
			// binding would be advice rather than a boundary, and any
			// loop could escape a restriction by declaring past it.
			name: "ancestor outranks the loop's own declaration",
			sets: []map[string]string{ancestor, own},
			want: map[string]string{BindingForgeAccount: "github-readonly"},
		},
		{
			// A per-request override is weakest of all: a restriction a
			// caller could lift by passing a field is not a restriction.
			name: "request override cannot displace an ancestor",
			sets: []map[string]string{ancestor, nil, {BindingForgeAccount: "github-primary"}},
			want: map[string]string{BindingForgeAccount: "github-readonly"},
		},
		{
			name: "empty values are ignored rather than binding to nothing",
			sets: []map[string]string{{BindingForgeAccount: ""}, own},
			want: map[string]string{BindingForgeAccount: "github-primary"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mergeBindings(tt.sets...)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeBindings() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("mergeBindings()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestBindingsContextRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("unbound context yields nothing", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		if got := BindingFromContext(ctx, BindingForgeAccount); got != "" {
			t.Errorf("BindingFromContext() = %q, want empty", got)
		}
		if got := BindingsFromContext(ctx); got != nil {
			t.Errorf("BindingsFromContext() = %v, want nil", got)
		}
	})

	t.Run("empty bindings leave the context untouched", func(t *testing.T) {
		t.Parallel()
		base := context.Background()
		if got := WithBindings(base, nil); got != base {
			t.Error("WithBindings() with nil returned a derived context; unbound callers should be untouched")
		}
		if got := WithBindings(base, map[string]string{}); got != base {
			t.Error("WithBindings() with an empty map returned a derived context")
		}
	})

	t.Run("a bound value round-trips", func(t *testing.T) {
		t.Parallel()
		ctx := WithBindings(context.Background(), map[string]string{BindingForgeAccount: "github-readonly"})
		if got := BindingFromContext(ctx, BindingForgeAccount); got != "github-readonly" {
			t.Errorf("BindingFromContext() = %q, want %q", got, "github-readonly")
		}
		if got := BindingFromContext(ctx, "other_key"); got != "" {
			t.Errorf("BindingFromContext(other) = %q, want empty", got)
		}
	})

	t.Run("the stored map is not aliased to the caller's", func(t *testing.T) {
		t.Parallel()
		source := map[string]string{BindingForgeAccount: "github-readonly"}
		ctx := WithBindings(context.Background(), source)
		source[BindingForgeAccount] = "github-primary"
		if got := BindingFromContext(ctx, BindingForgeAccount); got != "github-readonly" {
			t.Errorf("BindingFromContext() = %q; a caller mutating its map rewrote a live boundary", got)
		}
		returned := BindingsFromContext(ctx)
		returned[BindingForgeAccount] = "github-primary"
		if got := BindingFromContext(ctx, BindingForgeAccount); got != "github-readonly" {
			t.Errorf("BindingFromContext() = %q; mutating a returned copy rewrote the boundary", got)
		}
	})
}

func TestSpecValidateRejectsUnknownBinding(t *testing.T) {
	t.Parallel()

	spec := &Spec{
		Name:      "watcher",
		Operation: OperationService,
		Task:      "watch",
		Bindings:  map[string]string{"forge": "github-readonly"},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want a refusal for an unregistered binding key")
	}
	if !strings.Contains(err.Error(), "unknown binding") {
		t.Errorf("error = %q, want it to name the problem as an unknown binding", err.Error())
	}
}

func TestSpecToConfigCarriesBindings(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Name:      "watcher",
		Operation: OperationService,
		Task:      "watch",
		Bindings:  map[string]string{BindingForgeAccount: "github-readonly"},
	}
	cfg := spec.ToConfig()
	if got := cfg.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("Config.Bindings[%q] = %q, want %q", BindingForgeAccount, got, "github-readonly")
	}

	// The config must not alias the spec's map, or a later spec edit
	// would silently move a running loop's boundary.
	spec.Bindings[BindingForgeAccount] = "github-primary"
	if got := cfg.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("Config.Bindings[%q] = %q after mutating the spec; want an independent copy", BindingForgeAccount, got)
	}
}

// TestBindingsSurviveJSONRoundTrip guards the path that carries a
// binding across a restart. Spec marshals through the specJSON wire
// type, and definitions persist as JSON in opstate — so a field absent
// from that wire type is silently dropped on save and comes back empty
// on load. For a boundary, that failure is the worst-shaped one
// available: the loop restarts unbound, reaching every configured
// account, with the stored definition still showing the restriction
// and nothing anywhere reporting that it stopped applying.
func TestBindingsSurviveJSONRoundTrip(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Name:      "watcher",
		Operation: OperationService,
		Task:      "watch",
		Bindings:  map[string]string{BindingForgeAccount: "github-readonly"},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), "bindings") {
		t.Fatalf("marshalled spec omits bindings entirely: %s", data)
	}

	var back Spec
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := back.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Fatalf("binding after round-trip = %q, want %q — a persisted loop would restart unbound", got, "github-readonly")
	}

	// The decoded spec must own its map, not alias the wire value.
	back.Bindings[BindingForgeAccount] = "github-primary"
	var second Spec
	if err := json.Unmarshal(data, &second); err != nil {
		t.Fatalf("Unmarshal (second): %v", err)
	}
	if got := second.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("second decode = %q, want an independent map", got)
	}
}

// TestStatusDoesNotAliasBindings covers the deep-copy contract on
// Status(). An aliased map lets a caller rewrite the active account
// boundary without the loop lock, racing an iteration that is reading
// it — a data race and a security hole in one.
func TestStatusDoesNotAliasBindings(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:     "bound",
		Task:     "t",
		Bindings: map[string]string{BindingForgeAccount: "github-readonly"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	snapshot := l.Status()
	if got := snapshot.Config.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Fatalf("Status().Config.Bindings = %q, want the live binding", got)
	}
	snapshot.Config.Bindings[BindingForgeAccount] = "github-primary"

	req, err := l.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}
	if got := req.Bindings[BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("binding = %q after mutating a Status() snapshot; the snapshot aliased live state", got)
	}
}
