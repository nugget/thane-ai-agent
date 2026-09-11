package email

import (
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"
)

// Address is one mailbox: an optional display name and the addr-spec.
// Two addresses denote the same mailbox when their [Address.Key]
// values are equal; the display name never participates.
type Address struct {
	// Name is the display name, decoded from RFC 2047 encoding, or
	// empty.
	Name string `json:"name,omitempty"`

	// Address is the addr-spec (local@domain) as the message spelled
	// it.
	Address string `json:"address"`
}

// String renders the address for a reader: `Name <addr>` when a name
// is present, else the bare addr-spec. The name is left as text, never
// RFC 2047 encoded, because the result goes into tool results and
// event metadata the model reads, not into message headers; header
// construction goes through go-message in compose. The name is quoted
// only when it contains characters that would otherwise change how
// the string parses, so the rendering round-trips through
// [parseAddress].
func (a Address) String() string {
	if a.Name == "" {
		return a.Address
	}
	return quoteDisplayName(a.Name) + " <" + a.Address + ">"
}

// quoteDisplayName returns name as an RFC 5322 quoted-string when it
// contains a special character, and unchanged otherwise.
func quoteDisplayName(name string) string {
	if !strings.ContainsAny(name, "()<>[]:;@\\,.\"") {
		return name
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range name {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// Key is the comparison form of the address: the addr-spec lowercased
// and trimmed. Mailbox local parts are case-sensitive by the letter of
// RFC 5321, but no deployed mail system treats them so, and matching a
// contact directory that stores one spelling requires folding case.
func (a Address) Key() string {
	return strings.ToLower(strings.TrimSpace(a.Address))
}

// Domain returns the part of the addr-spec after the last "@", or
// empty when the address has no domain.
func (a Address) Domain() string {
	at := strings.LastIndexByte(a.Address, '@')
	if at < 0 || at == len(a.Address)-1 {
		return ""
	}
	return strings.ToLower(a.Address[at+1:])
}

// IsZero reports whether the address carries no addr-spec.
func (a Address) IsZero() bool {
	return strings.TrimSpace(a.Address) == ""
}

// parseAddress parses one RFC 5322 mailbox in either "Name <addr>" or
// bare "addr" form. Display names in RFC 2047 encoding are decoded
// with go-message's charset table, which is why this does not call
// net/mail directly.
func parseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Address{}, fmt.Errorf("empty address")
	}
	parsed, err := mail.ParseAddress(s)
	if err != nil {
		return Address{}, fmt.Errorf("parse address %q: %w", s, err)
	}
	return Address{Name: parsed.Name, Address: parsed.Address}, nil
}

// parseAddresses parses each entry of list with [parseAddress] and
// fails on the first invalid one, naming it.
func parseAddresses(list []string) ([]Address, error) {
	out := make([]Address, 0, len(list))
	for _, s := range list {
		a, err := parseAddress(s)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// imapAddress converts an IMAP envelope address. It returns false for
// group start and end markers, which carry no mailbox and would
// otherwise render as "Name <>".
func imapAddress(addr imap.Address) (Address, bool) {
	spec := addr.Addr()
	if spec == "" || addr.Mailbox == "" || addr.Host == "" {
		return Address{}, false
	}
	return Address{Name: addr.Name, Address: spec}, true
}

// imapAddresses converts an envelope address list, dropping group
// markers.
func imapAddresses(list []imap.Address) []Address {
	if len(list) == 0 {
		return nil
	}
	out := make([]Address, 0, len(list))
	for _, a := range list {
		if conv, ok := imapAddress(a); ok {
			out = append(out, conv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addressStrings renders a list for header-style display.
func addressStrings(list []Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return out
}

// collectRecipients gathers the unique addr-specs across the given
// lists, in first-seen order, for SMTP RCPT TO. Uniqueness is by
// [Address.Key]; the first spelling seen is the one sent.
func collectRecipients(lists ...[]Address) []string {
	seen := make(map[string]bool)
	var result []string
	for _, list := range lists {
		for _, addr := range list {
			key := addr.Key()
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, strings.TrimSpace(addr.Address))
		}
	}
	return result
}

// containsAddress reports whether list holds an address with the same
// key as target.
func containsAddress(list []Address, target Address) bool {
	key := target.Key()
	for _, a := range list {
		if a.Key() == key {
			return true
		}
	}
	return false
}
