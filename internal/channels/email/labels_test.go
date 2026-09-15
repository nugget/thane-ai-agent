package email

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
)

// TestFlagColorBitTable pins the colour encoding: each colour's
// $MailFlagBit0-2 keywords, read back whatever case the server uses, and
// the config vocabulary listing the same colours in bit order.
func TestFlagColorBitTable(t *testing.T) {
	tests := []struct {
		color string
		bits  []string
	}{
		{"red", nil},
		{"orange", []string{colorBit0}},
		{"yellow", []string{colorBit1}},
		{"green", []string{colorBit0, colorBit1}},
		{"blue", []string{colorBit2}},
		{"purple", []string{colorBit0, colorBit2}},
		{"grey", []string{colorBit1, colorBit2}},
	}
	for _, tt := range tests {
		t.Run(tt.color, func(t *testing.T) {
			value, ok := flagColorBits[tt.color]
			if !ok {
				t.Fatalf("no bits for %s", tt.color)
			}
			set, clear := splitColorBits(value)
			if got := flagNames(set); !slices.Equal(got, tt.bits) {
				t.Errorf("set bits = %v, want %v", got, tt.bits)
			}
			if len(set)+len(clear) != len(colorBitFlags) {
				t.Errorf("set %v and clear %v do not cover the three bits", set, clear)
			}
			lower := make([]string, len(tt.bits))
			for i, b := range tt.bits {
				lower[i] = strings.ToLower(b)
			}
			if got := colorBitsIn(lower); got != value {
				t.Errorf("colorBitsIn(%v) = %d, want %d", lower, got, value)
			}
			if got := flagColorOf(append([]string{`\FLAGGED`}, lower...)); got != tt.color {
				t.Errorf("flagColorOf(flagged + %v) = %q, want %q", lower, got, tt.color)
			}
			if got := flagColorOf(tt.bits); got != "" {
				t.Errorf("an unflagged message shows colour %q, want none", got)
			}
		})
	}
	if got := flagColorOf([]string{flagFlag, colorBit0, colorBit1, colorBit2}); got != "" {
		t.Errorf("all three bits name colour %q, want none", got)
	}
	colors := platformconfig.EmailLabelColors()
	if len(colors) != len(flagColorBits) {
		t.Fatalf("config colours %v, table %v", colors, flagColorBits)
	}
	for i, c := range colors {
		if flagColorBits[c] != uint8(i) {
			t.Errorf("config colour %d is %s, whose table value is %d", i, c, flagColorBits[c])
		}
	}
}

// TestKeywordVerdictOf pins how PERMANENTFLAGS reads.
func TestKeywordVerdictOf(t *testing.T) {
	tests := []struct {
		name      string
		permanent []imap.Flag
		want      KeywordVerdict
	}{
		{"wildcard keeps keywords", []imap.Flag{imap.FlagSeen, imap.FlagFlagged, imap.FlagWildcard}, KeywordsPermanent},
		{"system flags only", []imap.Flag{imap.FlagSeen, imap.FlagFlagged}, KeywordsSessionOnly},
		{"a listed keyword without the wildcard", []imap.Flag{imap.FlagSeen, "$MailFlagBit0"}, KeywordsSessionOnly},
		{"nothing listed", nil, KeywordsUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keywordVerdictOf(tt.permanent); got != tt.want {
				t.Errorf("keywordVerdictOf(%v) = %s, want %s", tt.permanent, got, tt.want)
			}
		})
	}
}

// TestLabelVocabulary pins how a label shows, which labels each tool
// offers, and which a message carries.
func TestLabelVocabulary(t *testing.T) {
	set := newLabelSet(map[string]LabelConfig{
		"waiting": waitingLabel,
		"contact": contactLabel,
		"urgent":  {Meaning: "Urgent", Color: "red"},
		"later":   {Meaning: "Read later", Keyword: "thane-later"},
	})
	names := make([]string, len(set))
	for i, l := range set {
		names[i] = l.Name
	}
	if !slices.Equal(names, []string{"contact", "later", "urgent", "waiting"}) {
		t.Errorf("labels = %v, want sorted by name", names)
	}
	tests := []struct {
		name         string
		wantShows    string
		wantKeywords bool
	}{
		{"contact", "blue flag and keyword thane-contact", true},
		{"later", "keyword thane-later", true},
		{"urgent", "red flag", false},
		{"waiting", "yellow flag and keyword thane-waiting", true},
	}
	for _, tt := range tests {
		l, _ := set.named(tt.name)
		if got := l.showsAs(); got != tt.wantShows {
			t.Errorf("%s shows as %q, want %q", tt.name, got, tt.wantShows)
		}
		if got := l.needsKeywords(); got != tt.wantKeywords {
			t.Errorf("%s needsKeywords = %v, want %v", tt.name, got, tt.wantKeywords)
		}
	}
	if got := set.markable(); !slices.Equal(got, []string{"later", "urgent", "waiting"}) {
		t.Errorf("markable = %v: a label Go applies must not be offered", got)
	}
	if got := set.searchable(); !slices.Equal(got, []string{"contact", "later", "waiting"}) {
		t.Errorf("searchable = %v: a colour-only label has no keyword to find", got)
	}
	if got := set.carried([]string{flagFlag, "THANE-CONTACT", "thane-waiting", colorBit2}); !slices.Equal(got, []string{"contact", "waiting"}) {
		t.Errorf("carried = %v", got)
	}
	if got := set.carried([]string{flagFlag}); got != nil {
		t.Errorf("a red flag carries %v, want no label: a colour alone cannot say who set it", got)
	}
}

