package facets

import (
	"fmt"
	"strings"
)

const (
	jsonFence  = "```json"
	fenceClose = "```"
)

// Render encodes a logical payload into the canonical Markdown persistence
// envelope. Callers must validate the payload before writing it.
func (c Contract) Render(payload Payload) string {
	fields := c.Fields()
	published := make(map[string]Field, len(fields))
	for _, field := range fields {
		published[field.Key] = field
	}
	blocks := make([]string, 0, len(fields))
	for _, item := range sections {
		field, ok := published[item.field.Key]
		if !ok {
			continue
		}
		value := strings.TrimSpace(*item.value(&payload))
		if value == "" {
			continue
		}
		if field.Format == FormatJSON {
			value = jsonFence + "\n" + value + "\n" + fenceClose
		}
		blocks = append(blocks, "## "+item.heading+"\n\n"+value)
	}
	return strings.Join(blocks, "\n\n")
}

// Parse decodes the canonical or legacy section envelope using the contract's
// declared formats.
func (c Contract) Parse(body string) Payload {
	payload, _ := ParseLegacy(body)
	for _, field := range c.Fields() {
		if field.Format != FormatJSON {
			continue
		}
		item, ok := sectionByKey(field.Key)
		if !ok {
			continue
		}
		value := item.value(&payload)
		*value = unfence(*value)
	}
	return payload
}

// ParseLegacy recognizes the historical reserved-heading envelope without a
// manifest. An ordinary body becomes Full and reports false, enabling lazy
// adoption on its first structured write.
func ParseLegacy(body string) (Payload, bool) {
	var payload Payload
	var preamble []string
	current := ""
	found := make(map[string]bool, len(sections))
	collected := make(map[string][]string, len(sections))
	for _, line := range strings.Split(body, "\n") {
		if heading, ok := reservedHeadingOf(line); ok {
			current = heading
			found[heading] = true
			continue
		}
		if current == "" {
			preamble = append(preamble, line)
			continue
		}
		collected[current] = append(collected[current], line)
	}
	for _, item := range sections {
		lines, ok := collected[item.heading]
		if !ok {
			continue
		}
		*item.value(&payload) = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	if leading := strings.TrimSpace(strings.Join(preamble, "\n")); leading != "" {
		if payload.Full == "" {
			payload.Full = leading
		} else {
			payload.Full = leading + "\n\n" + payload.Full
		}
	}
	// Status Line and Details were mandatory in every historical envelope.
	// Requiring both keeps an ordinary document's legitimate "## Details" or
	// "## Digest" section from being mistaken for private facet storage.
	if !found["Status Line"] || !found["Details"] {
		return Payload{Full: strings.TrimSpace(body)}, false
	}
	return payload, true
}

// RenderScaffold produces the canonical pre-first-write placeholder.
func (c Contract) RenderScaffold() string {
	var payload Payload
	for _, field := range c.Fields() {
		item, _ := sectionByKey(field.Key)
		placeholder := fmt.Sprintf("_(awaiting first cycle - %s)_", item.scaffoldHint)
		if field.MaxRunes > 0 {
			placeholder = fmt.Sprintf("_(awaiting first cycle - %s, <=%d characters)_", item.scaffoldHint, field.MaxRunes)
		}
		if field.Format == FormatJSON {
			placeholder = `{"awaiting_first_cycle": true}`
		}
		*item.value(&payload) = placeholder
	}
	return c.Render(payload)
}

func reservedHeadingOf(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "## ") {
		return "", false
	}
	text := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
	for _, item := range sections {
		if strings.EqualFold(text, item.heading) {
			return item.heading, true
		}
	}
	return "", false
}

func firstReservedHeading(value string) (string, bool) {
	for _, line := range strings.Split(value, "\n") {
		if heading, ok := reservedHeadingOf(line); ok {
			return "## " + heading, true
		}
	}
	return "", false
}

func unfence(value string) string {
	if !strings.HasPrefix(value, jsonFence) || !strings.HasSuffix(value, fenceClose) {
		return value
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(value, jsonFence), fenceClose)
	if strings.Contains(inner, fenceClose) {
		return value
	}
	return strings.TrimSpace(inner)
}
