package documents

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func cloneFrontmatter(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func normalizeFrontmatterValues(values []string) []string {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			clean = append(clean, value)
		}
	}
	return dedupeSorted(clean)
}

func renderDocument(meta map[string][]string, body string) string {
	return renderDocumentFromParts(renderFrontmatter(meta), body)
}

func renderDocumentFromParts(frontmatterRaw, body string) string {
	body = trimDocumentBody(body)
	if strings.TrimSpace(frontmatterRaw) == "" {
		if body == "" {
			return ""
		}
		return body + "\n"
	}
	if body == "" {
		return "---\n" + strings.Trim(frontmatterRaw, "\n") + "\n---\n"
	}
	return "---\n" + strings.Trim(frontmatterRaw, "\n") + "\n---\n\n" + body + "\n"
}

func renderFrontmatter(meta map[string][]string) string {
	if len(meta) == 0 {
		return ""
	}
	priority := map[string]int{
		"title":       0,
		"description": 1,
		"tags":        2,
		"created":     3,
		"updated":     4,
	}
	keys := make([]string, 0, len(meta))
	for key, values := range meta {
		if len(values) == 0 {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, iok := priority[keys[i]]
		pj, jok := priority[keys[j]]
		switch {
		case iok && jok:
			return pi < pj
		case iok:
			return true
		case jok:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		values := meta[key]
		switch {
		case len(values) == 1:
			lines = append(lines, fmt.Sprintf("%s: %s", key, strconv.Quote(values[0])))
		case len(values) > 1:
			quoted := make([]string, 0, len(values))
			for _, value := range values {
				quoted = append(quoted, strconv.Quote(value))
			}
			lines = append(lines, fmt.Sprintf("%s: [%s]", key, strings.Join(quoted, ", ")))
		}
	}
	return strings.Join(lines, "\n")
}

func touchDocumentFrontmatter(frontmatterRaw string, record *DocumentRecord, now time.Time) string {
	meta := parseFrontmatterMap(frontmatterRaw)
	if len(meta) == 0 && record != nil {
		meta = cloneFrontmatter(record.Frontmatter)
	}
	created := firstValue(meta, "created")
	if created == "" {
		created = now.Format(time.RFC3339)
	}
	meta["created"] = []string{created}
	meta["updated"] = []string{now.Format(time.RFC3339)}
	return renderFrontmatter(meta)
}

func trimDocumentBody(body string) string {
	return strings.Trim(body, "\n")
}

func appendDocumentBody(body, content string) string {
	content = trimDocumentBody(content)
	if content == "" {
		return trimDocumentBody(body)
	}
	body = trimDocumentBody(body)
	if body == "" {
		return content
	}
	return body + "\n\n" + content
}

func prependDocumentBody(body, content string) string {
	content = trimDocumentBody(content)
	if content == "" {
		return trimDocumentBody(body)
	}
	body = trimDocumentBody(body)
	if body == "" {
		return content
	}
	return content + "\n\n" + body
}

func upsertDocumentSection(body, selector, heading string, level int, content string) (string, string, error) {
	selector = strings.TrimSpace(selector)
	heading = strings.TrimSpace(heading)
	if heading == "" {
		heading = selector
	}
	if heading == "" {
		return "", "", fmt.Errorf("section heading is required")
	}
	if level <= 0 || level > 6 {
		level = 2
	}
	lines := strings.Split(trimDocumentBody(body), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	target, found := findSection(parseSections(strings.Join(lines, "\n")), selector, heading)
	blockLines := strings.Split(renderSectionBlock(level, heading, content), "\n")
	var out []string
	if found {
		start := target.StartLine - 1
		end := target.EndLine
		out = append(out, lines[:start]...)
		out = trimTrailingBlankLines(out)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, blockLines...)
		after := trimLeadingBlankLines(lines[end:])
		if len(after) > 0 {
			out = append(out, "")
			out = append(out, after...)
		}
	} else {
		out = append(out, trimTrailingBlankLines(lines)...)
		if len(out) > 0 {
			out = append(out, "")
			out = append(out, "")
		}
		out = append(out, blockLines...)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n"), heading, nil
}

func deleteDocumentSection(body, selector string) (string, string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", "", fmt.Errorf("section is required")
	}
	lines := strings.Split(trimDocumentBody(body), "\n")
	target, found := findSection(parseSections(strings.Join(lines, "\n")), selector, "")
	if !found {
		return "", "", fmt.Errorf("section %q not found", selector)
	}
	start := target.StartLine - 1
	end := target.EndLine
	out := append([]string{}, lines[:start]...)
	after := trimLeadingBlankLines(lines[end:])
	out = trimTrailingBlankLines(out)
	if len(after) > 0 && len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, after...)
	return strings.Trim(strings.Join(out, "\n"), "\n"), target.Heading, nil
}

func findSection(sections []Section, selector, heading string) (Section, bool) {
	targets := make([]string, 0, 2)
	if selector != "" {
		targets = append(targets, selector, slugify(selector))
	}
	if heading != "" {
		targets = append(targets, heading, slugify(heading))
	}
	for _, sec := range sections {
		for _, target := range targets {
			if target == "" {
				continue
			}
			if strings.EqualFold(sec.Heading, target) || sec.Slug == slugify(target) {
				return sec, true
			}
		}
	}
	return Section{}, false
}

func renderSectionBlock(level int, heading, content string) string {
	content = trimDocumentBody(content)
	header := strings.Repeat("#", level) + " " + heading
	if content == "" {
		return header
	}
	return header + "\n\n" + content
}

func trimLeadingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

func trimTrailingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func normalizeJournalWindow(window string) string {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case "week", "weekly":
		return "week"
	case "month", "monthly":
		return "month"
	default:
		return "day"
	}
}

