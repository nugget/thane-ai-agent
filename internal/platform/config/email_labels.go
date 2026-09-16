package config

import (
	"fmt"
	"slices"
	"strings"
)

// Email labels: a small vocabulary of marks the operator sees in their
// own mail client. A label is a meaning first. How it shows is a
// presentation: an IMAP keyword, which any client can search and some
// display, a flag colour, which a client that colours flags displays and
// every other client shows as a plain flag, or both. The vocabulary is
// declared once, under email.labels, and an account carries the labels
// its mailbox.labels names.

// EmailLabelApplyContactMatched is the one derived-label rule: Go applies
// the label at poll time to new INBOX mail whose sender matches exactly
// one contact record.
const EmailLabelApplyContactMatched = "contact_matched"

// Label vocabulary bounds. Every declared label is rendered into every
// turn that wears the email tag, so the vocabulary stays small.
const (
	maxEmailLabels            = 8
	maxEmailLabelNameBytes    = 32
	maxEmailLabelMeaningBytes = 200
	maxEmailLabelKeywordBytes = 64
)

// emailLabelColors is the closed set of flag colours, in the order of
// their bit encoding. The email package's colour-bit table must match
// it; a test there pins the two together.
var emailLabelColors = []string{"red", "orange", "yellow", "green", "blue", "purple", "grey"}

// emailFlagColorBitKeywords are the keywords that encode a flag colour.
// color: writes them, so a label's keyword may not be one of them.
var emailFlagColorBitKeywords = []string{"$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2"}

// emailJunkKeywords are the keywords junk filters in mail clients and
// servers read and write without a leading $. A label written with one
// would tell those filters a message is, or is not, junk.
var emailJunkKeywords = []string{"Junk", "NonJunk", "NotJunk"}

// EmailLabelColors returns the colours a label's color may name.
func EmailLabelColors() []string {
	return slices.Clone(emailLabelColors)
}

// EmailLabelConfig is one label: what it means and how it shows.
type EmailLabelConfig struct {
	// Meaning is what the label says about a message, in one short
	// sentence, at most 200 bytes. It is shown to the model beside the
	// label's name. Required.
	Meaning string `yaml:"meaning"`

	// Keyword is the IMAP keyword the label writes, for example
	// "thane-contact": printable ASCII without spaces or any of
	// ( ) { % * " \ ], at most 64 bytes. System flags such as \Flagged,
	// keywords beginning with $ (the ones clients already give meaning
	// to, such as $Junk and the $MailFlagBit0-2 colour keywords), and
	// Junk, NonJunk, and NotJunk are refused. A keyword is always
	// searchable; whether a client shows it depends on the client.
	Keyword string `yaml:"keyword"`

	// Color is the flag colour the label writes: red, orange, yellow,
	// green, blue, purple, or grey. It is written as \Flagged plus the
	// $MailFlagBit0-2 keywords that encode the colour, which a client
	// that colours flags displays; every other client shows a plain flag.
	// A colour is written only on a message nobody has flagged, and at
	// most one label may use each colour.
	Color string `yaml:"color"`

	// Apply names the rule under which Go applies the label itself, so
	// the model never does. "contact_matched" applies it at poll time to
	// new INBOX mail whose sender matches exactly one contact record, on
	// every account whose mailbox.labels names the label. Leave empty for
	// a label the model applies with email_mark.
	Apply string `yaml:"apply"`
}

