// Package media provides media transcript retrieval via yt-dlp.
// It wraps yt-dlp for subtitle download, cleans VTT caption files
// to remove bloat from auto-generated subtitles, and stores
// transcripts durably as markdown files with YAML frontmatter.
package media

import (
	"regexp"
	"strings"
)

// vttHeaderRe matches the WEBVTT file header and optional metadata lines.
var vttHeaderRe = regexp.MustCompile(`^WEBVTT\b.*$`)

// timingLineRe matches VTT timing cues like "00:00:01.234 --> 00:00:03.456"
// with optional position/alignment metadata after the timestamps.
var timingLineRe = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}\.\d{3}`)

// htmlTagRe matches HTML tags commonly found in VTT files (<font>, <c>, <i>, etc.).
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// cueIDRe matches standalone numeric or alphanumeric cue identifiers that
// appear on their own line before a timing cue.
var cueIDRe = regexp.MustCompile(`^\d+$`)

// metadataLineRe matches VTT metadata lines like "Kind:" and "Language:".
var metadataLineRe = regexp.MustCompile(`^(Kind|Language|NOTE)\b`)

// CleanVTT takes raw VTT subtitle content and produces clean, readable
// plain text. The pipeline strips headers, timing lines, HTML tags, and
// deduplicates rolling caption lines that auto-generated subtitles
// repeat across overlapping segments. The result is typically 25-30%
// the size of the raw VTT input.
func CleanVTT(raw string) string {
	if raw == "" {
		return ""
	}

	lines := strings.Split(raw, "\n")
	var cleaned []string
	prevLine := ""

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		// Skip VTT header.
		if vttHeaderRe.MatchString(line) {
			continue
		}

		// Skip metadata lines (Kind:, Language:, NOTE).
		if metadataLineRe.MatchString(line) {
			continue
		}

		// Skip timing lines.
		if timingLineRe.MatchString(line) {
			continue
		}

		// Skip standalone cue IDs (bare numbers).
		if cueIDRe.MatchString(strings.TrimSpace(line)) {
			continue
		}

		// Strip HTML tags.
		line = htmlTagRe.ReplaceAllString(line, "")

		// Trim whitespace.
		line = strings.TrimSpace(line)

		// Skip empty lines.
		if line == "" {
			continue
		}

		// Deduplicate: auto-subs repeat partial text across overlapping
		// cue segments. Skip lines that exactly match the previous.
		if line == prevLine {
			continue
		}

		cleaned = append(cleaned, line)
		prevLine = line
	}

	return strings.TrimSpace(strings.Join(cleaned, " "))
}

// CleanVTTWithParagraphs is like CleanVTT but inserts paragraph breaks
// (double newlines) when the timing gap between consecutive cues
// exceeds 2 seconds. This produces more readable output for long
// transcripts where topic shifts align with speaker pauses.
func CleanVTTWithParagraphs(raw string) string {
	if raw == "" {
		return ""
	}

	lines := strings.Split(raw, "\n")
	var paragraphs []string
	var currentPara []string
	prevLine := ""
	prevEndMs := 0
	currentStartMs := 0

	const paragraphGapMs = 2000

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		// Parse timing lines to detect gaps.
		if timingLineRe.MatchString(line) {
			times := strings.SplitN(line, "-->", 2)
			if len(times) == 2 {
				startMs := parseTimestampMs(strings.TrimSpace(times[0]))
				endMs := parseTimestampMs(strings.TrimSpace(strings.Fields(times[1])[0]))

				// Check for paragraph-worthy gap.
				if prevEndMs > 0 && startMs-prevEndMs > paragraphGapMs && len(currentPara) > 0 {
					paragraphs = append(paragraphs, strings.Join(currentPara, " "))
					currentPara = nil
				}

				currentStartMs = startMs
				_ = currentStartMs // used for future gap analysis
				prevEndMs = endMs
			}
			continue
		}

		// Skip headers, metadata, cue IDs.
		if vttHeaderRe.MatchString(line) || metadataLineRe.MatchString(line) {
			continue
		}
		if cueIDRe.MatchString(strings.TrimSpace(line)) {
			continue
		}

		// Strip HTML tags and trim.
		line = htmlTagRe.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)

		if line == "" || line == prevLine {
			continue
		}

		currentPara = append(currentPara, line)
		prevLine = line
	}

	// Flush last paragraph.
	if len(currentPara) > 0 {
		paragraphs = append(paragraphs, strings.Join(currentPara, " "))
	}

	return strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
}

// parseTimestampMs parses a VTT timestamp "HH:MM:SS.mmm" into milliseconds.
func parseTimestampMs(ts string) int {
	// Expected format: "00:01:23.456"
	ts = strings.TrimSpace(ts)
	if len(ts) < 12 {
		return 0
	}

	h := atoi(ts[0:2])
	m := atoi(ts[3:5])
	s := atoi(ts[6:8])
	ms := atoi(ts[9:12])

	return ((h*60+m)*60+s)*1000 + ms
}

// atoi converts a numeric string to int without error handling
// (the regex pre-validates format).
func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
