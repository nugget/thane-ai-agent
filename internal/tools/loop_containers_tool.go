package tools

import (
	"context"
	"fmt"
	"sort"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// loopContainerSampleChildrenCap bounds the sample_children preview per
// container so the directory stays compact for large fleets; child_count and
// descendant_count still report the true totals.
const loopContainerSampleChildrenCap = 8

// registerLoopContainers registers the loop_containers placement directory —
// the loop-graph analog of doc_roots (#1102 Tier 2). It shares the live
// registry dependency wired by ConfigureLoopRuntimeTools.
func (r *Registry) registerLoopContainers() {
	if r.liveLoopRegistry == nil {
		return
	}
	r.Register(&Tool{
		Name:        "loop_containers",
		Description: "List the container loops — the grouping nodes that new loops nest under — as a placement directory (the loop-graph analog of doc_roots). Each entry carries the container's intent, how many loops it holds directly (child_count) and transitively (descendant_count), the capability tags it confers to everything nested under it (confers_tags), and a sample of its children by name. Use this before creating a loop to decide where it belongs: pick the container whose confers_tags and intent match the new loop's purpose, then pass its name as parent_name to thane_loop_create.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: r.handleLoopContainers,
	})
}

func (r *Registry) handleLoopContainers(_ context.Context, _ map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	// Collect and sort the container statuses by name up front so the rows
	// emit in a stable order without post-sorting an untyped map slice.
	containerStatuses := make([]looppkg.Status, 0)
	for _, s := range r.liveLoopRegistry.Statuses() {
		if s.Config.Operation == looppkg.OperationContainer {
			containerStatuses = append(containerStatuses, s)
		}
	}
	sort.Slice(containerStatuses, func(i, j int) bool {
		return containerStatuses[i].Name < containerStatuses[j].Name
	})

	containers := make([]map[string]any, 0, len(containerStatuses))
	for _, s := range containerStatuses {
		children := r.liveLoopRegistry.Children(s.ID)
		sample := make([]string, 0, loopContainerSampleChildrenCap)
		for _, c := range children {
			if len(sample) >= loopContainerSampleChildrenCap {
				break
			}
			sample = append(sample, c.Name())
		}
		containers = append(containers, map[string]any{
			"name":             s.Name,
			"intent":           s.Config.Intent,
			"child_count":      len(children),
			"descendant_count": len(r.liveLoopRegistry.Descendants(s.ID)),
			"confers_tags":     containerConfersTags(s),
			"sample_children":  sample,
		})
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"containers": containers,
	})
}

// containerConfersTags returns the sorted, deduped capability tags a container
// passes to everything nested under it: its effective tag set (own declared
// tags plus any inherited from its own ancestors), falling back to the
// declared Tags when live effective state has not been computed. Always
// non-nil so the field serializes as [] rather than null.
func containerConfersTags(s looppkg.Status) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(tag string) {
		if tag == "" || seen[tag] {
			return
		}
		seen[tag] = true
		out = append(out, tag)
	}
	for _, t := range s.EffectiveTags {
		add(t.Tag)
	}
	if len(out) == 0 {
		for _, t := range s.Config.Tags {
			add(t)
		}
	}
	sort.Strings(out)
	return out
}
