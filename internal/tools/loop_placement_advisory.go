package tools

import (
	"fmt"
	"sort"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// containerTagSet pairs a container's name with the capability tags it
// declares — the input to the placement advisory's overlap check.
type containerTagSet struct {
	name string
	tags []string
}

// buildPlacementAdvisory returns a non-blocking placement suggestion (#1102
// Tier 2 — the loop-graph analog of doc_intake's recommendation + caution)
// when a tagged loop lands at the structural root yet existing containers
// declare tags it shares, so the loop most likely belongs under one of them.
// Returns nil when the loop is not at root, declares no tags, or nothing
// overlaps — the field is present only when it has something to say, and it
// never blocks creation.
func buildPlacementAdvisory(loopName, parentName string, loopTags []string, containers []containerTagSet) map[string]any {
	if !placementAtRoot(parentName) || len(loopTags) == 0 {
		return nil
	}
	want := make(map[string]bool, len(loopTags))
	for _, t := range loopTags {
		want[t] = true
	}

	candidates := make([]map[string]any, 0)
	for _, c := range containers {
		if c.name == loopName {
			// Never suggest a loop nest under itself — relevant when the loop
			// being placed is itself a container (create) or an update to an
			// existing same-named definition (lint).
			continue
		}
		var shared []string
		for _, t := range c.tags {
			if want[t] {
				shared = append(shared, t)
			}
		}
		if len(shared) == 0 {
			continue
		}
		sort.Strings(shared)
		candidates = append(candidates, map[string]any{
			"container":   c.name,
			"shared_tags": shared,
			"rationale":   fmt.Sprintf("declares %s, which this loop also has", quotedTagList(shared)),
		})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i]["container"].(string) < candidates[j]["container"].(string)
	})

	return map[string]any{
		"message": fmt.Sprintf(
			"This loop is parented to %q (the root), but %d existing container(s) declare tags it shares — consider setting parent_name to one so the loop nests under it and inherits its context.",
			looppkg.CoreLoopName, len(candidates)),
		"current_parent": looppkg.CoreLoopName,
		"candidates":     candidates,
	}
}

// placementAtRoot reports whether a loop with the given parent_name attaches to
// the structural root: an empty parent, or an explicit "core".
func placementAtRoot(parentName string) bool {
	p := strings.TrimSpace(parentName)
	return p == "" || p == looppkg.CoreLoopName
}

// quotedTagList renders tags as a readable quoted, comma-joined list.
func quotedTagList(tags []string) string {
	quoted := make([]string, len(tags))
	for i, t := range tags {
		quoted[i] = fmt.Sprintf("%q", t)
	}
	return strings.Join(quoted, ", ")
}

// livePlacementAdvisory computes the advisory for the loop-creation path from
// the live container set (the loops a live spawn can actually nest under).
// Returns nil when the live registry is unavailable or nothing applies.
func (r *Registry) livePlacementAdvisory(loopName string, loopTags []string, parentName string) map[string]any {
	if !placementAtRoot(parentName) || len(loopTags) == 0 {
		return nil
	}
	live := r.loopIntentDeps.LiveRegistry
	if live == nil {
		return nil
	}
	var containers []containerTagSet
	for _, s := range live.Statuses() {
		if s.Config.Operation != looppkg.OperationContainer || len(s.Config.Tags) == 0 {
			continue
		}
		containers = append(containers, containerTagSet{name: s.Name, tags: s.Config.Tags})
	}
	return buildPlacementAdvisory(loopName, parentName, loopTags, containers)
}

// placementAdvisoryFromView computes the advisory for the lint path from the
// stored definition set (lint is a dry-run over definitions, not live loops).
func placementAdvisoryFromView(loopName string, loopTags []string, parentName string, view *looppkg.DefinitionRegistryView) map[string]any {
	if !placementAtRoot(parentName) || len(loopTags) == 0 || view == nil {
		return nil
	}
	var containers []containerTagSet
	for _, def := range view.Definitions {
		if def.Spec.Operation != looppkg.OperationContainer || len(def.Spec.Tags) == 0 {
			continue
		}
		containers = append(containers, containerTagSet{name: def.Spec.Name, tags: def.Spec.Tags})
	}
	return buildPlacementAdvisory(loopName, parentName, loopTags, containers)
}
