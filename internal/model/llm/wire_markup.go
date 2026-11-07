package llm

import (
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
// Markup that begins inside another call's argument data, in any
// grammar, is that call's data and never a call of its own.
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
// so an adversarial or runaway text with thousands of nested calls or
// recipients would otherwise make a detection pass quadratic. Only
// call-shaped markup is a candidate: a Qwen function or bare
// <tool_call> tag, a Harmony recipient, or a JSON object with "name"
// and "arguments" keys at its own top level. Named data is never one,
// and a detector skips markup nested in its own grammar's calls, so a
// real call is hidden only behind this many calls of its grammar
// before it. Real replies carry a handful of calls.
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

// textRange is the half-open span [start, end) of a text.
type textRange struct{ start, end int }

// wireCandidate is one call a detector examined.
type wireCandidate struct {
	wireFinding
	// anchor is where the call itself begins, inside any wrapper: its
	// opening brace, <function= or <tool_call> tag, or recipient.
	anchor int
	// data are the spans holding the call's argument data: a JSON
	// call's object, a Harmony argument object, or Qwen parameter
	// values. A span cut off by the end of the text runs to its end.
	data []textRange
}

// insideCallData reports whether offset at lies inside any
// candidate's argument data.
func insideCallData(at int, cands []wireCandidate) bool {
	for _, c := range cands {
		for _, r := range c.data {
			if r.start <= at && at < r.end {
				return true
			}
		}
	}
	return false
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

// scanWireText gathers every detector's candidates and describes the
// best one that is not another call's argument data. Detectors return
// all the candidates they examine rather than stopping at their first
// well-formed call, because only together can they tell that a call is
// nested in another grammar's arguments. Each data span check walks a
// bounded number of candidates, so the pass stays linear.
func scanWireText(text string) (wireFinding, bool) {
	if strings.TrimSpace(text) == "" {
		return wireFinding{}, false
	}
	var cands []wireCandidate
	for _, detect := range []func(string) []wireCandidate{qwenXMLCandidates, jsonCallCandidates, harmonyCandidates} {
		cands = append(cands, detect(text)...)
	}
	var best wireFinding
	found := false
	for _, c := range cands {
		if (!found || c.betterThan(best)) && !insideCallData(c.anchor, cands) {
			best, found = c.wireFinding, true
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

// harmonyArgsHeaderTokens are the Harmony header pieces that may sit
// between a recipient's name and its argument object, tokenised or
// with the special tokens stripped.
var harmonyArgsHeaderTokens = []string{"<|channel|>", "<|constrain|>", "<|message|>", "commentary", "analysis", "json"}

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

const (
	harmonyRecipient = "to=functions."
	// harmonyArgsWindow bounds how far after the recipient name the
	// argument object may start. The longest real gap is a
	// "<|channel|>commentary <|constrain|>json<|message|>" header.
	harmonyArgsWindow = 96
)

// harmonyCandidates finds to=functions.NAME recipients and judges the
// JSON argument object that follows each. A recipient inside an earlier
// recipient's arguments is that call's data, so the search resumes past
// them.
func harmonyCandidates(text string) []wireCandidate {
	var out []wireCandidate
	for pos := 0; len(out) < maxWireCandidates; {
		i := strings.Index(text[pos:], harmonyRecipient)
		if i < 0 {
			break
		}
		c, next := parseHarmonyCall(text, pos+i)
		out = append(out, c)
		pos = next
	}
	return out
}

func parseHarmonyCall(text string, anchor int) (wireCandidate, int) {
	c := wireCandidate{
		wireFinding: wireFinding{markup: WireMarkupHarmony, start: markupStart(text, anchor, harmonyHeaderTokens)},
		anchor:      anchor,
	}
	nameStart := anchor + len(harmonyRecipient)
	nameEnd := nameStart
	for nameEnd < len(text) && isHarmonyNameByte(text[nameEnd]) {
		nameEnd++
	}
	c.name = text[nameStart:nameEnd]
	rest := text[nameEnd:]
	window := rest[:min(len(rest), harmonyArgsWindow)]
	// Arguments after the next recipient are that recipient's, so an
	// argument-less recipient stays malformed and cannot borrow them.
	if next := strings.Index(window, harmonyRecipient); next >= 0 {
		window = window[:next]
	}
	argsAt := harmonyArgsStart(window)
	if c.name == "" || argsAt < 0 {
		return c, nameEnd
	}
	open := nameEnd + argsAt
	end, ok := balancedObjectEnd(text[open:])
	if !ok {
		c.data = []textRange{{open + 1, len(text)}}
		return c, len(text)
	}
	c.data = []textRange{{open + 1, open + end}}
	var obj map[string]any
	c.wellFormed = json.Unmarshal([]byte(text[open:open+end]), &obj) == nil
	return c, open + end
}

// harmonyArgsStart returns the offset in window of the argument
// object's opening brace, where window is the text after a recipient's
// name. Only whitespace and Harmony header pieces may come before it,
// so the arguments are an object and not a value that merely contains
// one, and header tokens quoted inside the arguments stay data. It
// returns -1 when anything else comes first or no brace follows.
func harmonyArgsStart(window string) int {
	for pos := 0; pos < len(window); {
		switch window[pos] {
		case '{':
			return pos
		case ' ', '\t', '\r', '\n':
			pos++
			continue
		}
		tok := ""
		for _, t := range harmonyArgsHeaderTokens {
			if strings.HasPrefix(window[pos:], t) {
				tok = t
				break
			}
		}
		if tok == "" {
			return -1
		}
		pos += len(tok)
	}
	return -1
}

func isHarmonyNameByte(c byte) bool {
	return c == '_' || c == '-' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}
