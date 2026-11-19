package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	resultCapBytes  = 16_000
	emptyResultText = "[tool returned no content]"
)

// resultTruncatedNote ends a result cut at resultCapBytes and names the
// remedy, so the model narrows its request instead of assuming it saw
// everything.
const resultTruncatedNote = "\n[result truncated at 16000 bytes; request less, for example with narrower arguments]"

// blockTypeCapBytes bounds a server-supplied block type echoed in an
// "omitted" marker.
const blockTypeCapBytes = 64

// renderToolResult renders a complete CallToolResult as model text,
// capped at resultCapBytes (rune-safe, with a truncation note naming the
// remedy). Never returns "" (emptyResultText instead). isError results
// render from content blocks only, never structuredContent.
//
// A successful result with structuredContent renders as that value's
// compact JSON when it fits within the cap, followed by one line per
// non-text block; text blocks are skipped because the spec has servers
// repeat the structured value in them. When the JSON does not fit but
// the content blocks do, the blocks are used instead.
func renderToolResult(res *callToolResult) string {
	if res == nil {
		return emptyResultText
	}
	blocks := renderBlocks(res.Content)
	if !res.IsError {
		if structured, ok := compactStructured(res.StructuredContent); ok {
			joinedBlocks := strings.Join(blocks.all, "\n")
			if len(structured) <= resultCapBytes || joinedBlocks == "" || len(joinedBlocks) > resultCapBytes {
				return capResult(strings.Join(append([]string{structured}, blocks.nonText...), "\n"))
			}
			return capResult(joinedBlocks)
		}
	}
	return capResult(strings.Join(blocks.all, "\n"))
}

// renderedBlocks holds each content block rendered as text, in order,
// and the subset that are not text blocks.
type renderedBlocks struct {
	all     []string
	nonText []string
}

func renderBlocks(raws []json.RawMessage) renderedBlocks {
	var out renderedBlocks
	for _, raw := range raws {
		text, isText := renderBlock(raw)
		if text == "" {
			continue
		}
		out.all = append(out.all, text)
		if !isText {
			out.nonText = append(out.nonText, text)
		}
	}
	return out
}

// renderBlock renders one content block. isText reports a text block,
// whose text is used verbatim.
func renderBlock(raw json.RawMessage) (text string, isText bool) {
	var b contentBlock
	if err := json.Unmarshal(raw, &b); err != nil {
		return omittedMarker(blockType(raw)), false
	}
	switch b.Type {
	case "text":
		return b.Text, true
	case "image", "audio":
		return fmt.Sprintf("[%s (%s) omitted; not viewable through this tool]",
			b.Type, withMime(b.MimeType, fmt.Sprintf("%d bytes", base64DecodedLen(b.Data)))), false
	case "resource_link":
		return "[resource link: " + joinNonEmpty(b.Name, b.URI) + mimeSuffix(b.MimeType) + "]", false
	case "resource":
		return renderEmbeddedResource(b.Resource), false
	default:
		return omittedMarker(b.Type), false
	}
}

func renderEmbeddedResource(r *embeddedResource) string {
	if r == nil {
		return omittedMarker("resource")
	}
	head := "[resource"
	if r.URI != "" {
		head += " " + r.URI
	}
	switch {
	case r.Text != nil:
		return head + mimeSuffix(r.MimeType) + "]\n" + *r.Text
	case r.Blob != nil:
		return head + " (" + withMime(r.MimeType, fmt.Sprintf("%d bytes binary omitted", base64DecodedLen(*r.Blob))) + ")]"
	default:
		return head + mimeSuffix(r.MimeType) + "]"
	}
}

// blockType extracts just the "type" of a block that did not decode in
// full, or "" when even that fails.
func blockType(raw json.RawMessage) string {
	var b struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return ""
	}
	return b.Type
}

// omittedMarker names a block Thane cannot render. The type is
// server-supplied, so it is cleaned and capped.
func omittedMarker(typ string) string {
	typ = strings.Join(strings.Fields(cleanServerText(typ)), " ")
	if typ == "" {
		typ = "unrecognized"
	}
	return "[" + truncateUTF8(typ, blockTypeCapBytes) + " content omitted]"
}

func mimeSuffix(mime string) string {
	if mime == "" {
		return ""
	}
	return " (" + mime + ")"
}

func withMime(mime, detail string) string {
	if mime == "" {
		return detail
	}
	return mime + ", " + detail
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// base64DecodedLen returns the decoded size of standard base64 data,
// without decoding it.
func base64DecodedLen(s string) int {
	s = strings.TrimRight(s, "=")
	return len(s) * 3 / 4
}

// compactStructured returns structuredContent as compact JSON, and false
// when it is absent, JSON null, or not valid JSON.
func compactStructured(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "", false
	}
	return buf.String(), true
}

// capResult bounds rendered output at resultCapBytes, note included, and
// substitutes emptyResultText for an empty rendering.
func capResult(s string) string {
	if strings.TrimSpace(s) == "" {
		return emptyResultText
	}
	if len(s) <= resultCapBytes {
		return s
	}
	return truncateUTF8(s, resultCapBytes-len(resultTruncatedNote)) + resultTruncatedNote
}

// truncateUTF8 cuts s to at most maxBytes without splitting a rune.
// A cut that would land inside a multi-byte rune steps back to the
// rune's first byte. maxBytes <= 0 returns "".
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
