package iterate

import (
	"errors"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TargetOutcome is one target's share of a [ToolOutcome]: the calls in
// one Run that wrote the same document, as [Config.TargetKey] names it.
// Its fields count exactly as the tool's do, and each of the tool's
// counts is the sum of its targets' counts.
type TargetOutcome struct {
	Calls     int
	Failures  int
	Blocked   int
	Successes int
	// LastError is this target's most recent execution error, clipped
	// like [ToolOutcome.LastError].
	LastError string
}

// targetOf returns the document a call writes, or "" when the run sets
// no [Config.TargetKey].
func (c *Config) targetOf(tool string, args map[string]any) string {
	if c.TargetKey == nil {
		return ""
	}
	return c.TargetKey(tool, args)
}

// callTarget is one tool and one target: the key the ledger remembers a
// document's last failed call and its refusal by.
type callTarget struct {
	tool   string
	target string
}

// isTargetRefusal reports whether a failed call was refused for the
// document it named rather than for what it carried.
func isTargetRefusal(err error) bool {
	var refused *tools.ErrTargetRefused
	return errors.As(err, &refused)
}

// targetTally holds, per tool, each target's [TargetOutcome].
type targetTally map[string]map[string]TargetOutcome

// update applies fn to one tool and target's entry, creating it when
// this is the first call for that tool and target.
func (t targetTally) update(tool, target string, fn func(*TargetOutcome)) {
	byTarget := t[tool]
	if byTarget == nil {
		byTarget = make(map[string]TargetOutcome)
		t[tool] = byTarget
	}
	o := byTarget[target]
	fn(&o)
	byTarget[target] = o
}

// outcome sums one tool's targets into its [ToolOutcome]. It copies
// the per-target map, so a returned outcome never aliases the ledger.
func (t targetTally) outcome(tool, lastError string) ToolOutcome {
	byTarget := t[tool]
	o := ToolOutcome{LastError: lastError, Targets: make(map[string]TargetOutcome, len(byTarget))}
	for target, to := range byTarget {
		o.Calls += to.Calls
		o.Failures += to.Failures
		o.Blocked += to.Blocked
		o.Successes += to.Successes
		o.Targets[target] = to
	}
	return o
}