// LabelNames returns the declared label names, sorted.
func (c EmailConfig) LabelNames() []string {
	names := make([]string, 0, len(c.Labels))
	for name := range c.Labels {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// normalizeLabels trims every label field and every account's label
// names, and lowercases color and apply, which are closed vocabularies.
func (c *EmailConfig) normalizeLabels() {
	for name, l := range c.Labels {
		l.Meaning = strings.TrimSpace(l.Meaning)
		l.Keyword = strings.TrimSpace(l.Keyword)
		l.Color = strings.ToLower(strings.TrimSpace(l.Color))
		l.Apply = strings.ToLower(strings.TrimSpace(l.Apply))
		c.Labels[name] = l
	}
	for i := range c.Accounts {
		for j, name := range c.Accounts[i].Mailbox.Labels {
			c.Accounts[i].Mailbox.Labels[j] = strings.TrimSpace(name)
		}
	}
}

// validateLabels checks the label vocabulary, then the labels each
// account carries. Labels are checked in name order, so the first
// problem reported is the same on every load.
func (c EmailConfig) validateLabels() error {
	if n := len(c.Labels); n > maxEmailLabels {
		return fmt.Errorf("email.labels declares %d labels, over the limit of %d; keep the vocabulary to the marks the operator reads", n, maxEmailLabels)
	}
	colors := make(map[string]string, len(c.Labels))
	keywords := make(map[string]string, len(c.Labels))
	for _, name := range c.LabelNames() {
		l := c.Labels[name]
		where := "email.labels." + name
		if err := validateEmailLabelName(name); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if strings.TrimSpace(l.Meaning) == "" {
			return fmt.Errorf("%s: meaning is required; say in one sentence what the label says about a message", where)
		}
		if n := len(l.Meaning); n > maxEmailLabelMeaningBytes {
			return fmt.Errorf("%s: meaning is %d bytes, over the %d-byte limit; keep it to one short sentence", where, n, maxEmailLabelMeaningBytes)
		}
		keyword, color := strings.TrimSpace(l.Keyword), strings.TrimSpace(l.Color)
		if keyword == "" && color == "" {
			return fmt.Errorf("%s: set keyword, color, or both; a label with neither cannot show on a message", where)
		}
		if keyword != "" {
			if err := ValidateEmailKeyword(keyword); err != nil {
				return fmt.Errorf("%s: keyword %w", where, err)
			}
			folded := strings.ToLower(keyword)
			if other, taken := keywords[folded]; taken {
				return fmt.Errorf("%s: keyword %q is also label %s's; IMAP keywords ignore case, so each label needs its own", where, keyword, other)
			}
			keywords[folded] = name
		}
		if color != "" {
			if !slices.Contains(emailLabelColors, color) {
				return fmt.Errorf("%s: color %q is not one of %s", where, l.Color, strings.Join(emailLabelColors, ", "))
			}
			if other, taken := colors[color]; taken {
				return fmt.Errorf("%s: color %s is also label %s's; a message shows one flag colour, so each colour can mean only one label", where, color, other)
			}
			colors[color] = name
		}
		switch l.Apply {
		case "", EmailLabelApplyContactMatched:
		default:
			return fmt.Errorf("%s: apply %q is not a rule; use %s, or leave it empty for a label the model applies with email_mark", where, l.Apply, EmailLabelApplyContactMatched)
		}
	}
	for i, a := range c.Accounts {
		if err := c.validateAccountLabels(i, a); err != nil {
			return err
		}
	}
	return nil
}

// validateAccountLabels checks one account's mailbox.labels: each name
// is declared under email.labels and listed once, and an account whose
// access is read, which Thane never writes, names none.
func (c EmailConfig) validateAccountLabels(i int, a EmailAccountConfig) error {
	if len(a.Mailbox.Labels) == 0 {
		return nil
	}
	where := fmt.Sprintf("email.accounts[%d] (%s): mailbox.labels", i, a.Name)
	if a.AccessLevel() == EmailAccessRead {
		return fmt.Errorf("%s names labels, but the account's access is read, so Thane never writes a label there; remove mailbox.labels or raise access to organize", where)
	}
	seen := make(map[string]bool, len(a.Mailbox.Labels))
	for _, name := range a.Mailbox.Labels {
		if _, ok := c.Labels[name]; !ok {
			declared := "none"
			if names := c.LabelNames(); len(names) > 0 {
				declared = strings.Join(names, ", ")
			}
			return fmt.Errorf("%s names %q, which email.labels does not declare (declared: %s)", where, name, declared)
		}
		if seen[name] {
			return fmt.Errorf("%s names %q twice", where, name)
		}
		seen[name] = true
	}
	return nil
}

// validateEmailLabelName checks a label name: lowercase letters, digits,
// hyphen, and underscore, starting with a letter. The name is what the
// model passes to email_mark and email_search and reads in results.
func validateEmailLabelName(name string) error {
	if name == "" || len(name) > maxEmailLabelNameBytes {
		return fmt.Errorf("a label name must be 1 to %d bytes", maxEmailLabelNameBytes)
	}
	for i, r := range name {
		lower := r >= 'a' && r <= 'z'
		if lower || (i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '_')) {
			continue
		}
		return fmt.Errorf("a label name uses lowercase letters, digits, - and _, starting with a letter; %q does not", name)
	}
	return nil
}

// ValidateEmailKeyword checks that keyword is an IMAP keyword a label may
// write: an RFC 3501 atom of printable ASCII without ( ) { % * " \ or ],
// at most 64 bytes, and none that a client or server already gives
// meaning to: a system flag, a keyword beginning with $ (the registered
// keywords, $Junk, $Forwarded, and the flag-colour keywords among them),
// or a junk-filter keyword. The error completes a sentence that begins
// with the keyword's label.
func ValidateEmailKeyword(keyword string) error {
	switch {
	case keyword == "":
		return fmt.Errorf("is empty")
	case strings.HasPrefix(keyword, `\`):
		return fmt.Errorf("%q is an IMAP system flag, which labels never write; name a keyword of your own, such as thane-contact", keyword)
	case slices.ContainsFunc(emailFlagColorBitKeywords, func(k string) bool { return strings.EqualFold(k, keyword) }):
		return fmt.Errorf("%q is a flag-colour keyword, which color: writes; name a keyword of your own, such as thane-contact", keyword)
	case strings.HasPrefix(keyword, "$"):
		return fmt.Errorf("%q begins with $, which marks the keywords clients and servers already give meaning to, such as $Junk, $NotJunk, $Forwarded, and $MDNSent; name a keyword of your own, such as thane-contact", keyword)
	case slices.ContainsFunc(emailJunkKeywords, func(k string) bool { return strings.EqualFold(k, keyword) }):
		return fmt.Errorf("%q is a keyword junk filters read and write, so a label written with it would tell them a message is or is not junk; name a keyword of your own, such as thane-contact", keyword)
	case len(keyword) > maxEmailLabelKeywordBytes:
		return fmt.Errorf("%q is %d bytes, over the %d-byte limit", keyword, len(keyword), maxEmailLabelKeywordBytes)
	}
	for i := 0; i < len(keyword); i++ {
		b := keyword[i]
		if b <= ' ' || b >= 0x7f || strings.IndexByte(`(){%*"\]`, b) >= 0 {
			return fmt.Errorf("%q is not an IMAP keyword: it contains %q; use printable ASCII without spaces or any of ( ) { %% * \" \\ ]", keyword, string(rune(b)))
		}
	}
	return nil
}
