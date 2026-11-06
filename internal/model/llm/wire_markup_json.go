package llm

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// jsonCallFinding finds JSON objects carrying "name" and "arguments"
// anywhere in text: closed objects first, then an unclosed object cut
// off at the end of the text.
//
// Only call-shaped closed objects, with a "name" key at their own top
// level and a quoted "arguments" key inside them, are parsed and
// charged against [maxWireCandidates]. Named data such as a recapped
// tool result or tool list costs nothing, so no amount of it can hide
// a later call.
func jsonCallFinding(text string) (wireFinding, bool) {
	if !strings.Contains(text, `"name"`) {
		return wireFinding{}, false
	}
	argKeys := quotedOffsets(text, `"arguments"`)
	closed, unclosed := braceSpans(text)
	cutOffs := cutOffCalls(text, closed, unclosed)
	var best wireFinding
	found := false
	skipUntil, attempts, c := -1, 0, 0
	for _, sp := range closed {
		for c < len(cutOffs) && sp.start >= cutOffs[c].end {
			c++
		}
		if c < len(cutOffs) && sp.start > cutOffs[c].start {
			continue
		}
		if sp.start < skipUntil || !sp.named || !anyOffsetIn(argKeys, sp.start, sp.end) {
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
	if len(cutOffs) > 0 {
		first := cutOffs[0]
		f := wireFinding{
			markup: WireMarkupJSONCall,
			start:  markupStart(text, first.start, jsonWrapperTokens),
			name:   looseJSONName(text[first.nameKey:]),
		}
		if !found || f.betterThan(best) {
			best, found = f, true
		}
	}
	return best, found
}

// cutOffCall is a call left open at the end of the text, and where its
// own text ends.
type cutOffCall struct {
	start int
	// nameKey is the offset of its top-level "name" key.
	nameKey int
	// end is the offset just past its own text. Objects that start
	// inside it are its arguments or other data, not calls.
	end int
}

// cutOffCalls returns the calls cut off at the end of the text, in
// order. A cut-off call is an object still open at the end of the text
// whose own top level reads as a call and that starts after the
// previous cut-off call's own text. Open objects inside a cut-off
// call's text are its data, which often carry a "name" of their own.
// Their texts are disjoint, so finding them all stays linear.
func cutOffCalls(text string, closed []textSpan, unclosed []openObject) []cutOffCall {
	var out []cutOffCall
	after := 0
	for _, o := range unclosed {
		if o.start < after || !o.callShaped(text) {
			continue
		}
		after = o.textEnd(text, closed)
		out = append(out, cutOffCall{start: o.start, nameKey: o.nameKey, end: after})
	}
	return out
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

// textSpan is a balanced {...} span of the text.
type textSpan struct {
	start, end int
	// named reports a "name" key at the object's own top level.
	named bool
}

// openObject is a brace still open at the end of the text, with what
// its own top level showed before the text ran out.
type openObject struct {
	start int
	// nameKey is the offset of its top-level "name" key, or -1.
	nameKey int
	// argsKey is the offset just past its top-level "arguments" key, or
	// -1.
	argsKey int
	// keys counts its top-level keys.
	keys int
}

// callShaped reports whether a cut-off object reads as a call so far: a
// top-level "name" key beside "arguments", or "name" as its only key
// when the text ended before an "arguments" key could appear: inside
// the name pair, or inside a next key that could still be "arguments".
// A closed name with nothing after it is ordinary data as often as a
// call, so it is not enough on its own.
func (o openObject) callShaped(text string) bool {
	if o.nameKey < 0 {
		return false
	}
	return o.argsKey >= 0 || (o.keys == 1 && cutBeforeArguments(text[o.nameKey+len(`"name"`):]))
}

// textEnd returns the offset where a cut-off call's own text ends: just
// past the value of whichever of its "name" and "arguments" keys came
// last, and past a trailing comma, so a model that dropped one closing
// brace and wrote another call after it still has that call seen. A
// comma followed by a quoted key, or a value cut off by the end of the
// text, runs the object to the end of the text, and so does a missing
// "arguments" key.
func (o openObject) textEnd(text string, closed []textSpan) int {
	if o.argsKey < 0 {
		return len(text)
	}
	last := max(o.argsKey, o.nameKey+len(`"name"`))
	rest := strings.TrimLeft(text[last:], " \t\r\n")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, ":"), " \t\r\n")
	end := jsonValueEnd(text, len(text)-len(rest), closed)
	afterComma, comma := strings.CutPrefix(strings.TrimLeft(text[end:], " \t\r\n"), ",")
	switch {
	case !comma:
		return end
	case strings.HasPrefix(strings.TrimLeft(afterComma, " \t\r\n"), `"`):
		return len(text)
	}
	return len(text) - len(afterComma)
}

// jsonValueEnd returns the offset just past the JSON value that starts
// at text[v:], or len(text) when the value is cut off. A scalar ends at
// the first byte that cannot continue it.
func jsonValueEnd(text string, v int, closed []textSpan) int {
	rest := text[v:]
	switch {
	case rest == "":
		return len(text)
	case rest[0] == '[':
		return v + jsonArrayEnd(rest)
	case rest[0] == '{':
		i, ok := slices.BinarySearchFunc(closed, v, func(sp textSpan, start int) int { return sp.start - start })
		if !ok {
			return len(text)
		}
		return closed[i].end
	case rest[0] == '"':
		end, _ := jsonStringEnd(rest)
		return v + end
	}
	if i := strings.IndexAny(rest, ",{}[]\" \t\r\n"); i >= 0 {
		return v + i
	}
	return len(text)
}

// jsonArrayEnd returns the offset just past the array that opens s,
// matching brackets and braces outside strings, or len(s) when the
// array is cut off.
func jsonArrayEnd(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			end, ok := jsonStringEnd(s[i:])
			if !ok {
				return len(s)
			}
			i += end - 1
		case '[', '{':
			depth++
		case ']', '}':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

// cutBeforeArguments reads the tail of a cut-off object from just after
// its "name" key. It reports whether the text ended inside the name
// pair, before or inside its string value, or inside a next key that is
// still a prefix of "arguments". Every scan stops at the first byte
// that settles the answer, so a run of cut-off objects stays linear.
func cutBeforeArguments(tail string) bool {
	rest := strings.TrimLeft(tail, " \t\r\n")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, ":"), " \t\r\n")
	if rest == "" {
		return true
	}
	end, closed := jsonStringEnd(rest)
	if !closed {
		return end > 0
	}
	rest = strings.TrimLeft(rest[end:], " \t\r\n")
	if !strings.HasPrefix(rest, ",") {
		return false
	}
	key := strings.TrimLeft(rest[1:], " \t\r\n")
	keyEnd, keyClosed := jsonStringEnd(key)
	if !keyClosed {
		return keyEnd > 0 && strings.HasPrefix(`"arguments"`, key)
	}
	return key[:keyEnd] == `"arguments"` && strings.TrimLeft(key[keyEnd:], " \t\r\n") == ""
}

