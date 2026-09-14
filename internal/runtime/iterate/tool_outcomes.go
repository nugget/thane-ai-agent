package iterate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// ToolOutcome tallies one tool's calls within a single [Engine.Run]. A
// service loop runs one Run per wake, so for a loop this is the wake's
// write outcome: what it attempted, what landed, and what was refused.
type ToolOutcome struct {
	// Calls counts the calls the repeat guard let through: executed
	// calls plus calls refused as unavailable.
	Calls int
	// Failures counts calls that returned an error plus calls the repeat
	// guard refused. A refused call is a rejected attempt at the same
	// work, so it counts against the tool like any other failure.
	Failures int
	// Blocked counts the calls the repeat guard refused without running
	// them. Each is also counted in Failures, never in Calls.
	Blocked int
	// Successes counts executed calls that returned without error.
	Successes int
	// LastError is the most recent execution error, clipped to
	// maxToolOutcomeErrorRunes. Guard refusals never overwrite it: the
	// error that kept the model retrying is the one worth reading.
	LastError string
	// Targets breaks the tally down by the document each call wrote, as
	// [Config.TargetKey] names it; the counts above are its sums. A tool
	// that writes one document per argument — a dossier per contact —
	// can land one target and never land another, which the sums hide.
	// Without a TargetKey there is one entry, under the empty target. A
	// call refused with [tools.ErrTargetRefused] wrote no document of its
	// own, so it is also under the empty target.
	Targets map[string]TargetOutcome
}

const (
	// maxToolOutcomeErrorRunes bounds ToolOutcome.LastError. The tally
	// travels onto loop_status rows, where a reader judges a refused write
	// from this text, so the cap keeps the shared refusal frame plus the
	// first two or three field violations while still bounding a
	// pathological error.
	maxToolOutcomeErrorRunes = 800

	// maxUnchangedKeysNamed and maxUnchangedKeyRunes bound the
	// unchanged-arguments note. Argument names are model-authored, so
	// neither their number nor their length is otherwise limited.
	maxUnchangedKeysNamed = 8
	maxUnchangedKeyRunes  = 48
)

// toolLedger is the per-Run tally behind [Result.ToolOutcomes] and the
// memory behind the unchanged-arguments note. It lives on the stack of
// one Run like every other piece of per-run engine state.
type toolLedger struct {
	// targets holds each tool's tally per target; a tool's own counts
	// are summed from it when the tally is read.
	targets targetTally
	// lastError holds each tool's most recent execution error, whichever
	// target that call wrote.
	lastError map[string]string
	// lastFailed holds, per tool and target, the most recent failed call
	// for that document. A success for the target clears the entry, so
	// the note only ever compares consecutive failures of one document,
	// however many other targets land in between.
	lastFailed map[callTarget]failedCall
	// refused holds the targets whose latest executed call the tool
	// refused as a document ([tools.ErrTargetRefused]). A guard refusal
	// of such a target repeats a refused call, so it is charged to the
	// empty target as that call was.
	refused map[callTarget]bool
}

// failedCall is what the unchanged-arguments note compares against: the
// canonical JSON of each top-level argument, and the arguments the call's
// error marked as refused ([toolargs.RejectedArguments]), sorted.
type failedCall struct {
	args     map[string]string
	rejected []string
}

func newToolLedger() *toolLedger {
	return &toolLedger{
		targets:    make(targetTally),
		lastError:  make(map[string]string),
		lastFailed: make(map[callTarget]failedCall),
		refused:    make(map[callTarget]bool),
	}
}

// blocked records a call the repeat guard refused without running,
// charged to the target the call would have written, or to the empty
// target when the tool refused that target's latest executed call as a
// document.
func (t *toolLedger) blocked(tool, target string) {
	charged := target
	if t.refused[callTarget{tool: tool, target: target}] {
		charged = ""
	}
	t.targets.update(tool, charged, func(o *TargetOutcome) {
		o.Failures++
		o.Blocked++
	})
}

