package email

import (
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"

	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
)

// Labels are a portable vocabulary first (email.labels): each is a
// meaning, shown on a message as an IMAP keyword, a flag colour, or both.
// A flag colour is a presentation on top of the vocabulary: \Flagged plus
// the $MailFlagBit0-2 keywords, which a client that colours flags reads
// as a colour and every other client shows as a plain flag. Nothing here
// assumes a client; a keyword is searchable on any server that keeps it.
//
// Two rules hold wherever a label is written. A colour is never written
// over a flag the operator set, and removing a label removes only what
// Thane recorded setting (labels_marks.go). Both rest on IMAP keywords
// sticking, which the folder's PERMANENTFLAGS decides (KeywordVerdict).

// LabelConfig is one declared label.
type LabelConfig = platformconfig.EmailLabelConfig

// LabelApplyContactMatched is the derived-label rule Go applies at poll
// time to new INBOX mail whose sender matches exactly one contact.
const LabelApplyContactMatched = platformconfig.EmailLabelApplyContactMatched

// flagColorBits is the colour-bit table: each colour's value over
// $MailFlagBit0 (1), $MailFlagBit1 (2), and $MailFlagBit2 (4). Red is a
// flag with no bits.
var flagColorBits = map[string]uint8{
	"red":    0,
	"orange": 1,
	"yellow": 2,
	"green":  3,
	"blue":   4,
	"purple": 5,
	"grey":   6,
}

// colorBitFlags are the keywords for bits 0, 1, and 2.
var colorBitFlags = [3]imap.Flag{"$MailFlagBit0", "$MailFlagBit1", "$MailFlagBit2"}

// splitColorBits returns the keywords a colour value sets and the ones
// it leaves clear.
func splitColorBits(bits uint8) (set, clear []imap.Flag) {
	for i, f := range colorBitFlags {
		if bits&(1<<i) != 0 {
			set = append(set, f)
		} else {
			clear = append(clear, f)
		}
	}
	return set, clear
}

// colorBitsIn returns the colour value a message's flags carry, whatever
// case the server spells the keywords in.
func colorBitsIn(flags []string) uint8 {
	var bits uint8
	for i, f := range colorBitFlags {
		if stringFlagIn(flags, string(f)) {
			bits |= 1 << i
		}
	}
	return bits
}

// flagColorOf names the colour a message's flag shows, or "" when the
// message carries no \Flagged or a combination of bits no colour uses.
func flagColorOf(flags []string) string {
	if !flagged(flags) {
		return ""
	}
	bits := colorBitsIn(flags)
	for name, b := range flagColorBits {
		if b == bits {
			return name
		}
	}
	return ""
}

// flagNames spells flags as strings, for a record or a log line.
func flagNames(flags []imap.Flag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = string(f)
	}
	return out
}

// stringFlagIn reports whether flags holds want, ignoring case as IMAP
// does (RFC 3501 §2.3.2).
func stringFlagIn(flags []string, want string) bool {
	return slices.ContainsFunc(flags, func(f string) bool { return strings.EqualFold(f, want) })
}

// flagged reports whether a message carries \Flagged.
func flagged(flags []string) bool {
	return stringFlagIn(flags, string(imap.FlagFlagged))
}

// mailLabel is one declared label with its name.
type mailLabel struct {
	Name string
	LabelConfig
}

// colorBits returns the label's colour value and whether it has a colour.
func (l mailLabel) colorBits() (uint8, bool) {
	if l.Color == "" {
		return 0, false
	}
	bits, ok := flagColorBits[l.Color]
	return bits, ok
}

// needsKeywords reports whether writing the label writes any keyword:
// its own, or a colour's bits. Only a red flag and nothing else needs
// none.
func (l mailLabel) needsKeywords() bool {
	bits, _ := l.colorBits()
	return l.Keyword != "" || bits != 0
}

// showsAs says how the label shows on a message, for the Email Accounts
// entry: "blue flag and keyword thane-contact", "blue flag", or
// "keyword thane-contact".
func (l mailLabel) showsAs() string {
	var parts []string
	if l.Color != "" {
		parts = append(parts, l.Color+" flag")
	}
	if l.Keyword != "" {
		parts = append(parts, "keyword "+l.Keyword)
	}
	return strings.Join(parts, " and ")
}