// jsonStringEnd returns the offset just past the JSON string that opens
// s and true, or, when s opens a string that never closes, len(s) and
// false. It returns 0 and false when s does not open with a quote.
func jsonStringEnd(s string) (int, bool) {
	if !strings.HasPrefix(s, `"`) {
		return 0, false
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return len(s), false
}

// braceSpans returns every balanced {...} span in text, sorted by
// start, and the braces still open at the end, outermost first. Quotes
// are tracked only inside braces, so apostrophes and quotation marks
// in prose do not hide a call that follows them. A lone double quote
// inside a prose brace, such as an inch mark in {12" screen}, still
// opens a string there that swallows what follows, a call included,
// until another quote closes it.
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
			stack = append(stack, openObject{start: i, nameKey: -1, argsKey: -1})
		case '}':
			if n := len(stack); n > 0 {
				top := stack[n-1]
				closed = append(closed, textSpan{start: top.start, end: i + 1, named: top.nameKey >= 0})
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
		if obj.argsKey < 0 {
			obj.argsKey = end
		}
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

// quotedOffsets returns the ascending offsets of every occurrence of
// the quoted string key in text.
func quotedOffsets(text, key string) []int {
	var out []int
	for pos := 0; ; {
		i := strings.Index(text[pos:], key)
		if i < 0 {
			return out
		}
		out = append(out, pos+i)
		pos += i + len(key)
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
