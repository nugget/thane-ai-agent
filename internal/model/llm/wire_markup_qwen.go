package llm

import "strings"

// The Qwen3-coder XML tool-call grammar. Every token opens with '<',
// which lets the parser find the next one with a forward scan instead
// of repeated whole-text searches.
const (
	qwenToolCallOpen  = "<tool_call>"
	qwenToolCallClose = "</tool_call>"
	qwenFunctionOpen  = "<function="
	qwenFunctionClose = "</function>"
	qwenParamOpen     = "<parameter="
	qwenParamClose    = "</parameter>"
)

// qwenXMLCall is one call parsed from Qwen3-coder XML markup by
// [parseQwenXMLFunction], the single grammar implementation for that
// markup. Detection walks it one call at a time; anything else that
// reads the markup, such as text-call recovery, must reuse it so the
// two cannot drift.
type qwenXMLCall struct {
	// start is the offset of the <function= tag.
	start int
	name  string
	// params keep the model's order and its raw string values. Typing
	// a value is the recovering caller's job, against the tool schema.
	params []qwenXMLParam
	// values are the spans of every parameter value in the text, in
	// order, including values params omits: one cut off, unkeyed, or
	// after the point where the body stopped parsing. Markup inside
	// them is argument data, never a call.
	values []textRange
	// complete means a readable header, a body of nothing but
	// whitespace and parameters each closed by </parameter>, and the
	// function closed by </function>, </tool_call> or the end of the
	// text. A call cut off inside its header or a parameter value is not
	// complete, so a truncated value can never be mistaken for the
	// model's intended argument, and neither is a body holding free text,
	// whose arguments a runtime could not read.
	complete bool
}

type qwenXMLParam struct {
	key   string
	value string
}

// parseQwenXMLCalls returns every <function=...> call in text, in
// order, complete or not, for a reader that needs them all. Detection
// walks [parseQwenXMLFunction] itself instead, so it never parses past
// its candidate limit.
func parseQwenXMLCalls(text string) []qwenXMLCall {
	var calls []qwenXMLCall
	for pos := 0; ; {
		i := strings.Index(text[pos:], qwenFunctionOpen)
		if i < 0 {
			return calls
		}
		call, next := parseQwenXMLFunction(text, pos+i)
		calls = append(calls, call)
		pos = next
	}
}

func parseQwenXMLFunction(text string, open int) (qwenXMLCall, int) {
	call := qwenXMLCall{start: open}
	headerStart := open + len(qwenFunctionOpen)
	gt := strings.IndexAny(text[headerStart:], ">\n<")
	if gt < 0 || text[headerStart+gt] != '>' {
		// The header never closed: the text was cut inside the name.
		// Only the bounded header is trimmed, so a run of malformed
		// headers cannot each re-trim the same long tail.
		if gt >= 0 {
			call.name = strings.TrimSpace(text[headerStart : headerStart+gt])
		} else {
			call.name = strings.TrimSpace(text[headerStart:])
		}
		return call, headerStart
	}
	call.name = strings.TrimSpace(text[headerStart : headerStart+gt])
	bodyStart := headerStart + gt + 1
	bodyEnd, closer := nextQwenToken(text, bodyStart,
		qwenFunctionClose, qwenToolCallClose, qwenFunctionOpen, qwenToolCallOpen)
	// A missing </function> is tolerated only where the call ends: at
	// </tool_call> or at the end of the text. Running into the next
	// opening tag means the call was never closed.
	closedHere := closer != qwenFunctionOpen && closer != qwenToolCallOpen
	params, values, paramsOK := parseQwenXMLParams(text[bodyStart:bodyEnd])
	call.params = params
	for _, v := range values {
		call.values = append(call.values, textRange{bodyStart + v.start, bodyStart + v.end})
	}
	call.complete = call.name != "" && closedHere && paramsOK
	next := bodyEnd
	if closer == qwenFunctionClose {
		next += len(closer)
	}
	return call, next
}

