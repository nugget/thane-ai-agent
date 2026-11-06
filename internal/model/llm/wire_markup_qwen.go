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

// qwenXMLCall is one call parsed from Qwen3-coder XML markup. This is
// the single grammar implementation for that markup: detection uses it
// today, and recovery must reuse it so the two cannot drift.
type qwenXMLCall struct {
	// start is the offset of the <function= tag.
	start int
	name  string
	// params keep the model's order and its raw string values. Typing
	// a value is the recovering caller's job, against the tool schema.
	params []qwenXMLParam
	// complete means a readable header, every parameter closed by
	// </parameter>, and the function closed by </function>,
	// </tool_call> or the end of the text. A call cut off inside its
	// header or a parameter value is not complete, so a truncated value
	// can never be mistaken for the model's intended argument.
	complete bool
}

type qwenXMLParam struct {
	key   string
	value string
}

// parseQwenXMLCalls returns every <function=...> call in text, in
// order, complete or not.
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
		call.name = strings.TrimSpace(text[headerStart:])
		if gt >= 0 {
			call.name = strings.TrimSpace(text[headerStart : headerStart+gt])
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
	params, paramsOK := parseQwenXMLParams(text[bodyStart:bodyEnd])
	call.params = params
	call.complete = call.name != "" && closedHere && paramsOK
	next := bodyEnd
	if closer == qwenFunctionClose {
		next += len(closer)
	}
	return call, next
}

// parseQwenXMLParams parses a function body. It reports false when a
// parameter header or value is cut off or a value runs into the next
// <parameter= without its </parameter>.
func parseQwenXMLParams(body string) ([]qwenXMLParam, bool) {
	var params []qwenXMLParam
	for pos := 0; ; {
		i := strings.Index(body[pos:], qwenParamOpen)
		if i < 0 {
			return params, true
		}
		keyStart := pos + i + len(qwenParamOpen)
		gt := strings.IndexAny(body[keyStart:], ">\n<")
		if gt < 0 || body[keyStart+gt] != '>' {
			return params, false
		}
		key := strings.TrimSpace(body[keyStart : keyStart+gt])
		valueStart := keyStart + gt + 1
		valueEnd, closer := nextQwenToken(body, valueStart, qwenParamClose, qwenParamOpen)
		if key == "" || closer != qwenParamClose {
			return params, false
		}
		params = append(params, qwenXMLParam{key: key, value: trimQwenValue(body[valueStart:valueEnd])})
		pos = valueEnd + len(qwenParamClose)
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

// qwenXMLFinding reduces the parsed calls to a detector finding. A bare
// <tool_call> tag holding neither a function nor a JSON object, as when
// the text is cut right after it, also counts as malformed qwen_xml; a
// tag holding JSON is left to the json_call detector.
func qwenXMLFinding(text string) (wireFinding, bool) {
	var best wireFinding
	found := false
	for i, call := range parseQwenXMLCalls(text) {
		if i >= maxWireCandidates {
			break
		}
		f := wireFinding{
			markup:     WireMarkupQwenXML,
			start:      markupStart(text, call.start, jsonWrapperTokens),
			name:       call.name,
			wellFormed: call.complete,
		}
		if f.wellFormed {
			return f, true
		}
		if !found {
			best, found = f, true
		}
	}
	if found {
		return best, true
	}
	for pos, n := 0, 0; n < maxWireCandidates; n++ {
		i := strings.Index(text[pos:], qwenToolCallOpen)
		if i < 0 {
			break
		}
		open := pos + i
		bodyStart := open + len(qwenToolCallOpen)
		bodyEnd, _ := nextQwenToken(text, bodyStart, qwenToolCallClose, qwenToolCallOpen)
		if !strings.Contains(text[bodyStart:bodyEnd], "{") {
			return wireFinding{markup: WireMarkupQwenXML, start: markupStart(text, open, jsonWrapperTokens)}, true
		}
		pos = bodyEnd
	}
	return wireFinding{}, false
}
