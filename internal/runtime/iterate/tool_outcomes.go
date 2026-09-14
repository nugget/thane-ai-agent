package iterate

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/tools"
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
	outcomes map[string]ToolOutcome
	// lastFailed holds, per tool, that tool's most recent failed call. A
	// success clears the entry, so the note only ever compares
	// consecutive failures of one tool.
	lastFailed map[string]failedCall
}

// failedCall is what the unchanged-arguments note compares against: the
// canonical JSON of each top-level argument, and the error text.
type failedCall struct {
	args    map[string]string
	errText string
}

func newToolLedger() *toolLedger {
	return &toolLedger{
		outcomes:   make(map[string]ToolOutcome),
		lastFailed: make(map[string]failedCall),
	}
}

// blocked records a call the repeat guard refused without running.
func (t *toolLedger) blocked(tool string) {
	o := t.outcomes[tool]
	o.Failures++
	o.Blocked++
	t.outcomes[tool] = o
}

// observe records one call that passed the repeat guard and returns the
// tool result the model should see. That is result unchanged, except
// when the call failed and resent a value the tool's previous failure
// also carried, with both errors naming that argument: then the
// unchanged-arguments note is appended, naming those arguments. A call
// refused as unavailable is tallied but never annotated — its arguments
// were not what was refused.
func (t *toolLedger) observe(tool string, args map[string]any, toolErr error, result string) string {
	o := t.outcomes[tool]
	o.Calls++
	if toolErr == nil {
		o.Successes++
		t.outcomes[tool] = o
		delete(t.lastFailed, tool)
		return result
	}
	errText := toolErr.Error()
	o.Failures++
	o.LastError = clipRunes(errText, maxToolOutcomeErrorRunes)
	t.outcomes[tool] = o

	var unavailable *tools.ErrToolUnavailable
	if errors.As(toolErr, &unavailable) {
		return result
	}
	current := failedCall{args: canonicalArgs(args), errText: errText}
	unchanged := unchangedNamedKeys(t.lastFailed[tool], current)
	t.lastFailed[tool] = current
	if len(unchanged) == 0 {
		return result
	}
	return result + "\n\n" + prompts.UnchangedArgumentsNote(tool, renderKeyList(unchanged))
}

// snapshot returns a copy of the tally, or nil when no tool was called.
func (t *toolLedger) snapshot() map[string]ToolOutcome {
	if len(t.outcomes) == 0 {
		return nil
	}
	out := make(map[string]ToolOutcome, len(t.outcomes))
	for name, o := range t.outcomes {
		out[name] = o
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

// unchangedNamedKeys returns, sorted, the keys the two failed calls sent
// with identical canonical values and that both calls' errors name. An
// unchanged key neither error mentions is a value the tool accepted — a
// valid projection resent beside the one being fixed — and an error that
// names no argument at all (an unreachable backend, a timeout) is not
// about the values, so neither is worth a note.
func unchangedNamedKeys(previous, current failedCall) []string {
	if len(previous.args) == 0 {
		return nil
	}
	var keys []string
	for key, value := range current.args {
		if prev, ok := previous.args[key]; !ok || prev != value {
			continue
		}
		if namesKey(previous.errText, key) && namesKey(current.errText, key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// namesKey reports whether text mentions key as a whole identifier, so
// "full" matches "full is 98305 bytes" but not "fully" or "full_text".
func namesKey(text, key string) bool {
	if key == "" {
		return false
	}
	for from := 0; ; {
		idx := strings.Index(text[from:], key)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(key)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if !isIdentRune(before) && !isIdentRune(after) {
			return true
		}
		from = start + 1
	}
}

// isIdentRune reports whether r can continue an argument name. The
// utf8.RuneError that decoding returns at either end of the text is not.
func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
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
