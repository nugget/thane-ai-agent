package llm

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// WireMarkup names the tool-call grammar [ScanWire] found in model
// text. The zero value means no tool-call markup was found.
type WireMarkup string

const (
	// WireMarkupQwenXML is the Qwen3-coder XML grammar:
	// <tool_call><function=NAME>(<parameter=KEY>VALUE</parameter>)*</function></tool_call>.
	// The trailing </function> and </tool_call> may be missing when
	// the text ends, because runners can consume them as stop tokens.
	WireMarkupQwenXML WireMarkup = "qwen_xml"

	// WireMarkupJSONCall is a JSON object carrying "name" and
	// "arguments" keys, whether it is the whole text, fenced, wrapped in
	// <tool_call>, inside an array, or embedded after prose.
	WireMarkupJSONCall WireMarkup = "json_call"

	// WireMarkupHarmony is the gpt-oss Harmony recipient form,
	// to=functions.NAME followed by a JSON argument object, either with
	// its special tokens (<|channel|>, <|constrain|>, <|message|>) or
	// with them stripped by the runner.
	WireMarkupHarmony WireMarkup = "harmony"
)

// WireLocation names the response channel a [WireScan] finding came
// from.
type WireLocation string

const (
	// WireLocationContent is the assistant's content channel, the text
	// a client would see.
	WireLocationContent WireLocation = "content"

	// WireLocationReasoning is the model's thinking channel, as the
	// runner's reasoning parser separated it from content.
	WireLocationReasoning WireLocation = "reasoning"
)

// WireScan describes the tool-call markup, if any, that a model left
// in its text channels instead of the provider's structured tool-call
// field. It is detection only: it never executes, recovers or rewrites
// anything, and it carries no model text, so it is safe to log and to
// persist.
//
// When a channel holds more than one call, the scan describes the
// first well-formed call, or the first markup found when none is
// well-formed.
type WireScan struct {
	// Markup is the grammar of the described call. It is empty when
	// neither channel holds tool-call markup, and every other field is
	// then zero.
	Markup WireMarkup `json:"markup,omitempty"`

	// Location is the channel the described call came from.
	Location WireLocation `json:"location,omitempty"`

	// WellFormed reports that the call parsed completely: a non-empty
	// function name and arguments a runtime could execute as-is. For
	// json_call and harmony the arguments are a JSON object, or for
	// json_call JSON null, which means no arguments and runs as {}. For
	// qwen_xml they are zero or more closed parameters and nothing else
	// in the function body. Truncated markup, unparseable JSON and
	// stringified arguments are not well-formed.
	WellFormed bool `json:"well_formed"`

	// NameOffered reports that the call's function name is one of the
	// tool names offered on the request. It is false when no tools were
	// offered or the name could not be read.
	NameOffered bool `json:"name_offered"`

	// AfterProse reports that non-whitespace text precedes the call in
	// its channel. A code fence, <tool_call> tag, array bracket or
	// Harmony header wrapping the call does not count as prose.
	AfterProse bool `json:"after_prose"`

	// StringifiedArgs reports a json_call whose "arguments" value is a
	// JSON string rather than an object. Such a call is never
	// WellFormed.
	StringifiedArgs bool `json:"stringified_args"`
}

// Found reports whether the scan found tool-call markup in either
// channel.
func (s WireScan) Found() bool { return s.Markup != "" }

// ScanWire looks for tool-call markup in a response's content and
// reasoning, as the provider returned them and before any text
// fallback rewrites the content. offered is the list of tool names the
// request offered; it only decides [WireScan.NameOffered].
//
// Content takes precedence: reasoning is scanned only when content
// holds no markup at all, so a malformed call in content hides a
// well-formed one in reasoning. Within a channel the first well-formed
// call is described, or the first markup when none is well-formed.
func ScanWire(content, reasoning string, offered []string) WireScan {
	if f, ok := scanWireText(content); ok {
		return f.scan(content, WireLocationContent, offered)
	}
	if f, ok := scanWireText(reasoning); ok {
		return f.scan(reasoning, WireLocationReasoning, offered)
	}
	return WireScan{}
}

