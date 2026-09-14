package loop

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// contactDossierWriteTool is the registry tool that writes a contact
// dossier. It is not a loop runtime tool, so no [OutputSpec] names it,
// but a wake that tries to record what it learned about a person and
// never lands the write has lost that record exactly as surely as a
// failed output publish.
const contactDossierWriteTool = "contact_dossier_write"

// maxUnpublishedWritesNamed caps how many unpublished writes one
// loop's health clause names before folding the rest into a count.
const maxUnpublishedWritesNamed = 3

// ToolOutcome mirrors the agent engine's per-tool tally for one turn.
// The loop package keeps its own copy for the same reason [Response]
// mirrors agent.Response: it cannot import the engine.
type ToolOutcome struct {
	// Calls counts the calls the engine's repeat guard let through.
	Calls int
	// Failures counts rejected calls, repeat-guard refusals included.
	Failures int
	// Blocked counts the repeat-guard refusals within Failures.
	Blocked int
	// Successes counts calls that returned without error.
	Successes int
	// LastError is the most recent execution error, clipped.
	LastError string
	// Targets breaks the tally down by the document each call wrote, as
	// [Request.TargetKey] names it; the counts above are its sums. Nil
	// from a runner that names no targets, which reads as one empty
	// target.
	Targets map[string]TargetOutcome
}

// UnpublishedWrite is a durable write a wake attempted and never
// landed: every call it made to the tool for one target was rejected
// or refused, none succeeded, and the turn still ended normally — so
// the loop's error counters, mailbox ack, and backoff all read the wake
// as a success.
type UnpublishedWrite struct {
	// Tool is the write tool: a declared output's publish_output_,
	// replace_output_, or write_output_ tool, or contact_dossier_write.
	Tool string `json:"tool"`
	// Target is the document the calls wrote: the canonical contact_id
	// for contact_dossier_write. It is empty for a declared output's
	// tool, which writes one document, and for dossier calls that wrote
	// no dossier of their own, whose entry any landed dossier clears.
	Target string `json:"target,omitempty"`
	// Failures counts that wake's rejected calls to Tool for Target,
	// repeat-guard refusals included.
	Failures int `json:"failures"`
	// LastError is that wake's last rejection text for Target, clipped
	// by the engine.
	LastError string `json:"last_error,omitempty"`
	// At is when the wake ended.
	At time.Time `json:"at"`
	// ConversationID is the wake's conversation, the key for its tool
	// calls and log lines.
	ConversationID string `json:"conversation_id,omitempty"`
}

