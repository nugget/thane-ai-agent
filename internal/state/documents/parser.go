package documents

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

var (
	headingPattern   = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	markdownLinkRE   = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	wikiLinkRE       = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	frontmatterKeyRE = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.*)$`)
)

type parsedDocument struct {
	Title   string
	Summary string
	// Audience is the document's single audience frontmatter value
	// ("" when undeclared), promoted out of the frontmatter map so the
	// index can store it as a first-class column and exclude
	// audience: internal rows in SQL rather than after decoding every
	// row's frontmatter JSON (#1250, #1431).
	Audience string
	Facets   []string
	// FacetBytes is the UTF-8 byte length of each present projection's
	// content, keyed by facet key, with "full" always present. Byte
	// length, not rune count, deliberately: facet budgets elsewhere in
	// the contract are runes (display budgets), but these entries exist
	// so a context advertiser can estimate what materializing a level
	// costs against a byte-denominated context budget without any file
	// I/O — and a rune count under-estimates multi-byte UTF-8 content,
	// which the discriminator punishes with silent post-read drops.
	FacetBytes  map[string]int
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
	_, canonicalManifest, manifestErr := documentfacets.FromFrontmatter(meta)
	invalidManifest := canonicalManifest && manifestErr != nil
	contract := parsedFacetContract(meta, body)
	payload := documentfacets.Payload{Full: strings.TrimSpace(body)}
	faceted := !invalidManifest && len(contract.Facets) > 0
	if faceted {
		payload = contract.Parse(body)
	}
	logicalFull := payload.Full
	if invalidManifest {
		// Keep the document discoverable by path/title/tags without indexing
		// any private codec content. Direct reads carry the bounded repair
		// error; an empty projection-size map also prevents context offers.
		logicalFull = ""
	}
	sections := parseSections(logicalFull)
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

	// A faceted body carries its own authored snippet: the teaser is
	// written for exactly this surface, the status_line is the authored
	// fallback when no teaser is declared, and only an unfaceted body
	// falls back to the derived first paragraph. The present facets ride
	// along so a search hit can advertise which projections doc_read can
	// pull (#1250).
	summary := ""
	var facets []string
	facetBytes := make(map[string]int, len(documentfacets.Keys()))
	if faceted {
		for _, key := range documentfacets.Keys() {
			// full is every document's baseline and advertises nothing;
			// the facet list exists to say which condensed projections
			// doc_read can pull.
			if key == "full" {
				continue
			}
			if value, ok := payload.ByKey(key); ok && strings.TrimSpace(value) != "" {
				facets = append(facets, key)
				facetBytes[key] = len(value)
			}
		}
		if teaser, ok := payload.ByKey(string(documentfacets.Teaser)); ok && strings.TrimSpace(teaser) != "" {
			summary = strings.TrimSpace(teaser)
		} else if verdict, ok := payload.ByKey(string(documentfacets.StatusLine)); ok && strings.TrimSpace(verdict) != "" {
			summary = strings.TrimSpace(verdict)
		} else {
			summary = firstParagraph(payload.Full)
		}
	} else if !invalidManifest {
		summary = firstParagraph(body)
	}
	// full is measured for every document, faceted or not: the full body
	// is always a readable projection level, and an unfaceted body parses
	// entirely into payload.Full, so both branches share one source of
	// truth for what a full-level read yields.
	if !invalidManifest {
		facetBytes["full"] = len(logicalFull)
	}

	return parsedDocument{
		Title:       title,
		Summary:     summary,
		Audience:    firstValue(meta, audienceFrontmatterKey),
		Facets:      facets,
		FacetBytes:  facetBytes,
		WordCount:   len(strings.Fields(logicalFull)),
		Tags:        append([]string(nil), meta["tags"]...),
		Frontmatter: meta,
		Sections:    sections,
		Links:       parseLinks(logicalFull),
	}
}

// parsedFacetContract resolves a canonical durable manifest first and falls
// back to the historical reserved-heading envelope for compatibility reads.
// A malformed canonical manifest does not become an invented contract here;
// the record retains that invalid state so content-bearing reads fail closed
// and direct repair to the owning structured writer.
func parsedFacetContract(meta map[string][]string, body string) documentfacets.Contract {
	manifest, canonical, err := documentfacets.FromFrontmatter(meta)
	if canonical && err == nil {
		return manifest.Contract
	}
	if !canonical {
		manifest, _, found := documentfacets.InferLegacy(body, firstValue(meta, documentfacets.ManagedByKey))
		if found {
			return manifest.Contract
		}
	}
	return documentfacets.Contract{}
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
	pendingKey := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pendingKey != "" && strings.HasPrefix(trimmed, "-") {
			value := unquoteFrontmatterScalar(strings.TrimPrefix(trimmed, "-"))
			if value != "" {
				meta[pendingKey] = append(meta[pendingKey], value)
			}
			continue
		}
		m := frontmatterKeyRE.FindStringSubmatch(trimmed)
		if len(m) != 3 {
			pendingKey = ""
			continue
		}
		key := strings.TrimSpace(strings.ToLower(m[1]))
		value := parseFrontmatterValue(m[2])
		if len(value) == 0 {
			pendingKey = key
			if _, ok := meta[key]; !ok {
				meta[key] = nil
			}
			continue
		}
		meta[key] = value
		pendingKey = ""
	}
	for key, values := range meta {
		if len(values) == 0 {
			delete(meta, key)
			continue
		}
		meta[key] = dedupeSorted(values)
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
			part = unquoteFrontmatterScalar(part)
			if part != "" {
				values = append(values, part)
			}
		}
		return dedupeSorted(values)
	}
	value := unquoteFrontmatterScalar(raw)
	if value == "" {
		return nil
	}
	return []string{value}
}

// unquoteFrontmatterScalar reverses the quoting applied by
// renderFrontmatter, which writes every value through strconv.Quote.
// Rendered values are therefore double-quoted Go string literals, and
// strconv.Unquote inverts them exactly — including the backslash and
// embedded-quote escaping that, left un-reversed, re-escapes and doubles
// the value on every render→parse cycle. That asymmetry grew a
// loop-managed document's loop_intent into a 32 MiB run of backslashes on
// prod (knowledge/temporal/ranch-conditions.md, 2026-06): the intent held
// a literal "quoted phrase", strconv.Quote escaped the quotes to \", the
// old parser stripped only the outer quotes, and the owning service loop
// doubled the backslashes each iteration until the file hit the document
// read cap and wedged.
//
// Hand-authored frontmatter may instead use single quotes or bare,
// unquoted values; those are not valid Go literals, so they fall back to
// the historical quote-trim. This keeps the parser a strict superset of
// the previous behavior while making our own rendered output round-trip
// losslessly.
func unquoteFrontmatterScalar(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		if unquoted, err := strconv.Unquote(raw); err == nil {
			return unquoted
		}
	}
	return strings.Trim(raw, `"'`)
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