// maxWireCandidates bounds how many candidate calls each detector
// examines in one text. Every candidate costs a parse over its span,
// so an adversarial or runaway text with thousands of braces or
// recipients would otherwise make a detection pass quadratic. Real
// replies carry a handful of calls.
const maxWireCandidates = 16

// wireFinding is one detector's view of a call before it is reduced to
// a [WireScan]. start is where the call begins, wrappers included, so
// AfterProse can be judged from the text before it.
type wireFinding struct {
	markup          WireMarkup
	start           int
	name            string
	wellFormed      bool
	stringifiedArgs bool
}

func (f wireFinding) scan(text string, loc WireLocation, offered []string) WireScan {
	return WireScan{
		Markup:          f.markup,
		Location:        loc,
		WellFormed:      f.wellFormed,
		NameOffered:     f.name != "" && slices.Contains(offered, f.name),
		AfterProse:      strings.TrimSpace(text[:f.start]) != "",
		StringifiedArgs: f.stringifiedArgs,
	}
}

// betterThan orders findings: well-formed beats malformed, then the
// earlier call wins.
func (f wireFinding) betterThan(other wireFinding) bool {
	if f.wellFormed != other.wellFormed {
		return f.wellFormed
	}
	return f.start < other.start
}

func scanWireText(text string) (wireFinding, bool) {
	if strings.TrimSpace(text) == "" {
		return wireFinding{}, false
	}
	var best wireFinding
	found := false
	for _, detect := range []func(string) (wireFinding, bool){qwenXMLFinding, jsonCallFinding, harmonyFinding} {
		f, ok := detect(text)
		if ok && (!found || f.betterThan(best)) {
			best, found = f, true
		}
	}
	return best, found
}

// jsonWrapperTokens are the wrappers a model puts around a JSON or XML
// call. They belong to the markup, not to the prose before it.
var jsonWrapperTokens = []string{"<tool_call>", "```json", "```tool", "```xml", "```", "["}

// harmonyHeaderTokens are the Harmony header pieces that precede a
// recipient, tokenised or with the special tokens stripped.
var harmonyHeaderTokens = []string{"<|start|>", "<|channel|>", "assistant", "commentary"}

// markupStart walks back from start over whitespace and wrapper tokens
// and returns where the markup, wrappers included, begins.
func markupStart(text string, start int, tokens []string) int {
	for {
		trimmed := strings.TrimRight(text[:start], " \t\r\n")
		moved := false
		for _, tok := range tokens {
			if strings.HasSuffix(trimmed, tok) {
				start = len(trimmed) - len(tok)
				moved = true
				break
			}
		}
		if !moved {
			return start
		}
	}
}

// jsonCallFinding finds JSON objects carrying "name" and "arguments"
// anywhere in text: closed objects first, then an unclosed object cut
// off at the end of the text.
func jsonCallFinding(text string) (wireFinding, bool) {
	nameKeys := jsonNameKeyOffsets(text)
	if len(nameKeys) == 0 {
		return wireFinding{}, false
	}
	closed, unclosed := braceSpans(text)
	var best wireFinding
	found := false
	skipUntil, attempts := -1, 0
	for _, sp := range closed {
		if sp.start < skipUntil || !anyOffsetIn(nameKeys, sp.start, sp.end) {
			continue
		}
		if attempts++; attempts > maxWireCandidates {
			break
		}
		f, ok := classifyJSONCallObject(text[sp.start:sp.end])
		if !ok {
			continue
		}
		f.start = markupStart(text, sp.start, jsonWrapperTokens)
		if f.wellFormed {
			return f, true
		}
		if !found {
			best, found = f, true
		}
		// Objects nested in a call-shaped object are its arguments,
		// not further calls.
		skipUntil = sp.end
	}
	// A cut-off call is the outermost object still open at the end of
	// the text whose own top level reads as a call. Objects nested in it
	// are its arguments, which often carry a "name" of their own.
	for _, o := range unclosed {
		if !o.callShaped() {
			continue
		}
		f := wireFinding{
			markup: WireMarkupJSONCall,
			start:  markupStart(text, o.start, jsonWrapperTokens),
			name:   looseJSONName(text[o.nameKey:]),
		}
		if !found || f.betterThan(best) {
			best, found = f, true
		}
		break
	}
	return best, found
}

