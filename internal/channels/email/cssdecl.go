package email

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A browser splits an inline style attribute into declarations with the
// CSS tokenizer, and so does the renderer. A naive split on ';' and ':'
// invents declarations a browser never sees, and under declaration
// precedence an invented display:block would override a real
// display:none: display:none;x:'a;display:block' hides its text in a
// browser. So a semicolon ends a declaration, the first colon ends its
// name, and a '!' starts !important only where it stands outside every
// comment, string, escape, url(...) and bracketed block. A comment
// separates the tokens on either side of it rather than joining them, so
// display:bl/**/ock is two tokens, not the keyword block.

// cssSpace is the whitespace CSS trims around a token.
const cssSpace = " \t\n\r\f"

// cssNewlines is the CSS input preprocessing that bears on escapes and
// strings: CR LF, CR and FF each become one LF.
var cssNewlines = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n")

// cssDeclaration is one declaration of an inline style attribute.
type cssDeclaration struct {
	// name is the property name read as one keyword (see [cssKeyword]).
	name string

	// value is the value read as one keyword, with a trailing
	// !important removed. A value of several tokens keeps the
	// whitespace between them, so it matches no keyword.
	value string

	// escaped reports whether the value is written with an escape.
	escaped bool

	// important reports a trailing !important.
	important bool
}

// splitDeclarations returns the declarations of one style attribute in
// order. A declaration with no colon at the top level is dropped, as a
// browser drops it.
func splitDeclarations(style string) []cssDeclaration {
	src, top := scanCSS(cssNewlines.Replace(style))
	var decls []cssDeclaration
	start := 0
	for i := 0; i <= len(src); i++ {
		if i < len(src) && (src[i] != ';' || !top[i]) {
			continue
		}
		if d, ok := readDeclaration(src[start:i], top[start:i]); ok {
			decls = append(decls, d)
		}
		start = i + 1
	}
	return decls
}

