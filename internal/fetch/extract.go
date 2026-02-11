package fetch

import (
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skipElements are HTML elements whose content should be excluded.
var skipElements = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Noscript: true,
	atom.Iframe:   true,
	atom.Svg:      true,
	atom.Head:     true, // We extract title separately
	atom.Nav:      true,
	atom.Footer:   true,
	atom.Header:   true,
}

// extractHTML parses HTML and returns (title, readable text content).
func extractHTML(raw string) (string, string) {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		// Fallback: strip tags naively
		return "", stripTags(raw)
	}

	var title string
	var content strings.Builder

	// First pass: find title
	title = findTitle(doc)

	// Second pass: extract text
	extractText(doc, &content, false)

	// Clean up whitespace
	text := cleanWhitespace(content.String())
	return title, text
}

// findTitle walks the DOM looking for a <title> element.
func findTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.DataAtom == atom.Title {
		return getTextContent(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := findTitle(c); t != "" {
			return t
		}
	}
	return ""
}

// getTextContent returns concatenated text of all children.
func getTextContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(getTextContent(c))
	}
	return b.String()
}

// extractText recursively extracts visible text from the DOM.
func extractText(n *html.Node, w *strings.Builder, skip bool) {
	if skip {
		return
	}

	if n.Type == html.ElementNode {
		if skipElements[n.DataAtom] {
			return
		}
		// Add line breaks for block elements
		if isBlockElement(n.DataAtom) && w.Len() > 0 {
			w.WriteString("\n\n")
		}
	}

	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			w.WriteString(text)
			w.WriteString(" ")
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractText(c, w, false)
	}

	// Add newline after certain inline-block elements
	if n.Type == html.ElementNode && (n.DataAtom == atom.Br || n.DataAtom == atom.Li) {
		w.WriteString("\n")
	}
}

// isBlockElement returns true for elements that typically render as blocks.
func isBlockElement(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Main,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Blockquote, atom.Pre, atom.Ul, atom.Ol, atom.Table,
		atom.Tr, atom.Dl, atom.Dd, atom.Dt, atom.Figcaption, atom.Figure,
		atom.Details, atom.Summary, atom.Hr:
		return true
	}
	return false
}

// cleanWhitespace normalizes whitespace in extracted text.
func cleanWhitespace(s string) string {
	// Collapse runs of spaces/tabs within lines
	lines := strings.Split(s, "\n")
	var cleaned []string
	prevEmpty := false

	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if prevEmpty {
				continue // Skip consecutive blank lines
			}
			prevEmpty = true
		} else {
			prevEmpty = false
		}
		cleaned = append(cleaned, line)
	}

	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

// stripTags is a fallback that removes HTML tags naively.
func stripTags(s string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			if tokenizer.Err() == io.EOF {
				return cleanWhitespace(b.String())
			}
			return cleanWhitespace(b.String())
		case html.TextToken:
			b.WriteString(tokenizer.Token().Data)
			b.WriteString(" ")
		}
	}
}