// classifyJSONCallObject decides whether a balanced object is a call
// and whether it is well-formed. A balanced object that is not valid
// JSON still counts as a malformed call when it quotes both keys.
func classifyJSONCallObject(obj string) (wireFinding, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(obj), &fields); err != nil {
		if strings.Contains(obj, `"arguments"`) {
			return wireFinding{markup: WireMarkupJSONCall, name: looseJSONName(obj)}, true
		}
		return wireFinding{}, false
	}
	rawName, hasName := fields["name"]
	rawArgs, hasArgs := fields["arguments"]
	if !hasName || !hasArgs {
		return wireFinding{}, false
	}
	f := wireFinding{markup: WireMarkupJSONCall}
	nameOK := json.Unmarshal(rawName, &f.name) == nil && f.name != ""
	args := bytes.TrimSpace(rawArgs)
	switch {
	case len(args) > 0 && args[0] == '"':
		f.stringifiedArgs = true
	case len(args) > 0 && args[0] == '{', bytes.Equal(args, []byte("null")):
		// null arguments are what the text fallback executes as {}.
		f.wellFormed = nameOK
	}
	return f, true
}

type textSpan struct{ start, end int }

// openObject is a brace still open at the end of the text, with what
// its own top level showed before the text ran out.
type openObject struct {
	start int
	// nameKey is the offset of its top-level "name" key, or -1.
	nameKey int
	// hasArgs reports a top-level "arguments" key.
	hasArgs bool
	// keys counts its top-level keys.
	keys int
}

// callShaped reports whether a cut-off object reads as a call so far: a
// top-level "name" key beside "arguments", or "name" as its only key
// because the text ended before the next one.
func (o openObject) callShaped() bool {
	return o.nameKey >= 0 && (o.hasArgs || o.keys == 1)
}

// braceSpans returns every balanced {...} span in text, sorted by
// start, and the braces still open at the end, outermost first. Quotes
// are tracked only inside braces, so apostrophes and quotation marks
// in prose do not hide a call that follows them.
func braceSpans(text string) (closed []textSpan, unclosed []openObject) {
	var stack []openObject
	inString, escaped := false, false
	strStart := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				noteObjectKey(&stack[len(stack)-1], text, strStart, i+1)
			}
			continue
		}
		switch c {
		case '"':
			inString, strStart = len(stack) > 0, i
		case '{':
			stack = append(stack, openObject{start: i, nameKey: -1})
		case '}':
			if n := len(stack); n > 0 {
				closed = append(closed, textSpan{start: stack[n-1].start, end: i + 1})
				stack = stack[:n-1]
			}
		}
	}
	slices.SortFunc(closed, func(a, b textSpan) int { return a.start - b.start })
	return closed, stack
}

// noteObjectKey records the quoted string text[start:end] on obj when a
// colon follows it, which makes it one of obj's top-level keys.
func noteObjectKey(obj *openObject, text string, start, end int) {
	if !strings.HasPrefix(strings.TrimLeft(text[end:], " \t\r\n"), ":") {
		return
	}
	obj.keys++
	switch text[start:end] {
	case `"name"`:
		if obj.nameKey < 0 {
			obj.nameKey = start
		}
	case `"arguments"`:
		obj.hasArgs = true
	}
}

