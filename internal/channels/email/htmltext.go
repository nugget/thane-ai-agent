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
func htmlToText(src string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(src))
	var (
		sb           strings.Builder
		skipDepth    int
		preDepth     int
		href         string
		linkText     strings.Builder
		inLink       bool
		pendingSpace bool
	)

	// writeText appends inline text. Whitespace inside HTML text is
	// insignificant beyond separating words, so runs collapse to one
	// space, and a space that ended one text node is remembered so the
	// next node (after an inline tag) is still separated from it.
	writeText := func(text string) {
		if preDepth > 0 {
			sb.WriteString(text)
			pendingSpace = false
			return
		}
		if strings.TrimSpace(text) == "" {
			if text != "" {
				pendingSpace = true
			}
			return
		}
		first, _ := utf8.DecodeRuneInString(text)
		last, _ := utf8.DecodeLastRuneInString(text)
		leading := unicode.IsSpace(first)
		trailing := unicode.IsSpace(last)
		words := strings.Join(strings.Fields(text), " ")
		if sb.Len() > 0 && (pendingSpace || leading) {
			last := sb.String()[sb.Len()-1]
			if last != '\n' && last != ' ' && last != '\t' {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(words)
		pendingSpace = trailing
	}
	newline := func() {
		pendingSpace = false
		s := sb.String()
		if s == "" || strings.HasSuffix(s, "\n") {
			return
		}
		sb.WriteByte('\n')
	}
	paragraph := func() {
		pendingSpace = false
		s := sb.String()
		if s == "" || strings.HasSuffix(s, "\n\n") {
			return
		}
		if strings.HasSuffix(s, "\n") {
			sb.WriteByte('\n')
			return
		}
		sb.WriteString("\n\n")
	}

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return strings.TrimSpace(sb.String())
		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			// Text() already decodes entities; decoding again would turn
			// a literal "&amp;amp;" in the mail into "&".
			text := string(tokenizer.Text())
			if inLink {
				linkText.WriteString(text)
			}
			writeText(text)
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := tokenizer.Token()
			switch tok.DataAtom {
			case atom.Script, atom.Style, atom.Title, atom.Noscript, atom.Template:
				// head itself is not skipped: real mail leaves it unclosed
				// often enough that skipping it would drop the body.
				if tt == html.StartTagToken {
					skipDepth++
				}
			case atom.Br:
				newline()
			case atom.P, atom.Div, atom.Blockquote, atom.Table, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Ul, atom.Ol:
				paragraph()
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				paragraph()
			case atom.Tr:
				newline()
			case atom.Li:
				newline()
				sb.WriteString("- ")
			case atom.Td, atom.Th:
				pendingSpace = false
				if s := sb.String(); s != "" && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\t") {
					sb.WriteByte('\t')
				}
			case atom.Pre:
				paragraph()
				preDepth++
			case atom.Hr:
				paragraph()
				sb.WriteString("---")
				paragraph()
			case atom.A:
				if skipDepth == 0 {
					href = attr(tok, "href")
					inLink = href != ""
					linkText.Reset()
				}
			case atom.Img:
				if alt := attr(tok, "alt"); alt != "" && skipDepth == 0 {
					writeText("[" + alt + "]")
				}
			}
		case html.EndTagToken:
			tok := tokenizer.Token()
			switch tok.DataAtom {
			case atom.Script, atom.Style, atom.Title, atom.Noscript, atom.Template:
				if skipDepth > 0 {
					skipDepth--
				}
			case atom.P, atom.Div, atom.Blockquote, atom.Table, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Ul, atom.Ol:
				paragraph()
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				paragraph()
			case atom.Tr, atom.Li:
				newline()
			case atom.Pre:
				if preDepth > 0 {
					preDepth--
				}
				paragraph()
			case atom.A:
				if inLink {
					text := strings.TrimSpace(linkText.String())
					target := strings.TrimSpace(href)
					if target != "" && !strings.HasPrefix(strings.ToLower(target), "mailto:") && !strings.EqualFold(text, target) && !strings.HasPrefix(target, "#") {
						writeText(" (" + target + ")")
					}
					inLink = false
					href = ""
				}
			}
		}
	}
}

// attr returns the value of the named attribute on tok, or empty.
func attr(tok html.Token, name string) string {
	for _, a := range tok.Attr {
		if a.Key == name {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}
