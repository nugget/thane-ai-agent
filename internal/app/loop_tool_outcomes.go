package app

import (
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// loopToolOutcomes converts the agent engine's per-tool tally into the
// loop package's mirror, which cannot import the engine. The loop
// runtime reads it to notice a wake that ended without landing one of
// its durable writes.
func loopToolOutcomes(src map[string]iterate.ToolOutcome) map[string]looppkg.ToolOutcome {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]looppkg.ToolOutcome, len(src))
	for name, o := range src {
		out[name] = looppkg.ToolOutcome{
			Calls:     o.Calls,
			Failures:  o.Failures,
			Blocked:   o.Blocked,
			Successes: o.Successes,
			LastError: o.LastError,
			Targets:   loopTargetOutcomes(o.Targets),
		}
	}
	return out
}

// loopTargetOutcomes converts one tool's per-target breakdown, which
// the loop runtime reads to judge each document a wake wrote on its own.
func loopTargetOutcomes(src map[string]iterate.TargetOutcome) map[string]looppkg.TargetOutcome {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]looppkg.TargetOutcome, len(src))
	for target, o := range src {
		out[target] = looppkg.TargetOutcome{
			Calls:     o.Calls,
			Failures:  o.Failures,
			Blocked:   o.Blocked,
			Successes: o.Successes,
			LastError: o.LastError,
		}
	}
	return out
}