// readDeclaration reads one declaration from src, where top marks the
// bytes that stand at the top level (see [scanCSS]).
func readDeclaration(src string, top []bool) (cssDeclaration, bool) {
	colon := indexTop(src, top, ':')
	if colon < 0 {
		return cssDeclaration{}, false
	}
	d := cssDeclaration{name: cssKeyword(src[:colon])}
	val, valTop := src[colon+1:], top[colon+1:]
	if bang := lastIndexTop(val, valTop, '!'); bang >= 0 && cssKeyword(val[bang+1:]) == "important" {
		val, d.important = val[:bang], true
	}
	d.value = cssKeyword(val)
	d.escaped = strings.Contains(val, `\`)
	return d, true
}

// cssKeyword reads s as one keyword: CSS whitespace trimmed from the
// text as written, then escapes decoded and ASCII letters lower-cased,
// because a browser matches keywords ASCII case-insensitively.
// Whitespace an escape writes stays, and so does every letter outside
// ASCII: neither block\20 nor bloc\212a, with the Kelvin sign, is block.
func cssKeyword(s string) string {
	return asciiLower(cssUnescape(strings.Trim(s, cssSpace)))
}

// scanCSS blanks each comment in s to spaces and marks the bytes that
// stand at the top level: outside every comment, string, escape,
// url(...) and bracketed block. Blanking keeps the offsets of s and
// keeps a comment separating the tokens on either side of it.
func scanCSS(s string) (string, []bool) {
	src := []byte(s)
	top := make([]bool, len(src))
	var closers []byte // the closing bracket of each open block, innermost last
	word := 0          // where the ident-like run that ends at i starts
	for i := 0; i < len(src); {
		c := src[i]
		if c == '\\' {
			if _, n := cssEscape(s, i); n > 0 {
				// An escape continues the run and is never structure.
				i += n
				continue
			}
		}
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = blankComment(src, i)
		case c == '"' || c == '\'':
			i = stringEnd(s, i)
		case c == '(' && unquotedURL(s, word, i):
			i = urlEnd(s, i+1)
		default:
			top[i] = len(closers) == 0
			closers = nest(closers, c)
			i++
			if isIdentByte(c) {
				continue
			}
		}
		word = i
	}
	return string(src), top
}

// blankComment overwrites the comment that starts at src[i] with spaces
// and returns the offset just past it. An unterminated comment runs to
// the end.
func blankComment(src []byte, i int) int {
	end := len(src)
	if j := bytes.Index(src[i+2:], []byte("*/")); j >= 0 {
		end = i + 2 + j + 2
	}
	for k := i; k < end; k++ {
		src[k] = ' '
	}
	return end
}

// stringEnd returns the offset just past the string opened by the quote
// at s[i]: its closing quote, the newline that ends it unclosed, or the
// end. A backslash escapes the next byte, and one before a newline
// continues the string onto the next line.
func stringEnd(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case q:
			return j + 1
		case '\n':
			return j
		case '\\':
			j++
		}
	}
	return len(s)
}

// unquotedURL reports whether the '(' at s[i] opens a url token: the
// ident-like run s[word:i] before it is url, and no quote opens its
// argument. url("a") is an ordinary function whose argument is a string.
func unquotedURL(s string, word, i int) bool {
	if asciiLower(cssUnescape(s[word:i])) != "url" {
		return false
	}
	j := i + 1
	for j < len(s) && strings.IndexByte(cssSpace, s[j]) >= 0 {
		j++
	}
	return j == len(s) || (s[j] != '"' && s[j] != '\'')
}

// urlEnd returns the offset just past the url token whose argument
// starts at s[j]: its first ')' that is not escaped, or the end. Quotes,
// brackets and semicolons inside it are plain code points, and a
// malformed url is consumed to the same ')'.
func urlEnd(s string, j int) int {
	for j < len(s) {
		switch s[j] {
		case ')':
			return j + 1
		case '\\':
			if _, n := cssEscape(s, j); n > 0 {
				j += n
				continue
			}
		}
		j++
	}
	return j
}

// nest updates the stack of open blocks for the byte c. A closing
// bracket that does not match the innermost open block is a plain code
// point inside it.
func nest(closers []byte, c byte) []byte {
	switch c {
	case '(':
		return append(closers, ')')
	case '[':
		return append(closers, ']')
	case '{':
		return append(closers, '}')
	}
	if n := len(closers); n > 0 && c == closers[n-1] {
		return closers[:n-1]
	}
	return closers
}

// indexTop returns the offset of the first c in s at the top level, or
// -1.
func indexTop(s string, top []bool, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c && top[i] {
			return i
		}
	}
	return -1
}

// lastIndexTop returns the offset of the last c in s at the top level,
// or -1.
func lastIndexTop(s string, top []bool, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c && top[i] {
			return i
		}
	}
	return -1
}

// isIdentByte reports whether c can continue an ident: an ASCII letter
// or digit, '-', '_', or any byte of a character outside ASCII.
func isIdentByte(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || isDigit(c) ||
		c == '-' || c == '_' || c >= utf8.RuneSelf
}

// asciiLower lower-cases the ASCII letters of s and nothing else.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// cssUnescape decodes CSS backslash escapes (see [cssEscape]), which a
// browser decodes before it reads a keyword. Without it,
// display:n\one would hide text from a reader and not from the model.
func cssUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\\' {
			if r, n := cssEscape(s, i); n > 0 {
				sb.WriteRune(r)
				i += n
				continue
			}
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// cssEscape reads the escape that starts with the backslash at s[i]: the
// code point it stands for and its length in bytes. A backslash and one
// to six hex digits, with one optional whitespace after them, is that
// code point; a backslash before anything else is that character, and
// one at the end is U+FFFD. n is 0 when a newline follows the
// backslash, which starts no escape.
func cssEscape(s string, i int) (r rune, n int) {
	j := i + 1
	if j == len(s) {
		return unicode.ReplacementChar, 1
	}
	if s[j] == '\n' || s[j] == '\r' || s[j] == '\f' {
		return 0, 0
	}
	for j < len(s) && j-i <= 6 && isHexDigit(s[j]) {
		j++
	}
	if j == i+1 {
		r, size := utf8.DecodeRuneInString(s[j:])
		return r, 1 + size
	}
	cp, _ := strconv.ParseUint(s[i+1:j], 16, 32)
	if cp == 0 || cp > unicode.MaxRune || (cp >= 0xD800 && cp <= 0xDFFF) {
		cp = unicode.ReplacementChar
	}
	if j < len(s) && strings.IndexByte(cssSpace, s[j]) >= 0 {
		j++
	}
	return rune(cp), j - i
}

func isHexDigit(b byte) bool {
	return ('0' <= b && b <= '9') || ('a' <= b && b <= 'f') || ('A' <= b && b <= 'F')
}