// parseQwenXMLParams parses a function body, returning its parameters
// and the body-relative spans of their values. It reports false when a
// parameter header or value is cut off, a value runs into the next
// <parameter= without its </parameter>, a key is empty, or
// non-whitespace text sits outside the parameters: before the first,
// between two, or after the last. That last case also covers prose that
// merely quotes a <function=...> tag and runs to the end of the text.
// params stops at the first such fault, but values goes on to the end
// of the body, since every value's text is argument data all the same.
func parseQwenXMLParams(body string) ([]qwenXMLParam, []textRange, bool) {
	var params []qwenXMLParam
	var values []textRange
	ok := true
	for pos := 0; ; {
		i := strings.Index(body[pos:], qwenParamOpen)
		if i < 0 {
			return params, values, ok && strings.TrimSpace(body[pos:]) == ""
		}
		if strings.TrimSpace(body[pos:pos+i]) != "" {
			ok = false
		}
		keyStart := pos + i + len(qwenParamOpen)
		gt := strings.IndexAny(body[keyStart:], ">\n<")
		if gt < 0 {
			return params, values, false
		}
		if body[keyStart+gt] != '>' {
			ok, pos = false, keyStart+gt
			continue
		}
		key := strings.TrimSpace(body[keyStart : keyStart+gt])
		valueStart := keyStart + gt + 1
		valueEnd, closer := nextQwenToken(body, valueStart, qwenParamClose, qwenParamOpen)
		values = append(values, textRange{valueStart, valueEnd})
		if key == "" || closer != qwenParamClose {
			ok = false
		}
		if ok {
			params = append(params, qwenXMLParam{key: key, value: trimQwenValue(body[valueStart:valueEnd])})
		}
		pos = valueEnd
		if closer == qwenParamClose {
			pos += len(qwenParamClose)
		}
	}
}

// trimQwenValue drops the single newline the Qwen template puts on
// each side of a parameter value, and nothing more: other whitespace
// may be part of the value.
func trimQwenValue(v string) string {
	v = strings.TrimPrefix(v, "\n")
	return strings.TrimSuffix(v, "\n")
}

// nextQwenToken returns the offset of the first of tokens at or after
// from, and which token it was. It returns len(text) and "" when none
// occurs. Every token must begin with '<'.
func nextQwenToken(text string, from int, tokens ...string) (int, string) {
	for pos := from; pos < len(text); {
		i := strings.IndexByte(text[pos:], '<')
		if i < 0 {
			break
		}
		at := pos + i
		for _, tok := range tokens {
			if strings.HasPrefix(text[at:], tok) {
				return at, tok
			}
		}
		pos = at + 1
	}
	return len(text), ""
}

// qwenXMLCandidates examines up to [maxWireCandidates] functions, one
// at a time, then the bare <tool_call> tags.
func qwenXMLCandidates(text string) []wireCandidate {
	var out []wireCandidate
	for pos := 0; len(out) < maxWireCandidates; {
		i := strings.Index(text[pos:], qwenFunctionOpen)
		if i < 0 {
			break
		}
		call, next := parseQwenXMLFunction(text, pos+i)
		out = append(out, wireCandidate{
			wireFinding: wireFinding{
				markup:     WireMarkupQwenXML,
				start:      markupStart(text, call.start, jsonWrapperTokens),
				name:       call.name,
				wellFormed: call.complete,
			},
			anchor: call.start,
			data:   call.values,
		})
		pos = next
	}
	return append(out, bareToolCallCandidates(text)...)
}

// bareToolCallCandidates returns, as malformed qwen_xml, up to
// [maxWireCandidates] <tool_call> tags holding neither a function nor
// a JSON object, as when the text is cut right after the tag. They
// compete with functions by position, so the earliest malformed markup
// is the one described. A tag
// holding a function is that function's wrapper, and one holding JSON
// is left to the json_call detector. Tag bodies do not overlap, so the
// walk stays linear however many tags it passes over.
func bareToolCallCandidates(text string) []wireCandidate {
	var out []wireCandidate
	for pos := 0; len(out) < maxWireCandidates; {
		i := strings.Index(text[pos:], qwenToolCallOpen)
		if i < 0 {
			break
		}
		open := pos + i
		bodyStart := open + len(qwenToolCallOpen)
		bodyEnd, _ := nextQwenToken(text, bodyStart, qwenToolCallClose, qwenToolCallOpen)
		if body := text[bodyStart:bodyEnd]; !strings.Contains(body, "{") && !strings.Contains(body, qwenFunctionOpen) {
			out = append(out, wireCandidate{
				wireFinding: wireFinding{markup: WireMarkupQwenXML, start: markupStart(text, open, jsonWrapperTokens)},
				anchor:      open,
			})
		}
		pos = bodyEnd
	}
	return out
}
