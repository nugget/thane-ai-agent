package email

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Hidden text is text an HTML body's own markup keeps a person reading
// the message from seeing, which a renderer that ignored the markup
// would hand the model as ordinary body text (#1559). The renderer
// recognises the inline idioms senders use for it and nothing more:
// the hidden attribute, aria-hidden="true", and an inline style of
// display:none, visibility:hidden or collapse, a zero font-size, or a
// zero opacity. It reads no stylesheet and compares no colours; that
// would be layout.

// visibility is what the inline markup of an element and its ancestors
// does to the content inside it.
type visibility struct {
	// removed is set by an idiom no descendant can undo: the hidden
	// attribute, aria-hidden="true", display:none, or a zero opacity.
	removed bool

	// fontZero is an inherited zero font-size. It hides text but not
	// images, and a descendant's own nonzero font-size shows its text
	// again, which is how mail layouts close the gaps between columns.
	fontZero bool

	// invisible is an inherited visibility of hidden or collapse. It
	// hides text and images alike, and a descendant's own
	// visibility:visible shows them again.
	invisible bool
}

// textHidden reports whether text inside the element is hidden.
func (v visibility) textHidden() bool { return v.removed || v.fontZero || v.invisible }

// boxHidden reports whether an image or rule inside the element is
// hidden. A zero font-size shrinks text, not images.
func (v visibility) boxHidden() bool { return v.removed || v.invisible }

// within returns the visibility of n's content, given v inherited from
// n's parent.
func (v visibility) within(n *html.Node) visibility {
	if v.removed {
		return v
	}
	if n.DataAtom == atom.Font && legacyFontSize(attr(n, "size")) {
		// A valid <font size> sets a nonzero size of its own. It is a
		// presentational hint, so the element's inline style, read
		// below, still overrides it.
		v.fontZero = false
	}
	for _, a := range n.Attr {
		if a.Namespace != "" {
			continue
		}
		switch a.Key {
		case "hidden":
			// Presence hides whatever the value says, as in a browser.
			v.removed = true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(a.Val), "true") {
				v.removed = true
			}
		case "style":
			v = v.styled(a.Val)
		}
	}
	return v
}

// styled applies one inline style attribute. A hiding declaration holds
// even when a later one in the same attribute contradicts it: a browser
// lets the later one win only when its value is valid, and judging that
// takes a CSS parser, so the renderer errs toward withholding text and
// counting it. A value it does not understand changes nothing, the way
// a browser ignores an invalid declaration.
func (v visibility) styled(style string) visibility {
	var fontZero, fontReset, invisible, visible bool
	for _, decl := range strings.Split(stripCSSComments(style), ";") {
		prop, val, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		val = cssValue(val)
		switch strings.ToLower(strings.TrimSpace(cssUnescape(prop))) {
		case "display":
			v.removed = v.removed || val == "none"
		case "opacity":
			v.removed = v.removed || opacityZero(val)
		case "visibility":
			switch val {
			case "hidden", "collapse":
				invisible = true
			case "visible", "initial":
				visible = true
			}
		case "font-size":
			zero, reset := fontSizeZero(val)
			fontZero = fontZero || zero
			fontReset = fontReset || reset
		}
	}
	switch {
	case fontZero:
		v.fontZero = true
	case fontReset:
		v.fontZero = false
	}
	switch {
	case invisible:
		v.invisible = true
	case visible:
		v.invisible = false
	}
	return v
}

// stripCSSComments removes /* */ comments, which a browser ignores
// inside a declaration. An unterminated comment runs to the end.
func stripCSSComments(s string) string {
	var sb strings.Builder
	for {
		start := strings.Index(s, "/*")
		if start < 0 {
			sb.WriteString(s)
			return sb.String()
		}
		sb.WriteString(s[:start])
		end := strings.Index(s[start+2:], "*/")
		if end < 0 {
			return sb.String()
		}
		s = s[start+2+end+2:]
	}
}

// cssValue normalises a declaration's value: escapes decoded, trimmed,
// lower-cased, and without a trailing !important, which changes
// precedence rather than meaning.
func cssValue(val string) string {
	val = strings.ToLower(strings.TrimSpace(cssUnescape(val)))
	if i := strings.LastIndexByte(val, '!'); i >= 0 && strings.TrimSpace(val[i+1:]) == "important" {
		val = strings.TrimSpace(val[:i])
	}
	return val
}

