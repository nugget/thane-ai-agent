package contacts

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
)

// validFactKey is the grammar for a contact_save fact key and for a
// property name a model-facing vCard import may store. '.', ';', ':'
// and line breaks are vCard syntax (group, parameter and value
// separators, and a new property), and ContactToCard emits a row's name
// verbatim, so a name carrying them would decode as some other
// property, such as a live EMAIL or a trust zone, on the operator's next
// CardDAV PUT.
var validFactKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// thaneHeaderPrefix is the namespace of Thane's own vCard headers:
// trust zone, Home Assistant person binding, AI summary, origin policy
// and keys. contact_save writes those through dedicated arguments or
// never.
const thaneHeaderPrefix = "X-THANE-"

// factRefusal returns why contact_save refuses one fact, or "" when the
// fact may be saved. Keys are checked before values, and the KEY
// refusal keeps its original wording.
//
// The key is quoted back through [echoForRefusal], so a refusal stays
// bounded however long the key is.
func factRefusal(key, value string) string {
	quoted := echoForRefusal(key)
	if isReservedKeyProperty(key) {
		return fmt.Sprintf("fact %q cannot be set through contact_save: KEY and X-THANE-KEY-* properties hold the keys that authenticate a contact's messages and are operator-custodied; ask the operator to install the key, then retry without it", quoted)
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(key)), thaneHeaderPrefix) {
		return fmt.Sprintf("fact %q cannot be set through contact_save: X-THANE-* headers carry Thane's trust zone, Home Assistant person binding, AI summary and origin policy and are operator-custodied; use ai_summary, origin_tags or origin_context_refs for those, or ask the operator, then retry without it", quoted)
	}
	if !validFactKey.MatchString(key) {
		return fmt.Sprintf("fact %q cannot be set through contact_save: a fact key is a plain name of letters, digits, '-' and '_' that starts with a letter, at most 64 characters, because '.', ';', ':', spaces and line breaks are vCard syntax a contacts client would read as a different property; retry with a key like %q, and use email, phone, signal or matrix for addresses and numbers", quoted, suggestedFactKey(key))
	}
	if coreProperties[strings.ToUpper(key)] {
		return codecOwnedFactRefusal(key)
	}
	if hasControl(value) {
		return fmt.Sprintf("fact %q cannot be set through contact_save: its value contains a control character such as a line break, which a vCard would read as the start of another property; retry with the value on one line", quoted)
	}
	return ""
}

// refusalEchoMaxBytes caps each caller-supplied key or value a refusal
// quotes back: enough to recognize it, never enough to flood the reply.
const refusalEchoMaxBytes = 96

// refusalListMaxBytes caps the itemized part of one refusal. A single
// contact_save can carry any number of refused facts, and the whole
// error reaches the model as the tool result, so the list stops well
// inside the tool-result cap and counts what it left out.
const refusalListMaxBytes = 4 << 10

// echoForRefusal clips s to refusalEchoMaxBytes on a rune boundary and
// marks the cut, for quoting a caller-supplied key or value in a
// refusal.
func echoForRefusal(s string) string {
	if len(s) <= refusalEchoMaxBytes {
		return s
	}
	cut := refusalEchoMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s... (%d bytes)", s[:cut], len(s))
}

// boundedRefusalList renders items as "\n- item" lines. Once the next
// line would pass maxBytes it stops, says how many items it did not
// list, and how to see them; the first item is always listed, so a
// single oversized item is reported rather than silently dropped.
//
// Callers pass the budget their own refusal has left after the prose
// around the list, because what the model must keep — the recovery that
// closes the refusal — comes after the items and must not be what a long
// list pushes out.
func boundedRefusalList(items []string, maxBytes int) string {
	var b strings.Builder
	for i, item := range items {
		if i > 0 && b.Len()+len("\n- ")+len(item) > maxBytes {
			fmt.Fprintf(&b, "\n- ...and %d more refused, not listed here; fix or drop the listed ones and retry to see the rest", len(items)-i)
			break
		}
		b.WriteString("\n- ")
		b.WriteString(item)
	}
	return b.String()
}

// codecOwnedFactArguments names the contact_save argument that writes
// each vCard field the contact record owns.
var codecOwnedFactArguments = map[string]string{
	vcard.FieldFormattedName: "name",
	vcard.FieldName:          "given_name and family_name",
	vcard.FieldKind:          "kind",
	vcard.FieldNickname:      "nickname",
	vcard.FieldOrganization:  "org",
	vcard.FieldTitle:         "title",
	vcard.FieldRole:          "role",
	vcard.FieldNote:          "note",
}