// TestLabelMarksRecord pins Thane's provenance record: keyed by account
// and bare Message-ID, round-tripped with what Thane set, deleted once it
// claims nothing, refused when unreadable, and the ownership rule.
func TestLabelMarksRecord(t *testing.T) {
	state := testOpstate(t)
	key := labelMarksKey("primary", "abc@example.com")
	if key != labelMarksKey("primary", "<abc@example.com>") || len(key) != 64 {
		t.Fatalf("key = %q: want one hex SHA-256 for the bare and bracketed Message-ID", key)
	}
	m, err := loadLabelMarks(state, "primary", "<abc@example.com>")
	if err != nil || !m.empty() || m.MessageID != "abc@example.com" || m.Account != "primary" {
		t.Fatalf("fresh record = %+v, %v", m, err)
	}
	m.addKeyword("thane-contact")
	m.setColor("blue", []imap.Flag{colorBit2})
	m.By = emailPollerAuthor
	if err := saveLabelMarks(state, &m); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := state.Get(labelMarksNamespace, key)
	if err != nil || raw == "" {
		t.Fatalf("stored row = %q, %v", raw, err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored row is not JSON: %v", err)
	}
	for _, key := range []string{"keywords", "color", "color_bits", "set_flagged", "by", "updated_at"} {
		if _, ok := stored[key]; !ok {
			t.Errorf("stored row lacks %s: %s", key, raw)
		}
	}
	back, err := loadLabelMarks(state, "primary", "abc@example.com")
	if err != nil || !slices.Equal(back.Keywords, []string{"thane-contact"}) || back.Color != "blue" || !slices.Equal(back.ColorBits, []string{colorBit2}) || !back.SetFlagged {
		t.Fatalf("loaded = %+v, %v", back, err)
	}

	back.dropKeyword("THANE-CONTACT")
	back.clearColor()
	if err := saveLabelMarks(state, &back); err != nil {
		t.Fatalf("save empty: %v", err)
	}
	if raw, _ := state.Get(labelMarksNamespace, key); raw != "" {
		t.Errorf("a record that claims nothing must be deleted, got %s", raw)
	}

	if err := state.Set(labelMarksNamespace, labelMarksKey("primary", "bad@example.com"), "{"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLabelMarks(state, "primary", "bad@example.com"); err == nil {
		t.Error("an unreadable record must be an error, never read as empty")
	}

	blue := labelMarks{SetFlagged: true, Color: "blue", ColorBits: []string{colorBit2}}
	red := labelMarks{SetFlagged: true, Color: "red"}
	tests := []struct {
		name  string
		marks labelMarks
		flags []string
		want  bool
	}{
		{"Thane's blue flag", blue, []string{flagFlag, colorBit2}, true},
		{"read back in another case", blue, []string{`\FLAGGED`, "$mailflagbit2"}, true},
		{"recoloured by the operator", blue, []string{flagFlag, colorBit0, colorBit2}, false},
		{"bits cleared by the operator", blue, []string{flagFlag}, false},
		{"unflagged by the operator", blue, []string{colorBit2}, false},
		{"Thane never set the flag", labelMarks{Color: "blue", ColorBits: []string{colorBit2}}, []string{flagFlag, colorBit2}, false},
		{"Thane's red flag", red, []string{flagFlag}, true},
		{"a red flag the operator coloured", red, []string{flagFlag, colorBit1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.marks.ownsFlag(tt.flags); got != tt.want {
				t.Errorf("ownsFlag(%v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

// TestLabelMarksProvenance pins what the record lets Thane claim: only
// marks the message still shows as Thane's (reconcile), a wake's flags
// without them (strip), only the copy Thane marked (covers), and a
// record that remembers a derived label is kept.
func TestLabelMarksProvenance(t *testing.T) {
	marked := newMessageCopy("inbox", 7, 100)
	blue := func() labelMarks {
		return labelMarks{Copy: marked, Keywords: []string{"thane-contact"}, Color: "blue", ColorBits: []string{colorBit2}, SetFlagged: true}
	}
	tests := []struct {
		name         string
		flags        []string
		wantChanged  bool
		wantKeywords []string
		wantFlagged  bool
		wantStrip    []string
	}{
		{"everything still Thane's", []string{flagSeen, "thane-contact", flagFlag, colorBit2}, false, []string{"thane-contact"}, true, []string{flagSeen}},
		{"the operator unflagged", []string{"thane-contact", colorBit2}, true, []string{"thane-contact"}, false, []string{colorBit2}},
		{"the operator recoloured", []string{"thane-contact", flagFlag, colorBit0, colorBit2}, true, []string{"thane-contact"}, false, []string{flagFlag, colorBit0, colorBit2}},
		{"the operator removed the keyword", []string{flagFlag, colorBit2}, true, nil, true, nil},
		{"the operator took everything off", nil, true, nil, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blue().strip(tt.flags); !slices.Equal(got, tt.wantStrip) {
				t.Errorf("strip(%v) = %v, want %v", tt.flags, got, tt.wantStrip)
			}
			m := blue()
			if got := m.reconcile(tt.flags); got != tt.wantChanged {
				t.Errorf("reconcile(%v) changed = %v, want %v", tt.flags, got, tt.wantChanged)
			}
			if !slices.Equal(m.Keywords, tt.wantKeywords) || m.SetFlagged != tt.wantFlagged || (m.Color != "") != tt.wantFlagged {
				t.Errorf("after reconcile(%v): %+v", tt.flags, m)
			}
		})
	}

	m := blue()
	covers := []struct {
		name  string
		marks labelMarks
		copy  messageCopy
		want  bool
	}{
		{"the copy Thane marked, INBOX in any case", m, newMessageCopy("INBOX", 7, 100), true},
		{"another UID in the same folder", m, newMessageCopy("INBOX", 7, 101), false},
		{"the same UID after the folder was rebuilt", m, newMessageCopy("INBOX", 8, 100), false},
		{"the same UID in another folder", m, newMessageCopy("Archive", 7, 100), false},
		{"a folder that reports no UIDVALIDITY", m, newMessageCopy("INBOX", 0, 100), false},
		{"an unbound record that claims nothing", labelMarks{Derived: []string{"contact"}}, newMessageCopy("Archive", 3, 9), true},
		{"an unbound record that claims a mark", labelMarks{Keywords: []string{"thane-contact"}}, newMessageCopy("INBOX", 7, 100), false},
	}
	for _, tt := range covers {
		if got := tt.marks.covers(tt.copy); got != tt.want {
			t.Errorf("covers, %s: %v, want %v", tt.name, got, tt.want)
		}
	}
	var fresh labelMarks
	fresh.bind(newMessageCopy("INBOX", 7, 120))
	fresh.bind(newMessageCopy("INBOX", 7, 130))
	if fresh.Copy.UID != 120 {
		t.Errorf("bind rebound the record to uid %d", fresh.Copy.UID)
	}
	fresh.addDerived("contact")
	if fresh.empty() || !fresh.hasDerived("contact") {
		t.Errorf("a record remembering a derived label must be kept: %+v", fresh)
	}
}

// TestAccountLabels pins that an account carries only the labels its
// mailbox.labels names, and none when its access is read.
func TestAccountLabels(t *testing.T) {
	m := NewManager(Config{
		Labels: map[string]LabelConfig{"contact": contactLabel, "waiting": waitingLabel},
		Accounts: []AccountConfig{
			{Name: "personal", Policy: PolicyConfig{Access: AccessOrganize}, Mailbox: MailboxConfig{Labels: []string{"contact"}}},
			{Name: "thane", Policy: PolicyConfig{Access: AccessOrganize}},
			{Name: "reader", Policy: PolicyConfig{Access: AccessRead}, Mailbox: MailboxConfig{Labels: []string{"contact"}}},
		},
	}, quietSlog())
	tests := []struct {
		account string
		want    []string
	}{
		{"personal", []string{"contact"}},
		{"thane", nil},
		{"reader", nil},
		{"missing", nil},
	}
	for _, tt := range tests {
		if got := m.accountLabels(tt.account).names(); !slices.Equal(got, tt.want) {
			t.Errorf("account %s carries %v, want %v", tt.account, got, tt.want)
		}
	}
	if l, ok := m.labels.byColor("yellow"); !ok || l.Name != "waiting" {
		t.Errorf("byColor(yellow) = %v, %v", l.Name, ok)
	}
	if _, ok := m.labels.byColor(""); ok {
		t.Error("byColor(\"\") found a label")
	}
}