// observe records one call that passed the repeat guard and returns the
// tool result the model should see. That is result unchanged, except
// when the call failed and resent a value the previous failure for the
// same target also carried, with both errors marking that argument as
// refused: then the unchanged-arguments note is appended, naming those
// arguments. Only the typed marks count ([toolargs.RejectedArguments]);
// the error text is never searched for argument names, because a store
// or network failure can mention an argument it did not refuse. A call
// refused as unavailable is tallied but never annotated — its arguments
// were not what was refused. The call is tallied under target,
// the document it wrote, unless the tool refused that document itself
// ([tools.ErrTargetRefused]): then it wrote no document of its own and is
// tallied under the empty target.
func (t *toolLedger) observe(tool, target string, args map[string]any, toolErr error, result string) string {
	key := callTarget{tool: tool, target: target}
	if toolErr == nil {
		t.targets.update(tool, target, func(o *TargetOutcome) {
			o.Calls++
			o.Successes++
		})
		delete(t.lastFailed, key)
		delete(t.refused, key)
		return result
	}
	clipped := clipRunes(toolErr.Error(), maxToolOutcomeErrorRunes)
	charged := target
	if isTargetRefusal(toolErr) {
		t.refused[key] = true
		charged = ""
	} else {
		delete(t.refused, key)
	}
	t.targets.update(tool, charged, func(o *TargetOutcome) {
		o.Calls++
		o.Failures++
		o.LastError = clipped
	})
	t.lastError[tool] = clipped

	var unavailable *tools.ErrToolUnavailable
	if errors.As(toolErr, &unavailable) {
		return result
	}
	current := failedCall{args: canonicalArgs(args), rejected: toolargs.RejectedArguments(toolErr)}
	unchanged := unchangedRejectedKeys(t.lastFailed[key], current)
	t.lastFailed[key] = current
	if len(unchanged) == 0 {
		return result
	}
	return result + "\n\n" + prompts.UnchangedArgumentsNote(tool, renderKeyList(unchanged))
}

// snapshot returns a copy of the tally, or nil when no tool was called.
func (t *toolLedger) snapshot() map[string]ToolOutcome {
	if len(t.targets) == 0 {
		return nil
	}
	out := make(map[string]ToolOutcome, len(t.targets))
	for name := range t.targets {
		out[name] = t.targets.outcome(name, t.lastError[name])
	}
	return out
}

// canonicalArgs renders each top-level argument as JSON. encoding/json
// sorts map keys, so two structurally equal values render identically
// regardless of the order the model emitted their fields in. A value
// that cannot be marshaled is left out, which makes it compare as
// changed rather than risk a false "unchanged".
func canonicalArgs(args map[string]any) map[string]string {
	out := make(map[string]string, len(args))
	for key, value := range args {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		out[key] = string(encoded)
	}
	return out
}

// unchangedRejectedKeys returns, sorted, the keys both failed calls'
// errors marked as refused and both calls sent with identical canonical
// values. An unchanged key neither error marks is a value the tool
// accepted — a valid projection resent beside the one being fixed — and
// an error with no marks at all (an unreachable backend, a failed lookup)
// is not about the values, whatever its text mentions, so neither is
// worth a note. A key refused by only one of the two errors was not
// refused twice, so it is not either.
func unchangedRejectedKeys(previous, current failedCall) []string {
	var keys []string
	for _, key := range current.rejected {
		if !slices.Contains(previous.rejected, key) {
			continue
		}
		prev, sentBefore := previous.args[key]
		value, sentNow := current.args[key]
		if sentBefore && sentNow && prev == value {
			keys = append(keys, key)
		}
	}
	return keys
}

// renderKeyList joins argument names for the note, clipping each name
// and folding any beyond maxUnchangedKeysNamed into an explicit count.
func renderKeyList(keys []string) string {
	named := keys
	if len(named) > maxUnchangedKeysNamed {
		named = named[:maxUnchangedKeysNamed]
	}
	parts := make([]string, 0, len(named))
	for _, key := range named {
		parts = append(parts, clipRunes(key, maxUnchangedKeyRunes))
	}
	list := strings.Join(parts, ", ")
	if extra := len(keys) - len(named); extra > 0 {
		list += fmt.Sprintf(" (+%d more)", extra)
	}
	return list
}

// clipRunes truncates s to at most limit runes, marking the cut.
func clipRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + "…"
}
