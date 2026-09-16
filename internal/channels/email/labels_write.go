package email

import (
	"github.com/emersion/go-imap/v2"
)

// Writing and removing one label on one message, shared by the poller's
// derived labels (labels_apply.go) and email_mark (labels_mark.go). Every
// change is a +FLAGS or -FLAGS of named flags; nothing replaces a
// message's flags wholesale.

// Why a label's colour was not written.
const (
	// colorSkippedOperatorFlag: the message carries a flag Thane did not
	// set, or one the operator recoloured, and a colour never goes over it.
	colorSkippedOperatorFlag = "operator_flag"

	// colorSkippedKeywords: the folder does not keep keywords, and the
	// colour's bits are keywords.
	colorSkippedKeywords = "keywords_not_permanent"

	// colorSkippedThaneFlag: the message already carries the flag Thane
	// wrote for another label, and a message shows one colour.
	colorSkippedThaneFlag = "thane_flag_kept"
)

// labelWrite is what writing one label to one message did.
type labelWrite struct {
	keywordAdded bool
	colorWritten bool
	colorSkipped string
}

// writeLabel writes l to one message: its colour first, then its keyword
// with +FLAGS. The colour goes first so the flags it is decided on were
// read as recently as the message allows. Keywords, colour bits
// included, are written only when the folder keeps them. marks and st
// are updated with what was written; the caller saves marks. An error is
// a STORE the server refused, and what was written before it stays
// recorded.
func (s *flagSession) writeLabel(uid uint32, st *flagState, l mailLabel, marks *labelMarks) (labelWrite, error) {
	permanent := s.verdict == KeywordsPermanent
	w, err := s.writeColor(uid, st, l, marks, permanent)
	if err != nil {
		return w, err
	}
	if l.Keyword != "" && permanent && !stringFlagIn(st.Flags, l.Keyword) {
		kw := []imap.Flag{imap.Flag(l.Keyword)}
		if err := s.store(uid, imap.StoreFlagsAdd, kw); err != nil {
			return w, err
		}
		st.applyStore(imap.StoreFlagsAdd, kw)
		marks.bind(s.copyOf(uid))
		marks.addKeyword(l.Keyword)
		w.keywordAdded = true
	}
	return w, nil
}

// writeColor writes l's colour, as +FLAGS \Flagged with the colour's bits
// and -FLAGS for any other bit the message carries, and only on a
// message without \Flagged. A message already carrying the flag Thane
// wrote for l keeps it; any other flag, the operator's or another
// label's, is never written over.
func (s *flagSession) writeColor(uid uint32, st *flagState, l mailLabel, marks *labelMarks, permanent bool) (labelWrite, error) {
	var w labelWrite
	bits, hasColor := l.colorBits()
	if !hasColor {
		return w, nil
	}
	isFlagged := flagged(st.Flags)
	switch {
	case bits != 0 && !permanent:
		w.colorSkipped = colorSkippedKeywords
		return w, nil
	case isFlagged && marks.ownsFlag(st.Flags) && marks.Color == l.Color:
		return w, nil
	case isFlagged && marks.ownsFlag(st.Flags):
		w.colorSkipped = colorSkippedThaneFlag
		return w, nil
	case isFlagged:
		w.colorSkipped = colorSkippedOperatorFlag
		return w, nil
	}

	set, clear := splitColorBits(bits)
	add := append([]imap.Flag{imap.FlagFlagged}, set...)
	if err := s.store(uid, imap.StoreFlagsAdd, add); err != nil {
		return w, err
	}
	st.applyStore(imap.StoreFlagsAdd, add)
	marks.bind(s.copyOf(uid))
	marks.setColor(l.Color, set)
	w.colorWritten = true

	var stray []imap.Flag
	for _, f := range clear {
		if stringFlagIn(st.Flags, string(f)) {
			stray = append(stray, f)
		}
	}
	if err := s.store(uid, imap.StoreFlagsDel, stray); err != nil {
		// The flag reads another colour until the stray bit goes, and
		// ownsFlag will not match it, so Thane will not touch it again.
		return w, err
	}
	st.applyStore(imap.StoreFlagsDel, stray)
	return w, nil
}

// labelRemoval is what removing one label from one message did.
type labelRemoval struct {
	removed bool

	// kept names what the message carries for the label that Thane did
	// not set, so it stayed: "keyword", "flag", or both.
	kept []string
}

// removeLabel removes from one message what Thane recorded setting for
// l: its keyword when Thane added it, and its flag with the colour bits
// when the flag is still exactly the one Thane wrote. Anything else the
// message carries for the label is the operator's and stays. marks and
// st are updated; the caller saves marks.
func (s *flagSession) removeLabel(uid uint32, st *flagState, l mailLabel, marks *labelMarks) (labelRemoval, error) {
	var r labelRemoval
	if l.Keyword != "" {
		switch {
		case stringFlagIn(st.Flags, l.Keyword) && marks.hasKeyword(l.Keyword):
			kw := []imap.Flag{imap.Flag(l.Keyword)}
			if err := s.store(uid, imap.StoreFlagsDel, kw); err != nil {
				return r, err
			}
			st.applyStore(imap.StoreFlagsDel, kw)
			marks.dropKeyword(l.Keyword)
			r.removed = true
		case stringFlagIn(st.Flags, l.Keyword):
			r.kept = append(r.kept, "keyword")
		default:
			marks.dropKeyword(l.Keyword)
		}
	}

	bits, hasColor := l.colorBits()
	if !hasColor {
		return r, nil
	}
	showsColor := flagged(st.Flags) && colorBitsIn(st.Flags) == bits
	switch {
	case marks.Color == l.Color && marks.ownsFlag(st.Flags):
		set, _ := splitColorBits(bits)
		del := append([]imap.Flag{imap.FlagFlagged}, set...)
		if err := s.store(uid, imap.StoreFlagsDel, del); err != nil {
			return r, err
		}
		st.applyStore(imap.StoreFlagsDel, del)
		marks.clearColor()
		r.removed = true
	case marks.Color == l.Color:
		// The operator changed or cleared the flag Thane wrote; it is
		// theirs now, and Thane stops claiming it.
		marks.clearColor()
		if showsColor {
			r.kept = append(r.kept, "flag")
		}
	case showsColor:
		r.kept = append(r.kept, "flag")
	}
	return r, nil
}