// opacityZero reports whether an opacity value leaves nothing visible.
// A browser clamps a negative opacity to zero.
func opacityZero(val string) bool {
	f, err := strconv.ParseFloat(strings.TrimSuffix(val, "%"), 64)
	return err == nil && f <= 0
}

// cssUnescape decodes CSS backslash escapes, which a browser decodes
// before it reads a keyword: a backslash and one to six hex digits,
// with one optional whitespace after them, is that code point, and a
// backslash before anything else is that character. Without it,
// display:n\one would hide text from a reader and not from the model.
func cssUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			sb.WriteByte(s[i])
			continue
		}
		j := i + 1
		for j < len(s) && j-i <= 6 && isHexDigit(s[j]) {
			j++
		}
		if j == i+1 {
			// Not hex: the next character stands for itself. A trailing
			// backslash stands for nothing a keyword could match.
			if j < len(s) {
				r, size := utf8.DecodeRuneInString(s[j:])
				sb.WriteRune(r)
				j += size
			}
			i = j - 1
			continue
		}
		cp, _ := strconv.ParseUint(s[i+1:j], 16, 32)
		if cp == 0 || cp > unicode.MaxRune || (cp >= 0xD800 && cp <= 0xDFFF) {
			cp = unicode.ReplacementChar
		}
		sb.WriteRune(rune(cp))
		if j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r' || s[j] == '\f') {
			j++
		}
		i = j - 1
	}
	return sb.String()
}

func isHexDigit(b byte) bool {
	return ('0' <= b && b <= '9') || ('a' <= b && b <= 'f') || ('A' <= b && b <= 'F')
}

// legacyFontSize reports whether a <font size> value is one a browser
// applies: optional whitespace, an optional sign, and at least one
// digit. Every such value maps to a nonzero size from 1 to 7.
func legacyFontSize(val string) bool {
	val = strings.TrimLeft(val, " \t\n\r\f")
	if val != "" && (val[0] == '+' || val[0] == '-') {
		val = val[1:]
	}
	return val != "" && '0' <= val[0] && val[0] <= '9'
}

// fontSizeKeywords are font-size values that set a nonzero size of
// their own. smaller, larger and math are absent: they scale the
// parent's size, so under a zero size they stay zero.
var fontSizeKeywords = map[string]bool{
	"xx-small": true, "x-small": true, "small": true, "medium": true,
	"large": true, "x-large": true, "xx-large": true, "xxx-large": true,
	"initial": true,
}

// fontSizeUnits are the units a nonzero font-size sets a size of its
// own in. The empty unit counts because the quirks mode most mail
// renders in accepts a bare number. em, ex, ch and % are absent: they
// scale the parent's size, so under a zero size they stay zero. rem
// scales the root's size, which a sender hiding text does not zero.
var fontSizeUnits = map[string]bool{
	"": true, "px": true, "pt": true, "pc": true,
	"rem": true, "cm": true, "mm": true, "in": true,
	"q": true, "vw": true, "vh": true, "vmin": true, "vmax": true,
}

// fontSizeZero reads a font-size value. zero means a size of zero in
// any unit. reset means a nonzero size of the element's own, which
// shows text an ancestor's zero size hid. A value that is neither, such
// as inherit, a size relative to the parent's, a function, or an
// unknown unit, leaves the inherited size alone.
func fontSizeZero(val string) (zero, reset bool) {
	if fontSizeKeywords[val] {
		return false, true
	}
	i := strings.IndexFunc(val, func(r rune) bool { return !strings.ContainsRune("+-.0123456789", r) })
	if i < 0 {
		i = len(val)
	}
	f, err := strconv.ParseFloat(val[:i], 64)
	if err != nil {
		return false, false
	}
	unit := val[i:]
	switch {
	case f == 0 && (unit == "%" || strings.TrimLeftFunc(unit, unicode.IsLetter) == ""):
		return true, false
	case f > 0 && fontSizeUnits[unit]:
		return false, true
	}
	return false, false
}

// nonSpaceRunes counts the characters of s that are not whitespace,
// which is how hidden text is measured.
func nonSpaceRunes(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
