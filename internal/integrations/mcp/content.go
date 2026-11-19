package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
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
// compact JSON (see compactStructured) when it fits within the cap,
// followed by one line per non-text block; text blocks are skipped
// because the spec has servers repeat the structured value in them.
// When the JSON does not fit but the content blocks do, the blocks are
// used instead.
//
// One shape renders from the content blocks instead: structuredContent
// that is a single-key object whose value is a string equal to the
// joined text blocks. The reference Python SDK wraps every primitive
// return value that way ({"result": "..."}) beside a text block holding
// the value itself, so the wrapper adds no structure, and as JSON the
// value would reach the model on one line with its newlines and quotes
// escaped.
func renderToolResult(res *callToolResult) string {
	if res == nil {
		return emptyResultText
	}
	blocks := renderBlocks(res.Content)
	if !res.IsError {
		if structured, ok := compactStructured(res.StructuredContent); ok {
			joinedBlocks := strings.Join(blocks.all, "\n")
			if len(blocks.text) > 0 && wrapsText(res.StructuredContent, strings.Join(blocks.text, "\n")) {
				return capResult(joinedBlocks)
			}
			if len(structured) <= resultCapBytes || joinedBlocks == "" || len(joinedBlocks) > resultCapBytes {
				return capResult(strings.Join(append([]string{structured}, blocks.nonText...), "\n"))
			}
			return capResult(joinedBlocks)
		}
	}
	return capResult(strings.Join(blocks.all, "\n"))
}

// renderedBlocks holds each content block rendered as text, in order,
// the subset that are not text blocks, and the texts of the text
// blocks.
type renderedBlocks struct {
	all     []string
	nonText []string
	text    []string
}

// wrapsText reports whether structured is a single-key object whose
// one value is a JSON string equal to text.
func wrapsText(structured json.RawMessage, text string) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(structured, &obj) != nil || len(obj) != 1 {
		return false
	}
	for _, v := range obj {
		var s string
		return json.Unmarshal(v, &s) == nil && s == text
	}
	return false
}

func renderBlocks(raws []json.RawMessage) renderedBlocks {
	var out renderedBlocks
	for _, raw := range raws {
		text, isText := renderBlock(raw)
		if text == "" {
			continue
		}
		out.all = append(out.all, text)
		if isText {
			out.text = append(out.text, text)
		} else {
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

// compactStructured returns structuredContent as compact JSON with its
// \uXXXX escapes decoded where JSON allows (unescapeJSONStrings), and
// false when it is absent, JSON null, or not valid JSON. Key order and
// number spelling are the server's.
func compactStructured(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "", false
	}
	return string(unescapeJSONStrings(buf.Bytes())), true
}

// unescapeJSONStrings rewrites the \uXXXX escapes in valid JSON as the
// UTF-8 characters they stand for, combining surrogate pairs, so the
// model reads "Zürich" and "a&b" rather than escape codes (Python's
// json.dumps escapes every non-ASCII character by default, and Go's
// encoder escapes <, > and &). An escape stays as it is when its
// character is a control character (below U+0020, or U+007F), '"' or
// '\\', or a lone surrogate. Every other escape (\n, \", \\ and the
// rest) and every byte outside an escape is copied unchanged, so the
// output is still valid JSON with the same key order and numbers. In
// valid JSON a backslash occurs only inside a string, so the scan needs
// no string tracking.
func unescapeJSONStrings(b []byte) []byte {
	if bytes.IndexByte(b, '\\') < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] != '\\' || i+1 >= len(b) {
			out = append(out, b[i])
			i++
			continue
		}
		if b[i+1] != 'u' {
			out = append(out, b[i], b[i+1])
			i += 2
			continue
		}
		r, n := unicodeEscape(b[i:])
		if n == 0 {
			out = append(out, b[i], b[i+1])
			i += 2
			continue
		}
		if r >= 0x20 && r != 0x7f && r != '"' && r != '\\' && utf8.ValidRune(r) {
			out = utf8.AppendRune(out, r)
		} else {
			out = append(out, b[i:i+n]...)
		}
		i += n
	}
	return out
}

// unicodeEscape decodes the \uXXXX escape at the start of b, combined
// with a following low-surrogate escape when it is a high surrogate.
// n is the number of bytes consumed (6 or 12), or 0 when b does not
// start with a well-formed escape. A lone surrogate is returned as is.
func unicodeEscape(b []byte) (r rune, n int) {
	r, ok := hex4(b, 2)
	if !ok {
		return 0, 0
	}
	if !utf16.IsSurrogate(r) || len(b) < 12 || b[6] != '\\' || b[7] != 'u' {
		return r, 6
	}
	low, ok := hex4(b, 8)
	if !ok {
		return r, 6
	}
	if pair := utf16.DecodeRune(r, low); pair != utf8.RuneError {
		return pair, 12
	}
	return r, 6
}

// hex4 parses the four hex digits at b[at:].
func hex4(b []byte, at int) (rune, bool) {
	if len(b) < at+4 {
		return 0, false
	}
	var r rune
	for _, c := range b[at : at+4] {
		switch {
		case '0' <= c && c <= '9':
			r = r<<4 | rune(c-'0')
		case 'a' <= c && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case 'A' <= c && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
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
