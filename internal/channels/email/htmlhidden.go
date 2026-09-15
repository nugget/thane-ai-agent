package email

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Hidden text is text an HTML body's own markup keeps a person reading
// the message from seeing, which a renderer that ignored the markup
// would hand the model as ordinary body text (#1559). The renderer
// recognises the inline idioms senders use for it and nothing more:
// the hidden attribute, aria-hidden="true", and an inline style of
// display:none, visibility:hidden or collapse, a zero font-size, or a
// zero opacity. Within one style attribute each of those properties is
// judged by the declaration a browser applies (see [parseInlineStyle]).
// It reads no stylesheet and compares no colours; that would be layout.

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

// styled applies one inline style attribute. Each property is judged by
// its winning declaration, so a later display:block undoes an earlier
// display:none unless the earlier one is !important. A property with no
// declaration the renderer recognises changes nothing.
func (v visibility) styled(style string) visibility {
	s := parseInlineStyle(style)
	if s.display.effect == effectHide || s.opacity.effect == effectHide {
		v.removed = true
	}
	v.invisible = s.visibility.apply(v.invisible)
	v.fontZero = s.fontSize.apply(v.fontZero)
	return v
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

// fontSizeRelativeUnits are the units a font-size scales the parent's
// size in, so the element keeps whatever size it inherits.
var fontSizeRelativeUnits = map[string]bool{"em": true, "ex": true, "ch": true, "%": true}

// fontSizeEffect reads a font-size value. A size of zero in any unit
// hides text, and a nonzero size of the element's own shows text an
// ancestor's zero size hid. inherit, smaller, larger, and a size relative
// to the parent's keep the inherited size. A function, a negative size,
// or an unknown unit is not recognised; math is not either, because not
// every engine accepts it.
func fontSizeEffect(val string) (styleEffect, bool) {
	switch {
	case fontSizeKeywords[val]:
		return effectShow, true
	case val == "smaller", val == "larger", cssWideKeyword(val):
		return effectKeep, true
	}
	f, unit, ok := cssNumber(val)
	if !ok {
		return effectKeep, false
	}
	switch {
	case f == 0 && (unit == "%" || strings.TrimLeftFunc(unit, unicode.IsLetter) == ""):
		return effectHide, true
	case f > 0 && fontSizeUnits[unit]:
		return effectShow, true
	case f > 0 && fontSizeRelativeUnits[unit]:
		return effectKeep, true
	}
	return effectKeep, false
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