func defaultJournalWindowLimit(window string) int {
	switch window {
	case "week":
		return 8
	case "month":
		return 12
	default:
		return 7
	}
}

func journalWindowHeading(now time.Time, window string) string {
	switch window {
	case "week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := now.AddDate(0, 0, -(weekday - 1))
		return "Week of " + start.Format("2006-01-02")
	case "month":
		return now.Format("2006-01")
	default:
		return now.Format("2006-01-02")
	}
}

func formatJournalEntry(now time.Time, entry string) string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return ""
	}
	lines := strings.Split(entry, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	if len(lines) == 1 {
		return "- " + now.Format("2006-01-02 15:04 MST") + ": " + lines[0]
	}
	out := []string{"- " + now.Format("2006-01-02 15:04 MST") + ":"}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "  ")
			continue
		}
		out = append(out, "  "+line)
	}
	return strings.Join(out, "\n")
}

func currentSectionBody(body, heading string) string {
	section, found := findSection(parseSections(body), heading, heading)
	if !found {
		return ""
	}
	lines := strings.Split(section.Content, "\n")
	if len(lines) <= 1 {
		return ""
	}
	return strings.Trim(strings.Join(lines[1:], "\n"), "\n")
}

func appendJournalEntryBody(existing, entry string) string {
	existing = trimDocumentBody(existing)
	entry = trimDocumentBody(entry)
	if existing == "" {
		return entry
	}
	return existing + "\n" + entry
}

func pruneJournalWindows(body string, level int, window string, maxWindows int) string {
	if maxWindows <= 0 {
		return body
	}
	sections := parseSections(body)
	type candidate struct {
		heading string
		at      time.Time
	}
	var windows []candidate
	for _, sec := range sections {
		if sec.Level != level {
			continue
		}
		at, ok := parseJournalWindowHeading(sec.Heading, window)
		if !ok {
			continue
		}
		windows = append(windows, candidate{heading: sec.Heading, at: at})
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].at.After(windows[j].at) })
	if len(windows) <= maxWindows {
		return body
	}
	keep := make(map[string]bool, maxWindows)
	for _, win := range windows[:maxWindows] {
		keep[win.heading] = true
	}
	pruned := body
	for _, win := range windows[maxWindows:] {
		if keep[win.heading] {
			continue
		}
		next, _, err := deleteDocumentSection(pruned, win.heading)
		if err == nil {
			pruned = next
		}
	}
	return pruned
}

func parseJournalWindowHeading(heading, window string) (time.Time, bool) {
	switch window {
	case "week":
		if !strings.HasPrefix(heading, "Week of ") {
			return time.Time{}, false
		}
		t, err := time.Parse("2006-01-02", strings.TrimPrefix(heading, "Week of "))
		return t, err == nil
	case "month":
		t, err := time.Parse("2006-01", heading)
		return t, err == nil
	default:
		t, err := time.Parse("2006-01-02", heading)
		return t, err == nil
	}
}