// UnpublishedWriteView is the model-facing projection of one
// [UnpublishedWrite] on a loop_status row: the same fact with its time
// as a delta, so the model reads age without doing timestamp math.
type UnpublishedWriteView struct {
	Tool           string `json:"tool"`
	Target         string `json:"target,omitempty"`
	Failures       int    `json:"failures"`
	LastError      string `json:"last_error,omitempty"`
	AtDelta        string `json:"at_delta"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// writeOutcomeState is a loop's record of unpublished writes, guarded
// by the loop's mu.
type writeOutcomeState struct {
	// latest is the most recent unpublished wake per write tool and
	// target, at most maxUnpublishedWriteEntries of them.
	latest map[writeKey]UnpublishedWrite
	// wakes counts wakes since start with at least one unpublished write.
	wakes int
}

// wakeWriteTally is one wake's durable-write outcome, reported on the
// iteration_complete event.
type wakeWriteTally struct {
	// rejections sums the wake's failed calls across durable write tools.
	rejections int
	// unpublished lists the writes the wake never landed, one per tool
	// and target.
	unpublished []UnpublishedWrite
}

// durableWriteTools is the set of tools whose calls a wake exists to
// land: the generated tool of every declared output, plus
// contact_dossier_write.
func durableWriteTools(outputs []OutputSpec) map[string]bool {
	set := map[string]bool{contactDossierWriteTool: true}
	for _, out := range outputs {
		set[out.ToolName()] = true
	}
	return set
}

// observe folds one completed wake's tool tally into the record, per
// durable write tool and target. A target with failures and no success
// in the wake is unpublished and replaces that tool and target's entry.
// A success for that target clears it; a success for another target of
// the same tool does not, because that is a different document. A wake
// that never writes the target leaves its entry standing, because the
// write is still unpublished.
//
// The empty target is judged per tool instead: it holds a declared
// output's calls, which all write its one document, and a dossier's
// calls that wrote no dossier of their own (a contact_id that spells no
// contact, or one the tool refused as the wrong contact). Any success of
// the tool lands it, so it is unpublished only in a wake where the tool
// landed nothing, and any later landed write clears it.
//
// Entries for tools that are no longer durable (a retune removed the
// output) are dropped rather than left to hold the loop degraded until
// restart, and past maxUnpublishedWriteEntries the oldest entries are
// dropped.
func (s *writeOutcomeState) observe(outcomes map[string]ToolOutcome, durable map[string]bool, convID string, at time.Time) wakeWriteTally {
	for key := range s.latest {
		if !durable[key.tool] {
			delete(s.latest, key)
		}
	}
	var tally wakeWriteTally
	for tool, o := range outcomes {
		if !durable[tool] {
			continue
		}
		tally.rejections += o.Failures
		targets := targetsOf(o)
		landed := landedAny(targets)
		if landed {
			delete(s.latest, writeKey{tool: tool})
		}
		for target, t := range targets {
			key := writeKey{tool: tool, target: target}
			switch {
			case t.Successes > 0:
				delete(s.latest, key)
			case target == "" && landed:
				// Cleared above: the tool landed a write this wake.
			case t.Failures > 0:
				w := UnpublishedWrite{
					Tool:           tool,
					Target:         target,
					Failures:       t.Failures,
					LastError:      t.LastError,
					At:             at,
					ConversationID: convID,
				}
				if s.latest == nil {
					s.latest = make(map[writeKey]UnpublishedWrite)
				}
				s.latest[key] = w
				tally.unpublished = append(tally.unpublished, w)
			}
		}
	}
	sortByKey(tally.unpublished)
	s.evictOldest()
	if len(tally.unpublished) > 0 {
		s.wakes++
	}
	return tally
}

// snapshot returns the current entries sorted by tool, then target, or
// nil.
func (s *writeOutcomeState) snapshot() []UnpublishedWrite {
	if len(s.latest) == 0 {
		return nil
	}
	out := make([]UnpublishedWrite, 0, len(s.latest))
	for _, w := range s.latest {
		out = append(out, w)
	}
	sortByKey(out)
	return out
}

// recordWriteOutcomes folds a completed wake's tool tally into the
// loop's unpublished-write record, logs each write the wake never
// landed, and returns the wake's counts for the iteration_complete
// event. Detection only: it leaves consecutive errors, the mailbox ack,
// backoff, and envelope delivery exactly as a successful wake has them.
//
// Only completed wakes reach it. A runner error returns no tally, so a
// wake that landed its write and then failed on a later model call
// leaves an older entry standing until a later completed wake lands the
// write; its error already shows in consecutive errors.
func (l *Loop) recordWriteOutcomes(log *slog.Logger, outcomes map[string]ToolOutcome, convID string) wakeWriteTally {
	l.mu.Lock()
	tally := l.writes.observe(outcomes, durableWriteTools(l.config.Outputs), convID, time.Now())
	l.mu.Unlock()
	for _, w := range tally.unpublished {
		log.Warn("loop wake ended without landing a durable write",
			"tool", w.Tool,
			"target", w.Target,
			"failures", w.Failures,
			"last_error", w.LastError,
		)
	}
	return tally
}

// DegradedReason says why a loop reads as degraded, or "" when it does
// not. It is the one definition behind both loop_status's health rollup
// and the system_health loop census, so the two can never disagree
// about which loops are degraded or why.
func DegradedReason(s Status, now time.Time) string {
	var reasons []string
	switch {
	case s.ConsecutiveErrors > 0:
		reasons = append(reasons, fmt.Sprintf("%d consecutive errors", s.ConsecutiveErrors))
	case s.State == StateError:
		// Errored this cycle but the counter already reset (or never
		// incremented) — label the state, not a misleading "0 errors".
		reasons = append(reasons, "error")
	}
	if clause := UnpublishedWriteClause(s, now); clause != "" {
		reasons = append(reasons, clause)
	}
	return strings.Join(reasons, "; ")
}

// UnpublishedWriteClause renders a loop's uncleared unpublished writes
// as one short clause, newest first, or "" when there are none — for
// example "wake at -2h ended without publishing
// publish_output_trip_planner after 16 rejections", or, for a dossier,
// "wake at -2h ended without publishing contact_dossier_write for
// contact <uuid> after 7 rejections". It names at most
// maxUnpublishedWritesNamed writes; loop_status carries each write's
// last error for the drill-down.
func UnpublishedWriteClause(s Status, now time.Time) string {
	if len(s.UnpublishedWrites) == 0 {
		return ""
	}
	writes := append([]UnpublishedWrite(nil), s.UnpublishedWrites...)
	sort.SliceStable(writes, func(i, j int) bool {
		if !writes[i].At.Equal(writes[j].At) {
			return writes[i].At.After(writes[j].At)
		}
		return keyOf(writes[i]).less(keyOf(writes[j]))
	})
	named := writes
	if len(named) > maxUnpublishedWritesNamed {
		named = named[:maxUnpublishedWritesNamed]
	}
	parts := make([]string, 0, len(named))
	for _, w := range named {
		noun := "rejections"
		if w.Failures == 1 {
			noun = "rejection"
		}
		parts = append(parts, fmt.Sprintf("wake at %s ended without publishing %s%s after %d %s",
			promptfmt.FormatDeltaOnly(w.At, now), w.Tool, targetPhrase(w), w.Failures, noun))
	}
	clause := strings.Join(parts, "; ")
	if extra := len(writes) - len(named); extra > 0 {
		clause += fmt.Sprintf(" (+%d more)", extra)
	}
	return clause
}

// unpublishedWriteViews projects a loop's unpublished writes for its
// loop_status row, or nil when there are none.
func unpublishedWriteViews(writes []UnpublishedWrite, now time.Time) []UnpublishedWriteView {
	if len(writes) == 0 {
		return nil
	}
	out := make([]UnpublishedWriteView, 0, len(writes))
	for _, w := range writes {
		out = append(out, UnpublishedWriteView{
			Tool:           w.Tool,
			Target:         w.Target,
			Failures:       w.Failures,
			LastError:      w.LastError,
			AtDelta:        promptfmt.FormatDeltaOnly(w.At, now),
			ConversationID: w.ConversationID,
		})
	}
	return out
}

// cloneToolOutcomes copies a tool tally, per-target breakdowns
// included, so a retained response or iteration result never aliases
// the runner's maps.
func cloneToolOutcomes(src map[string]ToolOutcome) map[string]ToolOutcome {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]ToolOutcome, len(src))
	for name, o := range src {
		o.Targets = cloneTargetOutcomes(o.Targets)
		out[name] = o
	}
	return out
}
