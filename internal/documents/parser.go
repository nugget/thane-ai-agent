package documents

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	headingPattern   = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	markdownLinkRE   = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	wikiLinkRE       = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	frontmatterKeyRE = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.*)$`)
)

type parsedDocument struct {
	Title       string
	Summary     string
	WordCount   int
	Tags        []string
	Frontmatter map[string][]string
	Sections    []Section
	Links       []string
}

// Section captures one heading-defined region in a markdown document.
type Section struct {
	Heading   string `json:"heading"`
	Slug      string `json:"slug"`
	Level     int    `json:"level"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content,omitempty"`
}

func parseMarkdownDocument(name, raw string) parsedDocument {
	meta, body := splitFrontmatter(raw)
	return parseMarkdownDocumentParts(name, meta, body)
}

func parseMarkdownDocumentParts(name string, meta map[string][]string, body string) parsedDocument {
	sections := parseSections(body)
	title := firstValue(meta, "title")
	if title == "" {
		for _, sec := range sections {
			if sec.Level == 1 {
				title = sec.Heading
				break
			}
		}
	}
	if title == "" {
		base := filepath.Base(name)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		title = base
	}

	return parsedDocument{
		Title:       title,
		Summary:     firstParagraph(body),
		WordCount:   len(strings.Fields(body)),
		Tags:        append([]string(nil), meta["tags"]...),
		Frontmatter: meta,
		Sections:    sections,
		Links:       parseLinks(body),
	}
}

func splitFrontmatter(raw string) (map[string][]string, string) {
	if !strings.HasPrefix(raw, "---") {
		return map[string][]string{}, raw
	}
	rest := strings.TrimLeft(raw[3:], " \t")
	switch {
	case strings.HasPrefix(rest, "\r\n"):
		rest = rest[2:]
	case strings.HasPrefix(rest, "\n"):
		rest = rest[1:]
	default:
		return map[string][]string{}, raw
	}
	closeIdx, closeLen := findFrontmatterClose(rest)
	if closeIdx < 0 {
		return map[string][]string{}, raw
	}

	meta := parseFrontmatterMap(rest[:closeIdx])
	body := strings.TrimLeft(rest[closeIdx+closeLen:], "\r\n")
	return meta, body
}

func findFrontmatterClose(rest string) (int, int) {
	lfIdx := strings.Index(rest, "\n---")
	crlfIdx := strings.Index(rest, "\r\n---")
	switch {
	case lfIdx < 0 && crlfIdx < 0:
		return -1, 0
	case lfIdx < 0:
		return crlfIdx, len("\r\n---")
	case crlfIdx < 0:
		return lfIdx, len("\n---")
	case crlfIdx < lfIdx:
		return crlfIdx, len("\r\n---")
	default:
		return lfIdx, len("\n---")
	}
}

func parseFrontmatterMap(raw string) map[string][]string {
	meta := make(map[string][]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := frontmatterKeyRE.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(m[1]))
		value := parseFrontmatterValue(m[2])
		if len(value) == 0 {
			continue
		}
		meta[key] = value
	}
	return meta
}

func parseFrontmatterValue(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimPrefix(raw, "[")
		raw = strings.TrimSuffix(raw, "]")
		parts := strings.Split(raw, ",")
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.Trim(strings.TrimSpace(part), `"'`)
			if part != "" {
				values = append(values, part)
			}
		}
		return dedupeSorted(values)
	}
	value := strings.Trim(raw, `"'`)
	if value == "" {
		return nil
	}
	return []string{value}
}

func parseSections(body string) []Section {
	lines := strings.Split(body, "\n")
	var headings []Section
	for i, line := range lines {
		m := headingPattern.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		heading := strings.TrimSpace(m[2])
		headings = append(headings, Section{
			Heading:   heading,
			Slug:      slugify(heading),
			Level:     len(m[1]),
			StartLine: i + 1,
		})
	}
	if len(headings) == 0 {
		return nil
	}
	for i := range headings {
		endLine := len(lines)
		for j := i + 1; j < len(headings); j++ {
			if headings[j].Level <= headings[i].Level {
				endLine = headings[j].StartLine - 1
				break
			}
		}
		headings[i].EndLine = endLine
		headings[i].Content = strings.TrimRight(strings.Join(lines[headings[i].StartLine-1:endLine], "\n"), "\n")
	}
	return headings
}

func firstParagraph(body string) string {
	lines := strings.Split(body, "\n")
	var para []string
	inCode := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			if len(para) > 0 {
				break
			}
			continue
		}
		if inCode {
			continue
		}
		if trimmed == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		if headingPattern.MatchString(trimmed) {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, trimmed)
	}
	return strings.Join(para, " ")
}

func parseLinks(body string) []string {
	var links []string
	for _, m := range markdownLinkRE.FindAllStringSubmatch(body, -1) {
		if len(m) == 2 {
			target := strings.TrimSpace(m[1])
			if target != "" {
				links = append(links, target)
			}
		}
	}
	for _, m := range wikiLinkRE.FindAllStringSubmatch(body, -1) {
		if len(m) == 2 {
			target := strings.TrimSpace(m[1])
			if target != "" {
				links = append(links, target)
			}
		}
	}
	return dedupeSorted(links)
}

func slugify(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var out []rune
	prevDash := false
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out = append(out, r)
			prevDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/':
			if !prevDash && len(out) > 0 {
				out = append(out, '-')
				prevDash = true
			}
		}
	}
	result := strings.Trim(string(out), "-")
	if result == "" {
		return "section"
	}
	return result
}

func firstValue(meta map[string][]string, key string) string {
	values := meta[strings.ToLower(strings.TrimSpace(key))]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
