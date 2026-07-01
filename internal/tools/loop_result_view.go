package tools

import (
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// This file provides the canonical-LoopView projection helpers the model-facing
// loop result surfaces share (#1106 B2), so spawn_loop / stop_loop and the
// definition mutation tools all emit the same "ps auxwwww" row that loop_status
// and loop_definition_get do, rather than a bespoke per-tool shape.

// loopViewResolver builds a LoopView resolver over the given live status batch
// (so parent_name / ancestry / child_count resolve) joined with the definition
// policy/eligibility, capturing one clock for all delta strings.
func (r *Registry) loopViewResolver(statuses []looppkg.Status) looppkg.LoopViewResolver {
	return looppkg.NewLoopViewResolver(statuses, r.loopPolicyByName(), time.Now())
}

// loopViewByID projects the currently-live loop with the given id into its
// canonical LoopView, returning ok=false when no live loop matches.
func (r *Registry) loopViewByID(id string) (looppkg.LoopView, bool) {
	if r.liveLoopRegistry == nil {
		return looppkg.LoopView{}, false
	}
	statuses := r.liveLoopRegistry.Statuses()
	resolver := r.loopViewResolver(statuses)
	for _, s := range statuses {
		if s.ID == id {
			return resolver.FromStatus(s), true
		}
	}
	return looppkg.LoopView{}, false
}

// loopViewFromStatus projects an already-captured Status — e.g. the pre-stop
// snapshot stop_loop holds, or a launch's final status — into its canonical
// LoopView, resolving the graph against the current live batch.
//
// The captured loop is often no longer in the live batch (stop_loop captures it
// before stopping; a synchronous spawn's loop has finished), so we add the
// status to the batch before building the resolver. ancestry/child_count are
// keyed by loop ID, so without its own entry the resolver would report an empty
// ancestry even when ParentID is known. Guard against double-indexing — which
// would over-count the parent's child_count — for the case where the loop is
// still live.
func (r *Registry) loopViewFromStatus(s looppkg.Status) looppkg.LoopView {
	var statuses []looppkg.Status
	if r.liveLoopRegistry != nil {
		statuses = r.liveLoopRegistry.Statuses()
	}
	present := false
	for _, existing := range statuses {
		if existing.ID == s.ID {
			present = true
			break
		}
	}
	if !present {
		statuses = append(statuses, s)
	}
	return r.loopViewResolver(statuses).FromStatus(s)
}
