package promptfmt

import (
	"strings"
)

// AppendMarkdownSection appends a markdown heading and body to sb.
// It returns false when body is blank. The helper keeps section spacing
// consistent for generated model-facing context without making every
// caller hand-roll heading punctuation.
func AppendMarkdownSection(sb *strings.Builder, level int, title string, body string) bool {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" || body == "" {
		return false
	}
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	sb.WriteString(strings.Repeat("#", level))
	sb.WriteByte(' ')
	sb.WriteString(title)
	sb.WriteString("\n\n")
	sb.WriteString(body)
	return true
}