// labelSet is the declared vocabulary, sorted by name.
type labelSet []mailLabel

// newLabelSet builds the vocabulary from configuration.
func newLabelSet(cfg map[string]LabelConfig) labelSet {
	set := make(labelSet, 0, len(cfg))
	for name, l := range cfg {
		set = append(set, mailLabel{Name: name, LabelConfig: l})
	}
	slices.SortFunc(set, func(a, b mailLabel) int { return strings.Compare(a.Name, b.Name) })
	return set
}

// named returns the label called name.
func (s labelSet) named(name string) (mailLabel, bool) {
	for _, l := range s {
		if l.Name == name {
			return l, true
		}
	}
	return mailLabel{}, false
}

// only returns the labels named in names, in vocabulary order.
func (s labelSet) only(names []string) labelSet {
	var out labelSet
	for _, l := range s {
		if slices.Contains(names, l.Name) {
			out = append(out, l)
		}
	}
	return out
}

// byColor returns the label whose colour is color.
func (s labelSet) byColor(color string) (mailLabel, bool) {
	for _, l := range s {
		if color != "" && l.Color == color {
			return l, true
		}
	}
	return mailLabel{}, false
}

// accountLabels returns the labels an account carries: the ones its
// mailbox.labels names, and none on an account whose access is read,
// which Thane never writes.
func (m *Manager) accountLabels(account string) labelSet {
	cfg, ok := m.configs[account]
	if !ok || cfg.AccessLevel() == AccessRead {
		return nil
	}
	return m.labels.only(cfg.Mailbox.Labels)
}

// appliedBy returns the labels Go applies under rule.
func (s labelSet) appliedBy(rule string) labelSet {
	var out labelSet
	for _, l := range s {
		if l.Apply == rule {
			out = append(out, l)
		}
	}
	return out
}

// markable returns the names of the labels the model applies with
// email_mark: every label without an apply rule.
func (s labelSet) markable() []string {
	var out []string
	for _, l := range s {
		if l.Apply == "" {
			out = append(out, l.Name)
		}
	}
	return out
}

// searchable returns the names of the labels email_search can find:
// every label with a keyword.
func (s labelSet) searchable() []string {
	var out []string
	for _, l := range s {
		if l.Keyword != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

// hasColors reports whether any label writes a flag colour.
func (s labelSet) hasColors() bool {
	return slices.ContainsFunc(s, func(l mailLabel) bool { return l.Color != "" })
}

// carried returns the names of the labels whose keyword a message
// carries, or nil. A label without a keyword never appears: its colour
// alone cannot say who set it.
func (s labelSet) carried(flags []string) []string {
	var out []string
	for _, l := range s {
		if l.Keyword != "" && stringFlagIn(flags, l.Keyword) {
			out = append(out, l.Name)
		}
	}
	return out
}

// KeywordVerdict is what a folder's PERMANENTFLAGS says about IMAP
// keywords, which every label is written with except a bare red flag.
type KeywordVerdict string

// Keyword verdicts.
const (
	// KeywordsPermanent: PERMANENTFLAGS lists \*, so a keyword Thane
	// writes stays on the message.
	KeywordsPermanent KeywordVerdict = "permanent"

	// KeywordsSessionOnly: PERMANENTFLAGS lacks \*, so a new keyword
	// would last only as long as Thane's session.
	KeywordsSessionOnly KeywordVerdict = "session_only"

	// KeywordsUnsupported: the server listed no permanent flags at all,
	// so nothing Thane writes can be relied on to stay.
	KeywordsUnsupported KeywordVerdict = "unsupported"
)

// keywordVerdictOf reads a SELECT's PERMANENTFLAGS. An absent list reads
// as unsupported: RFC 3501 lets a client assume every flag sticks, but a
// guard that writes marks the operator will see takes the answer that
// writes nothing.
func keywordVerdictOf(permanent []imap.Flag) KeywordVerdict {
	switch {
	case len(permanent) == 0:
		return KeywordsUnsupported
	case flagIn(permanent, imap.FlagWildcard):
		return KeywordsPermanent
	default:
		return KeywordsSessionOnly
	}
}
