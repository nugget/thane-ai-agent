package loop

import (
	"fmt"
	"strings"
)

// SelfContextMarkdown renders this loop's canonical view as the compact,
// always-on "self-context" block a running loop sees each iteration (#1106 B3):
// who it is, where it sits in the graph, why it exists, its live cadence and
// health, and what capability tags it inherited — so the loop is self-aware
// without a loop_status tool call. Absent fields are omitted so the block stays
// tight; a zero view renders "".
func (v LoopView) SelfContextMarkdown() string {
	if v.Name == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("### This loop\n")

	// Identity: name (short id) · operation · state · eligibility.
	line := v.Name
	if v.ID != nil && *v.ID != "" {
		line += " (" + shortLoopID(*v.ID) + ")"
	}
	line += " · " + v.Operation
	if v.State != nil && *v.State != "" {
		line += " · " + *v.State
	}
	if v.Eligible {
		line += " · eligible"
	} else {
		line += " · ineligible"
	}
	b.WriteString(line + "\n")

	// Structure: graph position (leaf-adjacent first) + child count if any.
	if chain := ancestryChain(v.ParentName, v.Ancestry); chain != "" {
		b.WriteString("parent: " + chain)
		if v.ChildCount > 0 {
			b.WriteString(fmt.Sprintf("  (%d children)", v.ChildCount))
		}
		b.WriteString("\n")
	}

	// Purpose.
	if v.Intent != "" {
		b.WriteString("intent: " + v.Intent + "\n")
	}

	// Live cadence & health — only the facts a self-pacing loop acts on.
	if cadence := selfCadenceLine(v); cadence != "" {
		b.WriteString(cadence + "\n")
	}

	// Inherited capability tags with provenance.
	if len(v.EffectiveTags) > 0 {
		b.WriteString("effective tags: " + renderSelfEffectiveTags(v.EffectiveTags) + "\n")
	}

	return b.String()
}

// shortLoopID trims a UUID-style loop id to its first segment for legibility.
func shortLoopID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ancestryChain renders the loop's graph position leaf-adjacent first
// ("watchers ← core"). Ancestry is ordered root→leaf, so it is reversed here;
// ParentName is the fallback when the ancestry list is empty.
func ancestryChain(parentName *string, ancestry []string) string {
	if len(ancestry) > 0 {
		rev := make([]string, len(ancestry))
		for i, n := range ancestry {
			rev[len(ancestry)-1-i] = n
		}
		return strings.Join(rev, " ← ")
	}
	if parentName != nil && *parentName != "" {
		return *parentName
	}
	return ""
}

func selfCadenceLine(v LoopView) string {
	var parts []string
	if v.Iterations != nil {
		parts = append(parts, fmt.Sprintf("iteration %d", *v.Iterations))
	}
	if v.NextWakeDelta != nil {
		parts = append(parts, "next wake "+*v.NextWakeDelta)
	}
	if v.ConsecutiveErrors != nil {
		parts = append(parts, fmt.Sprintf("consecutive_errors %d", *v.ConsecutiveErrors))
	}
	return strings.Join(parts, " · ")
}

func renderSelfEffectiveTags(tags []EffectiveTag) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.From == "" || t.From == EffectiveOriginSelf {
			parts = append(parts, t.Tag+" (self)")
		} else {
			parts = append(parts, t.Tag+" (←"+t.From+")")
		}
	}
	return strings.Join(parts, ", ")
}
