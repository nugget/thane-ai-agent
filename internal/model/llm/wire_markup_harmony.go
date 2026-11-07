package llm

import (
	"encoding/json"
	"strings"
)

const (
	harmonyRecipient = "to=functions."
	// harmonyArgsWindow bounds how far after the recipient name the
	// argument value may start. The longest real gap is a
	// "<|channel|>commentary <|constrain|>json<|message|>" header.
	harmonyArgsWindow = 96
)

// harmonyHeaderTokens are the Harmony header pieces that precede a
// recipient, tokenised or with the special tokens stripped.
var harmonyHeaderTokens = []string{"<|start|>", "<|channel|>", "assistant", "commentary", "analysis"}

// harmonyEndTokens end a Harmony message wherever they appear, inside a
// JSON string included, because the model emits each as one special
// token rather than as text it is quoting.
var harmonyEndTokens = []string{"<|call|>", "<|end|>", "<|return|>", "<|start|>"}

// harmonyCandidates finds to=functions.NAME recipients and judges the
// JSON argument object that follows each. A recipient inside an earlier
// recipient's closed arguments is that call's data, so the search
// resumes past them. Past a value that never closed it resumes after
// the name instead: where such a value ends is a guess, and a guess
// made inside another call's data could swallow a later real call. The
// scan still excludes what lies inside it.
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

// parseHarmonyCall reads the recipient at anchor and its argument
// value. The call is well-formed only when that value is a JSON object
// that closes; any JSON value, whatever its type, is the call's data.
// It also returns where the search for the next recipient resumes:
// past a closed value, or after the name.
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
	at := harmonyArgsStart(window)
	if c.name == "" || at < 0 {
		return c, nameEnd
	}
	open := nameEnd + at
	if b := text[open]; b != '{' && b != '[' && b != '"' {
		return c, nameEnd
	}
	end, closed := harmonyValueEnd(text, open)
	c.data = []textRange{{open + 1, end}}
	if !closed {
		return c, nameEnd
	}
	if text[open] == '{' {
		var obj map[string]any
		c.wellFormed = json.Unmarshal([]byte(text[open:end]), &obj) == nil
	}
	return c, end
}

// harmonyArgsStart returns the offset in window where a recipient's
// argument value begins, window being the text after the recipient's
// name. Only the header may come first: whitespace, special tokens such
// as <|constrain|> and <|message|>, and the words around them, such as
// a channel or a content type (commentary, json, code). The value
// begins at the first byte that is none of those, so json[...] begins
// at its bracket rather than at an object inside it, and a token quoted
// inside the value stays in the value. It returns -1 when the window
// holds nothing but header.
func harmonyArgsStart(window string) int {
	for pos := 0; pos < len(window); {
		switch c := window[pos]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || isHarmonyNameByte(c):
			pos++
		case strings.HasPrefix(window[pos:], "<|"):
			end := strings.Index(window[pos:], "|>")
			if end < 0 {
				return -1
			}
			pos += end + len("|>")
		default:
			return pos
		}
	}
	return -1
}

// harmonyValueEnd returns the offset just past the JSON value opening
// at text[open], a brace, bracket or quote, and whether it closed. A
// value that never closes ends where its message does: at an end token
// anywhere, or, outside a string, at another special token or a next
// recipient, neither of which JSON can hold. Only failing those, it
// runs to the end of the text.
func harmonyValueEnd(text string, open int) (int, bool) {
	depth, inString, escaped := 0, false, false
	for i := open; i < len(text); i++ {
		c := text[i]
		if c == '<' && hasAnyPrefix(text[i:], harmonyEndTokens) {
			return i, false
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				if depth == 0 {
					return i + 1, true
				}
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth--; depth <= 0 {
				return i + 1, true
			}
		case c == '<' && strings.HasPrefix(text[i:], "<|"),
			c == 't' && strings.HasPrefix(text[i:], harmonyRecipient):
			return i, false
		}
	}
	return len(text), false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isHarmonyNameByte(c byte) bool {
	return c == '_' || c == '-' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}
