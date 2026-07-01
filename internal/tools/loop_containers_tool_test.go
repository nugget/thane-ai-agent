package tools

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestLoopContainers(t *testing.T) {
	live := looppkg.NewRegistry(looppkg.WithMaxLoops(10))
	runner := noopLoopRunner{}
	mk := func(cfg looppkg.Config) *looppkg.Loop {
		l, err := looppkg.New(cfg, looppkg.Deps{Runner: runner})
		if err != nil {
			t.Fatalf("New(%s): %v", cfg.Name, err)
		}
		if err := live.Register(l); err != nil {
			t.Fatalf("Register(%s): %v", cfg.Name, err)
		}
		return l
	}
	svc := func(name, parentID string) looppkg.Config {
		return looppkg.Config{
			Name: name, Task: "work", Operation: looppkg.OperationService,
			ParentID: parentID, SleepMin: time.Minute, SleepMax: time.Minute,
		}
	}

	travel := mk(looppkg.Config{
		Name: "travel", Operation: looppkg.OperationContainer,
		Intent: "trip logistics", Tags: []string{"travel"},
	})
	flight := mk(svc("flight_watch", travel.ID()))
	mk(svc("hotel_tracker", travel.ID()))
	mk(svc("seat_alert", flight.ID())) // grandchild → descendant but not direct child
	mk(svc("battery_watch", ""))       // top-level non-container, must be excluded

	reg := NewEmptyRegistry()
	reg.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{Registry: live})

	out, err := reg.Get("loop_containers").Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("loop_containers: %v", err)
	}
	var got struct {
		Status     string `json:"status"`
		Containers []struct {
			Name            string   `json:"name"`
			Intent          string   `json:"intent"`
			ChildCount      int      `json:"child_count"`
			DescendantCount int      `json:"descendant_count"`
			ConfersTags     []string `json:"confers_tags"`
			SampleChildren  []string `json:"sample_children"`
		} `json:"containers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}

	// Only the container is listed; the top-level service loop is excluded.
	if len(got.Containers) != 1 {
		t.Fatalf("containers = %d, want 1 (travel only): %s", len(got.Containers), out)
	}
	c := got.Containers[0]
	if c.Name != "travel" || c.Intent != "trip logistics" {
		t.Errorf("name/intent = %q/%q, want travel/trip logistics", c.Name, c.Intent)
	}
	if c.ChildCount != 2 {
		t.Errorf("child_count = %d, want 2 (direct)", c.ChildCount)
	}
	if c.DescendantCount != 3 {
		t.Errorf("descendant_count = %d, want 3 (incl. grandchild)", c.DescendantCount)
	}
	if !slices.Contains(c.ConfersTags, "travel") {
		t.Errorf("confers_tags = %v, want to include travel", c.ConfersTags)
	}
	if !slices.Equal(c.SampleChildren, []string{"flight_watch", "hotel_tracker"}) {
		t.Errorf("sample_children = %v, want [flight_watch hotel_tracker]", c.SampleChildren)
	}
}