// balancedObjectEnd returns the offset just past the object that opens
// s, or false when s does not open with '{' or the object never closes.
func balancedObjectEnd(s string) (int, bool) {
	if s == "" || s[0] != '{' {
		return 0, false
	}
	closed, _ := braceSpans(s)
	for _, sp := range closed {
		if sp.start == 0 {
			return sp.end, true
		}
	}
	return 0, false
}

// jsonNameKeyOffsets returns the ascending offsets of every quoted
// "name" key, meaning "name" followed by a colon.
func jsonNameKeyOffsets(text string) []int {
	var out []int
	for pos := 0; ; {
		i := strings.Index(text[pos:], `"name"`)
		if i < 0 {
			return out
		}
		at := pos + i
		pos = at + len(`"name"`)
		if rest := strings.TrimLeft(text[pos:], " \t\r\n"); strings.HasPrefix(rest, ":") {
			out = append(out, at)
		}
	}
}

func anyOffsetIn(sorted []int, start, end int) bool {
	i, _ := slices.BinarySearch(sorted, start)
	return i < len(sorted) && sorted[i] < end
}

// looseJSONName reads the string value of the first "name" key without
// requiring the surrounding JSON to parse, so a truncated or invalid
// call still reports whether it named an offered tool.
func looseJSONName(s string) string {
	offsets := jsonNameKeyOffsets(s)
	if len(offsets) == 0 {
		return ""
	}
	rest := strings.TrimLeft(s[offsets[0]+len(`"name"`):], " \t\r\n")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, ":"), " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	end := strings.IndexByte(rest[1:], '"')
	if end < 0 {
		return ""
	}
	return rest[1 : 1+end]
}

const (
	harmonyRecipient = "to=functions."
	harmonyMessage   = "<|message|>"
	// harmonyArgsWindow bounds how far after the recipient name the
	// argument object may start. The longest real gap is a
	// "<|channel|>commentary <|constrain|>json<|message|>" header.
	harmonyArgsWindow = 96
)

// harmonyFinding finds to=functions.NAME recipients and judges the JSON
// argument object that follows each.
func harmonyFinding(text string) (wireFinding, bool) {
	var best wireFinding
	found := false
	for pos, attempts := 0, 0; attempts < maxWireCandidates; attempts++ {
		i := strings.Index(text[pos:], harmonyRecipient)
		if i < 0 {
			break
		}
		f, next := parseHarmonyCall(text, pos+i)
		if f.wellFormed {
			return f, true
		}
		if !found {
			best, found = f, true
		}
		pos = next
	}
	return best, found
}

func parseHarmonyCall(text string, anchor int) (wireFinding, int) {
	f := wireFinding{markup: WireMarkupHarmony, start: markupStart(text, anchor, harmonyHeaderTokens)}
	nameStart := anchor + len(harmonyRecipient)
	nameEnd := nameStart
	for nameEnd < len(text) && isHarmonyNameByte(text[nameEnd]) {
		nameEnd++
	}
	f.name = text[nameStart:nameEnd]
	rest := text[nameEnd:]
	window := rest[:min(len(rest), harmonyArgsWindow)]
	argsAt := strings.Index(window, harmonyMessage)
	if argsAt >= 0 {
		argsAt += len(harmonyMessage)
	} else {
		argsAt = strings.IndexByte(window, '{')
	}
	if f.name == "" || argsAt < 0 {
		return f, nameEnd
	}
	args := strings.TrimLeft(rest[argsAt:], " \t\r\n")
	end, ok := balancedObjectEnd(args)
	if !ok {
		return f, nameEnd
	}
	var obj map[string]any
	f.wellFormed = json.Unmarshal([]byte(args[:end]), &obj) == nil
	return f, nameEnd
}

func isHarmonyNameByte(c byte) bool {
	return c == '_' || c == '-' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}
