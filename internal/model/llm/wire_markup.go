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
// call-shaped markup is a candidate: a Qwen function or <tool_call>
// tag, a Harmony recipient, or a JSON object with a top-level "name"
// key and a quoted "arguments" key. Named data is never one, so a real
// call is hidden only behind this many malformed calls before it. Real
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
	// Arguments after the next recipient are that recipient's, so an
	// argument-less recipient stays malformed and cannot borrow them.
	if next := strings.Index(window, harmonyRecipient); next >= 0 {
		window = window[:next]
	}
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
