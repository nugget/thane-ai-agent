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

// LoopContainerView is one row of the loop_containers placement
// directory: a container loop expressed for model consumption. Field
// declaration order matches the alphabetical order the previous
// map-based output marshaled in, so the rendered JSON is byte-identical
// across the typed-schema introduction (#1173).
type LoopContainerView struct {
	// ChildCount is how many loops nest directly under this container.
	ChildCount int `json:"child_count"`
	// ConfersTags is the sorted capability tag set the container passes
	// to everything nested under it. Always non-nil so it serializes as
	// [] rather than null.
	ConfersTags []string `json:"confers_tags"`
	// DescendantCount is the transitive nested-loop count.
	DescendantCount int `json:"descendant_count"`
	// Intent is the container's declared purpose.
	Intent string `json:"intent"`
	// Name is the container's loop name — the value a new loop passes as
	// parent_name to nest here.
	Name string `json:"name"`
	// SampleChildren previews up to loopContainerSampleChildrenCap child
	// loop names; ChildCount carries the true total.
	SampleChildren []string `json:"sample_children"`
}

// loopContainersResult is the loop_containers tool's response envelope.
type loopContainersResult struct {
	Containers []LoopContainerView `json:"containers"`
	Status     string              `json:"status"`
}

func (r *Registry) handleLoopContainers(_ context.Context, _ map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	statuses := r.liveLoopRegistry.Statuses()

	// Index the graph once so per-container child/descendant rollups don't each
	// rescan the fleet (O(containers) walks over one shared index, not O(N)
	// per container).
	byID := make(map[string]looppkg.Status, len(statuses))
	for _, s := range statuses {
		byID[s.ID] = s
	}
	childrenByParent := make(map[string][]string, len(statuses))
	var containerStatuses []looppkg.Status
	for _, s := range statuses {
		if s.ParentID != "" {
			childrenByParent[s.ParentID] = append(childrenByParent[s.ParentID], s.ID)
		}
		if s.Config.Operation == looppkg.OperationContainer {
			containerStatuses = append(containerStatuses, s)
		}
	}
	sort.Slice(containerStatuses, func(i, j int) bool {
		return containerStatuses[i].Name < containerStatuses[j].Name
	})

	containers := make([]LoopContainerView, 0, len(containerStatuses))
	for _, s := range containerStatuses {
		childIDs := append([]string(nil), childrenByParent[s.ID]...)
		sort.Slice(childIDs, func(i, j int) bool {
			return byID[childIDs[i]].Name < byID[childIDs[j]].Name
		})
		sample := make([]string, 0, loopContainerSampleChildrenCap)
		for _, cid := range childIDs {
			if len(sample) >= loopContainerSampleChildrenCap {
				break
			}
			sample = append(sample, byID[cid].Name)
		}
		containers = append(containers, LoopContainerView{
			Name:            s.Name,
			Intent:          s.Config.Intent,
			ChildCount:      len(childIDs),
			DescendantCount: countDescendants(s.ID, childrenByParent),
			ConfersTags:     containerConfersTags(s),
			SampleChildren:  sample,
		})
	}
	return ldMarshalToolJSON(loopContainersResult{
		Status:     "ok",
		Containers: containers,
	})
}

// countDescendants counts the transitive children of rootID via the shared
// parent→children index, cycle-safe (seen-set), without rescanning the fleet.
func countDescendants(rootID string, childrenByParent map[string][]string) int {
	seen := map[string]struct{}{rootID: {}}
	queue := []string{rootID}
	count := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, cid := range childrenByParent[current] {
			if _, ok := seen[cid]; ok {
				continue
			}
			seen[cid] = struct{}{}
			count++
			queue = append(queue, cid)
		}
	}
	return count
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