// codecOwnedFactRefusal explains why a fact key that names a field of
// the contact record itself is refused. ContactToCard emits the record's
// own field under that name and withholds a property row that shadows
// it, so the fact would never reach the operator's contacts client and
// the operator's next CardDAV PUT, which replaces every row with what
// the card carries, would delete it.
func codecOwnedFactRefusal(key string) string {
	field := strings.ToUpper(key)
	lost := fmt.Sprintf("fact %q cannot be set through contact_save: %s is a field of the contact record itself, so a fact under that name never reaches the operator's contacts client and is lost on the operator's next edit", key, field)
	if argument, ok := codecOwnedFactArguments[field]; ok {
		return fmt.Sprintf("%s; set it with the %s argument instead, then retry without the fact", lost, argument)
	}
	return fmt.Sprintf("%s; ask the operator to set it through CardDAV or the contacts API, then retry without it", lost)
}

// isLineBreakOrControl reports whether r is a control character or a
// Unicode line or paragraph separator: anything a contacts client might
// read as the end of a vCard line.
func isLineBreakOrControl(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// hasControl reports whether a single-line value carries a line break
// or other control character.
func hasControl(value string) bool {
	return strings.IndexFunc(value, isLineBreakOrControl) >= 0
}

// hasUnescapedControl reports whether a value carries a control
// character the go-vcard encoder emits raw. The encoder escapes LF, so
// a multi-line note is safe, but it writes a CR, any other control
// character and the Unicode separators verbatim, and a client that
// reads one as a line ending would start a new property mid-value.
// Tab is allowed.
func hasUnescapedControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return r != '\n' && r != '\t' && isLineBreakOrControl(r)
	}) >= 0
}

// suggestedFactKey turns a refused key into a grammatical one by
// replacing every run of disallowed characters with '_'.
func suggestedFactKey(key string) string {
	var b strings.Builder
	pendingUnderscore := false
	for _, r := range key {
		allowed := r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_')
		if !allowed {
			pendingUnderscore = b.Len() > 0
			continue
		}
		if b.Len() == 0 && !unicode.IsLetter(r) {
			continue
		}
		if pendingUnderscore {
			b.WriteByte('_')
			pendingUnderscore = false
		}
		b.WriteRune(r)
	}
	suggestion := b.String()
	if len(suggestion) > 64 {
		suggestion = suggestion[:64]
	}
	if suggestion == "" {
		return "note"
	}
	return suggestion
}

// factRefusals checks every fact and returns one error naming each
// refused key with its reason, or nil when all may be saved.
func factRefusals(facts map[string]string) error {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var refusals []string
	for _, key := range keys {
		if refusal := factRefusal(key, facts[key]); refusal != "" {
			refusals = append(refusals, refusal)
		}
	}
	if len(refusals) == 0 {
		return nil
	}
	return fmt.Errorf("contact_save refused %d fact(s); nothing was saved:%s", len(refusals), boundedRefusalList(refusals, refusalListMaxBytes))
}

// argumentRefusals checks contact_save's scalar and origin arguments
// the way factRefusals checks facts. ContactToCard emits every one of
// them as a vCard value, and the go-vcard encoder escapes only LF, so a
// carriage return in a note would reach the operator's contacts client
// raw and could read as the start of a live EMAIL or trust-zone line.
// Single-line arguments refuse every line break and control character;
// note and ai_summary keep plain line breaks and tabs.
func argumentRefusals(args SaveContactArgs) error {
	var refusals []string
	singleLine := func(argument, value string) {
		if hasControl(value) {
			refusals = append(refusals, fmt.Sprintf("%s cannot be saved: its value contains a line break or other control character, which a vCard would read as the start of another property; retry with the value on one line", argument))
		}
	}
	multiLine := func(argument, value string) {
		if hasUnescapedControl(value) {
			refusals = append(refusals, fmt.Sprintf("%s cannot be saved: its value contains a carriage return or other control character, which a vCard would read as the start of another property; use plain line breaks, then retry", argument))
		}
	}
	singleLine("name", args.Name)
	singleLine("kind", args.Kind)
	singleLine("given_name", args.GivenName)
	singleLine("family_name", args.FamilyName)
	singleLine("nickname", args.Nickname)
	singleLine("org", args.Org)
	singleLine("title", args.Title)
	singleLine("role", args.Role)
	multiLine("note", args.Note)
	multiLine("ai_summary", args.AISummary)
	for _, value := range args.OriginTags {
		singleLine("origin_tags", value)
	}
	for _, value := range args.OriginContextRefs {
		singleLine("origin_context_refs", value)
	}
	if len(refusals) == 0 {
		return nil
	}
	return fmt.Errorf("contact_save refused %d argument value(s); nothing was saved:%s", len(refusals), boundedRefusalList(refusals, refusalListMaxBytes))
}
