package email

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

// Thane's label provenance: what it set on a message, kept in the
// operational state store under labelMarksNamespace with the key
// <account>/<Message-ID>. The Message-ID survives a move between folders,
// where a UID does not. The record is the only thing that lets Thane
// tell its own marks from the operator's: a keyword it did not add, and
// a flag whose colour is no longer the one it wrote, are the operator's,
// and nothing removes them.

const (
	// labelMarksNamespace is the opstate namespace of the records.
	labelMarksNamespace = "email_labels"

	// labelMarksTTL is how long a record lives after its last write.
	// Once it expires Thane no longer claims the marks, so they read as
	// the operator's and nothing Thane does removes them.
	labelMarksTTL = 90 * 24 * time.Hour
)

// labelMarks is what Thane set on one message.
type labelMarks struct {
	Account   string `json:"account"`
	MessageID string `json:"message_id"`

	// Size is the RFC822.SIZE of the copy Thane marked. One folder can
	// hold two copies of a message under one Message-ID, such as a direct
	// copy and a mailing list's; a copy of another size is not the one
	// this record describes, so Thane claims nothing on it. A move keeps
	// the size. Zero means no copy is marked yet.
	Size uint32 `json:"size,omitempty"`

	// Keywords lists the label keywords Thane added. A keyword the
	// message already carried is not listed, because Thane did not set
	// it.
	Keywords []string `json:"keywords,omitempty"`

	// Color is the flag colour Thane wrote, ColorBits the colour keywords
	// that encode it (none for red), and SetFlagged whether the flag they
	// colour is one Thane set. The flag is Thane's only while the message
	// still carries \Flagged with exactly these bits.
	Color      string   `json:"color,omitempty"`
	ColorBits  []string `json:"color_bits,omitempty"`
	SetFlagged bool     `json:"set_flagged,omitempty"`

	// Derived lists the labels with an apply rule the poller has applied
	// to this message. It applies each at most once, so a mark the
	// operator takes off stays off when the message is listed again.
	Derived []string `json:"derived,omitempty"`

	// By is who wrote the last change: email_poller, a loop name, or
	// operator.
	By        string    `json:"by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// labelMarksKey is a record's opstate key.
func labelMarksKey(account, messageID string) string {
	return account + "/" + normalizeMessageID(messageID)
}

// loadLabelMarks returns Thane's record for a message, or an empty one
// for the same account and Message-ID when there is none. A record that
// does not decode is an error: reading it as empty would let Thane treat
// its own marks as the operator's, or the reverse.
func loadLabelMarks(state *opstate.Store, account, messageID string) (labelMarks, error) {
	empty := labelMarks{Account: account, MessageID: normalizeMessageID(messageID)}
	raw, err := state.Get(labelMarksNamespace, labelMarksKey(account, messageID))
	if err != nil {
		return empty, fmt.Errorf("read Thane's label record for message %s: %w", empty.MessageID, err)
	}
	if raw == "" {
		return empty, nil
	}
	var m labelMarks
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return empty, fmt.Errorf("decode Thane's label record for message %s: %w", empty.MessageID, err)
	}
	return m, nil
}

// saveLabelMarks writes a record with a fresh TTL, or deletes it once it
// claims nothing.
func saveLabelMarks(state *opstate.Store, m *labelMarks) error {
	key := labelMarksKey(m.Account, m.MessageID)
	if m.empty() {
		if err := state.Delete(labelMarksNamespace, key); err != nil {
			return fmt.Errorf("delete Thane's label record for message %s: %w", m.MessageID, err)
		}
		return nil
	}
	m.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode Thane's label record for message %s: %w", m.MessageID, err)
	}
	if err := state.SetWithTTL(labelMarksNamespace, key, string(data), labelMarksTTL); err != nil {
		return fmt.Errorf("write Thane's label record for message %s: %w", m.MessageID, err)
	}
	return nil
}

// empty reports whether the record claims nothing and remembers no
// derived label.
func (m labelMarks) empty() bool {
	return len(m.Keywords) == 0 && !m.SetFlagged && m.Color == "" && len(m.Derived) == 0
}

// covers reports whether the record describes the copy of a message
// that is size bytes long. A record that has marked no copy yet
// describes any.
func (m labelMarks) covers(size uint32) bool {
	return m.Size == 0 || m.Size == size
}

// bind ties the record to the copy Thane is about to mark.
func (m *labelMarks) bind(size uint32) {
	if m.Size == 0 {
		m.Size = size
	}
}

// ownsFlag reports whether the message's flag is still the one Thane
// wrote: Thane set \Flagged, the message carries it, and its colour
// keywords are exactly the ones Thane wrote. A flag the operator
// recoloured, or set before Thane saw the message, is theirs. Thane
// learns only what it reads (see reconcile): a flag the operator took
// off and later set again in the same colour, with no read by Thane in
// between, still reads as Thane's. Closing that gap needs the server to
// report a change sequence (CONDSTORE), which Thane does not use.
func (m labelMarks) ownsFlag(flags []string) bool {
	return m.SetFlagged && flagged(flags) && colorBitsIn(flags) == colorBitsIn(m.ColorBits)
}

// reconcile drops from the record what the message's current flags show
// the operator has taken back: a recorded keyword the message no longer
// carries, and a flag that is no longer exactly the one Thane wrote. It
// reports whether the record changed. Every path that reads a message's
// flags beside its record reconciles first, so a mark the operator took
// back is never claimed again.
func (m *labelMarks) reconcile(flags []string) bool {
	changed := false
	for _, k := range slices.Clone(m.Keywords) {
		if !stringFlagIn(flags, k) {
			m.dropKeyword(k)
			changed = true
		}
	}
	if m.SetFlagged && !m.ownsFlag(flags) {
		m.clearColor()
		changed = true
	}
	return changed
}

// strip returns flags without the marks the record says Thane set and
// the message still shows as Thane's: its keywords, and its flag with
// the colour keywords while the flag is still the one Thane wrote.
func (m labelMarks) strip(flags []string) []string {
	owned := m.ownsFlag(flags)
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		switch {
		case stringFlagIn(m.Keywords, f):
		case owned && (flagged([]string{f}) || stringFlagIn(m.ColorBits, f)):
		default:
			out = append(out, f)
		}
	}
	return out
}

// hasKeyword reports whether Thane recorded adding keyword.
func (m labelMarks) hasKeyword(keyword string) bool {
	return stringFlagIn(m.Keywords, keyword)
}

// addKeyword records that Thane added keyword.
func (m *labelMarks) addKeyword(keyword string) {
	if !m.hasKeyword(keyword) {
		m.Keywords = append(m.Keywords, keyword)
	}
}

// dropKeyword forgets keyword.
func (m *labelMarks) dropKeyword(keyword string) {
	m.Keywords = slices.DeleteFunc(m.Keywords, func(k string) bool { return strings.EqualFold(k, keyword) })
}

// hasDerived reports whether the poller has already applied label.
func (m labelMarks) hasDerived(label string) bool {
	return slices.Contains(m.Derived, label)
}

// addDerived records that the poller applied label.
func (m *labelMarks) addDerived(label string) {
	if !m.hasDerived(label) {
		m.Derived = append(m.Derived, label)
	}
}

// setColor records the colour Thane wrote over a flag it set.
func (m *labelMarks) setColor(color string, bits []imap.Flag) {
	m.Color, m.ColorBits, m.SetFlagged = color, flagNames(bits), true
}

// clearColor forgets the colour and the flag: Thane no longer claims
// either.
func (m *labelMarks) clearColor() {
	m.Color, m.ColorBits, m.SetFlagged = "", nil, false
}
