package tools

import (
	"context"
	"encoding/json"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestBuildPlacementAdvisory(t *testing.T) {
	t.Parallel()

	containers := []containerTagSet{
		{name: "travel", tags: []string{"travel", "calendar"}},
		{name: "home", tags: []string{"ha"}},
		{name: "trips", tags: []string{"travel"}},
	}

	candNames := func(adv map[string]any) []string {
		cands := adv["candidates"].([]map[string]any)
		out := make([]string, len(cands))
		for i, c := range cands {
			out[i] = c["container"].(string)
		}
		return out
	}

	t.Run("root with overlapping tags surfaces sorted candidates", func(t *testing.T) {
		adv := buildPlacementAdvisory("flight_watch", "", []string{"travel"}, containers)
		if adv == nil {
			t.Fatal("expected an advisory")
		}
		if got := candNames(adv); len(got) != 2 || got[0] != "travel" || got[1] != "trips" {
			t.Fatalf("candidates = %v, want [travel trips]", got)
		}
		if adv["current_parent"] != looppkg.CoreLoopName {
			t.Errorf("current_parent = %v, want core", adv["current_parent"])
		}
	})

	t.Run("explicit core parent also counts as root", func(t *testing.T) {
		if buildPlacementAdvisory("x", "core", []string{"travel"}, containers) == nil {
			t.Fatal("an explicit core parent should still advise")
		}
	})

	t.Run("nested loop gets no advisory", func(t *testing.T) {
		if adv := buildPlacementAdvisory("x", "travel", []string{"travel"}, containers); adv != nil {
			t.Fatalf("a nested loop should get no advisory, got %v", adv)
		}
	})

	t.Run("no tags means no advisory", func(t *testing.T) {
		if buildPlacementAdvisory("x", "", nil, containers) != nil {
			t.Fatal("a tagless loop should get no advisory")
		}
	})

	t.Run("no overlap means no advisory", func(t *testing.T) {
		if buildPlacementAdvisory("x", "", []string{"finance"}, containers) != nil {
			t.Fatal("no shared tags should get no advisory")
		}
	})

	t.Run("never suggests nesting under self", func(t *testing.T) {
		// A container named "travel" (tag travel) must not be told to nest under
		// itself; only the other tag-sharing container remains.
		adv := buildPlacementAdvisory("travel", "", []string{"travel"}, containers)
		if adv == nil {
			t.Fatal("expected an advisory (trips still matches)")
		}
		if got := candNames(adv); len(got) != 1 || got[0] != "trips" {
			t.Fatalf("candidates = %v, want [trips]", got)
		}
	})
}

func TestPlacementAdvisoryOnLint(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{Name: "travel", Operation: looppkg.OperationContainer, Intent: "trip logistics", Tags: []string{"travel"}},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	reg := NewEmptyRegistry()
	reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry: defs,
		View: func() *looppkg.DefinitionRegistryView {
			return looppkg.BuildDefinitionRegistryView(defs.Snapshot(), nil)
		},
	})

	// Lint a service spec declaring "travel" with no parent — it should draw a
	// non-blocking placement advisory pointing at the travel container.
	out, err := reg.Get("loop_definition_lint").Handler(context.Background(), map[string]any{
		"spec": map[string]any{
			"name": "flight_watch", "operation": "service", "task": "watch flights",
			"tags": []any{"travel"}, "sleep_min": "5m", "sleep_max": "30m",
		},
	})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	var got struct {
		Valid             bool `json:"valid"`
		PlacementAdvisory *struct {
			CurrentParent string `json:"current_parent"`
			Candidates    []struct {
				Container  string   `json:"container"`
				SharedTags []string `json:"shared_tags"`
			} `json:"candidates"`
		} `json:"placement_advisory"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if !got.Valid {
		t.Fatalf("spec should lint valid:\n%s", out)
	}
	if got.PlacementAdvisory == nil {
		t.Fatalf("expected placement_advisory on root-parented tagged loop:\n%s", out)
	}
	if len(got.PlacementAdvisory.Candidates) != 1 ||
		got.PlacementAdvisory.Candidates[0].Container != "travel" {
		t.Fatalf("candidates = %+v, want [travel]", got.PlacementAdvisory.Candidates)
	}
}
