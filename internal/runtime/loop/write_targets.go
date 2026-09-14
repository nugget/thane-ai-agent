package loop

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// maxUnpublishedWriteEntries bounds how many unpublished writes one loop
// holds. A wake refused for many contacts records one entry per contact,
// so without a bound a bad run would grow the record, and the loop's
// loop_status row, without limit. The oldest entries go first.
const maxUnpublishedWriteEntries = 16

// TargetOutcome mirrors the agent engine's per-target tally: one
// document's share of a [ToolOutcome].
type TargetOutcome struct {
	Calls     int
	Failures  int
	Blocked   int
	Successes int
	LastError string
}

// writeTarget names the document a tool call writes. It is the
// [Request.TargetKey] of every turn the loop prepares. A declared
// output's tool writes exactly one document, so its target is empty.
// contact_dossier_write writes one dossier per contact, so its target is
// the contact its contact_id spells, in canonical form: the tool refuses
// a UUID sent upper-case, braced, or without hyphens, and the canonical
// retry that lands writes the same contact's dossier. A contact_id that
// spells no contact — missing, not a string, not a UUID, or the nil
// UUID — is the empty target, because the call wrote no dossier of its
// own; see [writeOutcomeState.observe].
func writeTarget(tool string, args map[string]any) string {
	if tool != contactDossierWriteTool {
		return ""
	}
	raw, _ := args["contact_id"].(string)
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || id == uuid.Nil {
		return ""
	}
	return id.String()
}

// writeKey identifies one unpublished write: a durable write tool and
// the document it targets.
type writeKey struct {
	tool   string
	target string
}

func keyOf(w UnpublishedWrite) writeKey {
	return writeKey{tool: w.Tool, target: w.Target}
}

// less orders keys by tool, then target.
func (k writeKey) less(other writeKey) bool {
	if k.tool != other.tool {
		return k.tool < other.tool
	}
	return k.target < other.target
}

// sortByKey orders writes by tool, then target.
func sortByKey(writes []UnpublishedWrite) {
	sort.Slice(writes, func(i, j int) bool { return keyOf(writes[i]).less(keyOf(writes[j])) })
}

// targetsOf returns a tool outcome's per-target breakdown. An outcome
// without one, from a runner that names no targets, reads as a single
// empty target carrying the tool's own counts: the per-tool reading.
func targetsOf(o ToolOutcome) map[string]TargetOutcome {
	if len(o.Targets) > 0 {
		return o.Targets
	}
	return map[string]TargetOutcome{"": {
		Calls: o.Calls, Failures: o.Failures, Blocked: o.Blocked,
		Successes: o.Successes, LastError: o.LastError,
	}}
}

// landedAny reports whether any target in a tool's breakdown landed.
func landedAny(targets map[string]TargetOutcome) bool {
	for _, t := range targets {
		if t.Successes > 0 {
			return true
		}
	}
	return false
}

// evictOldest drops the oldest entries until at most
// maxUnpublishedWriteEntries remain. The entries one wake records share
// its time, so ties drop in key order, which keeps the choice
// deterministic.
func (s *writeOutcomeState) evictOldest() {
	excess := len(s.latest) - maxUnpublishedWriteEntries
	if excess <= 0 {
		return
	}
	entries := make([]UnpublishedWrite, 0, len(s.latest))
	for _, w := range s.latest {
		entries = append(entries, w)
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].At.Equal(entries[j].At) {
			return entries[i].At.Before(entries[j].At)
		}
		return keyOf(entries[i]).less(keyOf(entries[j]))
	})
	for _, w := range entries[:excess] {
		delete(s.latest, keyOf(w))
	}
}

// targetPhrase names an unpublished write's target for the health
// clause: " for contact <id>" for a dossier, " for <target>" for any
// other targeted tool, and "" for the empty target.
func targetPhrase(w UnpublishedWrite) string {
	switch {
	case w.Target == "":
		return ""
	case w.Tool == contactDossierWriteTool:
		return " for contact " + w.Target
	default:
		return " for " + w.Target
	}
}

// cloneTargetOutcomes copies a per-target breakdown so a retained
// response or iteration result never aliases the runner's map.
func cloneTargetOutcomes(src map[string]TargetOutcome) map[string]TargetOutcome {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]TargetOutcome, len(src))
	for target, o := range src {
		out[target] = o
	}
	return out
}
