package email

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlToText renders an HTML body as plain text for a reader that
// cannot see markup: script and style content is dropped, block
// elements become line breaks, list items get a marker, and links
// keep their target in parentheses when it differs from their text.
// Runs of blank lines collapse to one. It is deliberately conservative
// — the goal is a faithful, readable rendering, not layout.
//
// Faithful means the text a person reading the message would see. Text
// the markup hides from that person through an inline idiom (see
// [visibility]) is withheld, and hiddenChars counts its characters,
// whitespace aside, so the caller can say something was hidden instead
// of dropping it silently. A link whose text is all hidden withholds
// its target too.
//
// The body is parsed into a tree the way a browser parses it, so an
// element a sender leaves unclosed ends where a browser would end it.
func htmlToText(src string) (text string, hiddenChars int) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		// A strings.Reader never fails, so the parser recovered from a
		// panic on this input. Any other rendering could let hidden text
		// through, so the body says what happened instead.
		return "[the HTML body could not be rendered: " + err.Error() + "]", 0
	}
	var r htmlRenderer
	r.children(doc, visibility{})
	return strings.TrimSpace(r.sb.String()), r.hiddenChars
}

// htmlRenderer accumulates the rendering of one parsed HTML body.
type htmlRenderer struct {
	sb           strings.Builder
	preDepth     int
	pendingSpace bool

	// inLink is set while an anchor's content renders, and linkText
	// collects its visible text so the target is shown only when it
	// differs from that text. linkImage records a visible image inside
	// the anchor, which a reader sees and can follow even without alt
	// text.
	inLink    bool
	linkText  strings.Builder
	linkImage bool

	// hiddenChars counts the non-whitespace characters withheld because
	// the markup hid them.
	hiddenChars int
}

func (r *htmlRenderer) children(n *html.Node, vis visibility) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			r.text(c.Data, vis)
		case html.ElementNode:
			r.element(c, vis)
		}
	}
}

// text renders a text node, or counts it when it is hidden. The parser
// has already decoded entities; decoding again would turn a literal
// "&amp;amp;" in the mail into "&".
func (r *htmlRenderer) text(s string, vis visibility) {
	if vis.textHidden() {
		r.hiddenChars += nonSpaceRunes(s)
		return
	}
	if r.inLink {
		r.linkText.WriteString(s)
	}
	r.write(s)
}

func (r *htmlRenderer) element(n *html.Node, inherited visibility) {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Title, atom.Noscript, atom.Template:
		// Nothing in these is text a reader sees, hidden or not.
		return
	}
	vis := inherited.within(n)
	switch {
	case vis.removed:
		// A removed element takes no space, so it adds no line breaks;
		// its text and image descriptions are only counted.
		if n.DataAtom == atom.Img {
			r.hiddenChars += nonSpaceRunes(attr(n, "alt"))
		}
		r.children(n, vis)
	case n.DataAtom == atom.A:
		r.link(n, vis)
	case n.DataAtom == atom.Img:
		r.image(n, vis)
	default:
		r.open(n, vis)
		r.children(n, vis)
		r.close(n)
	}
}

// isBlock reports whether an element starts and ends a paragraph.
func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Blockquote, atom.Table, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Ul, atom.Ol,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}

// open writes what an element contributes before its content.
func (r *htmlRenderer) open(n *html.Node, vis visibility) {
	switch {
	case isBlock(n.DataAtom):
		r.paragraph()
	case n.DataAtom == atom.Br, n.DataAtom == atom.Tr:
		r.newline()
	case n.DataAtom == atom.Li:
		r.newline()
		if !vis.textHidden() {
			r.sb.WriteString("- ")
		}
	case n.DataAtom == atom.Td, n.DataAtom == atom.Th:
		r.pendingSpace = false
		if s := r.sb.String(); s != "" && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\t") {
			r.sb.WriteByte('\t')
		}
	case n.DataAtom == atom.Pre:
		r.paragraph()
		r.preDepth++
	case n.DataAtom == atom.Hr:
		r.paragraph()
		if !vis.boxHidden() {
			r.sb.WriteString("---")
		}
		r.paragraph()
	}
}

// close writes what an element contributes after its content.
func (r *htmlRenderer) close(n *html.Node) {
	switch {
	case isBlock(n.DataAtom):
		r.paragraph()
	case n.DataAtom == atom.Tr, n.DataAtom == atom.Li:
		r.newline()
	case n.DataAtom == atom.Pre:
		r.preDepth--
		r.paragraph()
	}
}

// link renders an anchor's content, then its target in parentheses
// when a reader sees a link whose text is not the target itself. A
// link whose content the markup hid entirely shows nothing, so its
// target is withheld with it; so is an anchor with no visible content
// inside hidden text, which a reader cannot see either.
func (r *htmlRenderer) link(n *html.Node, vis visibility) {
	href := attr(n, "href")
	if href == "" || r.inLink {
		r.children(n, vis)
		return
	}
	start, hiddenBefore := r.sb.Len(), r.hiddenChars
	r.inLink = true
	r.linkText.Reset()
	r.linkImage = false
	r.children(n, vis)
	r.inLink = false

	shown := strings.TrimSpace(r.sb.String()[start:]) != "" || r.linkImage
	if vis.boxHidden() || (!shown && (vis.textHidden() || r.hiddenChars > hiddenBefore)) {
		return
	}
	text := strings.TrimSpace(r.linkText.String())
	if strings.HasPrefix(strings.ToLower(href), "mailto:") || strings.EqualFold(text, href) || strings.HasPrefix(href, "#") {
		return
	}
	r.write(" (" + href + ")")
}

// image renders an image as its alt text. A zero font-size does not
// hide an image, so only a hidden box withholds the description.
func (r *htmlRenderer) image(n *html.Node, vis visibility) {
	if r.inLink && !vis.boxHidden() {
		r.linkImage = true
	}
	alt := attr(n, "alt")
	switch {
	case alt == "":
	case vis.boxHidden():
		r.hiddenChars += nonSpaceRunes(alt)
	default:
		r.write("[" + alt + "]")
	}
}

// write appends inline text. Whitespace inside HTML text is
// insignificant beyond separating words, so runs collapse to one
// space, and a space that ended one text node is remembered so the
// next node (after an inline tag) is still separated from it.
func (r *htmlRenderer) write(text string) {
	if r.preDepth > 0 {
		r.sb.WriteString(text)
		r.pendingSpace = false
		return
	}
	if strings.TrimSpace(text) == "" {
		if text != "" {
			r.pendingSpace = true
		}
		return
	}
	first, _ := utf8.DecodeRuneInString(text)
	last, _ := utf8.DecodeLastRuneInString(text)
	leading := unicode.IsSpace(first)
	trailing := unicode.IsSpace(last)
	words := strings.Join(strings.Fields(text), " ")
	if r.sb.Len() > 0 && (r.pendingSpace || leading) {
		s := r.sb.String()
		if end := s[len(s)-1]; end != '\n' && end != ' ' && end != '\t' {
			r.sb.WriteByte(' ')
		}
	}
	r.sb.WriteString(words)
	r.pendingSpace = trailing
}

func (r *htmlRenderer) newline() {
	r.pendingSpace = false
	s := r.sb.String()
	if s == "" || strings.HasSuffix(s, "\n") {
		return
	}
	r.sb.WriteByte('\n')
}

func (r *htmlRenderer) paragraph() {
	r.pendingSpace = false
	s := r.sb.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		r.sb.WriteByte('\n')
		return
	}
	r.sb.WriteString("\n\n")
}

// attr returns the trimmed value of n's named attribute, or empty.
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == name {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}
