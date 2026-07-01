package loop

import "sort"

// LoopTreeNode is one node in the derived loop-graph tree projection: a loop
// plus its nested children, addressed by human name. It is a deliberately
// light structural view (name, operation, intent) — the flat loops[] list
// carries the per-loop detail, while the tree exists to make the parent→child
// shape legible in a single read (#1102 Tier 2).
type LoopTreeNode struct {
	Name      string         `json:"name"`
	Operation string         `json:"operation"`
	Intent    string         `json:"intent,omitempty"`
	Children  []LoopTreeNode `json:"children,omitempty"`
}

// BuildLoopTree assembles the forest of loop trees from a full status batch.
// Loops with no registered parent (parent empty, or a parent that isn't in the
// batch) become roots; every other loop nests under its parent. Siblings —
// and roots — are sorted by name for stable output. A shared seen-set prevents
// a malformed cycle from being expanded twice, and recursion is bounded at
// ancestorWalkLimit so a pathologically deep graph can't overflow the stack.
//
// maxNodes caps the number of loops emitted so the projection stays bounded on
// a large fleet (a value <= 0 means unlimited). When the cap is hit the walk
// stops adding nodes and returns truncated=true, so the caller can flag the
// result rather than emit an unbounded structure.
func BuildLoopTree(statuses []Status, maxNodes int) (nodes []LoopTreeNode, truncated bool) {
	byID := make(map[string]Status, len(statuses))
	for _, s := range statuses {
		byID[s.ID] = s
	}
	childrenByParent := make(map[string][]string, len(statuses))
	var rootIDs []string
	for _, s := range statuses {
		if _, hasParent := byID[s.ParentID]; s.ParentID != "" && hasParent {
			childrenByParent[s.ParentID] = append(childrenByParent[s.ParentID], s.ID)
			continue
		}
		rootIDs = append(rootIDs, s.ID)
	}
	sortByName := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool { return byID[ids[i]].Name < byID[ids[j]].Name })
	}

	seen := make(map[string]bool, len(statuses))
	emitted := 0
	atCap := func() bool {
		if maxNodes > 0 && emitted >= maxNodes {
			truncated = true
			return true
		}
		return false
	}

	var build func(id string, depth int) LoopTreeNode
	build = func(id string, depth int) LoopTreeNode {
		s := byID[id]
		node := LoopTreeNode{
			Name:      s.Name,
			Operation: string(effectiveOperation(s.Config.Operation)),
			Intent:    s.Config.Intent,
		}
		if depth >= ancestorWalkLimit {
			return node
		}
		kids := childrenByParent[id]
		sortByName(kids)
		for _, kid := range kids {
			if seen[kid] {
				continue
			}
			if atCap() {
				break
			}
			seen[kid] = true
			emitted++
			node.Children = append(node.Children, build(kid, depth+1))
		}
		return node
	}

	sortByName(rootIDs)
	out := make([]LoopTreeNode, 0, len(rootIDs))
	for _, id := range rootIDs {
		if seen[id] {
			continue
		}
		if atCap() {
			break
		}
		seen[id] = true
		emitted++
		out = append(out, build(id, 0))
	}
	return out, truncated
}
