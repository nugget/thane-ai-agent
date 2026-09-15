package email

// labelsView is the part of an account's Email Accounts entry that says
// which labels its mail can carry and whether its INBOX keeps them.
// Both are omitted on an account whose mailbox carries no label, and on
// one whose access is read, where Thane writes no label, so such an
// entry renders as it did before labels existed. keywords renders once
// a read-write SELECT of INBOX has reported its PERMANENTFLAGS: the
// render reads the cached verdict, it never asks the server.
type labelsView struct {
	Labels   []labelEntryView `json:"labels,omitempty"`
	Keywords KeywordVerdict   `json:"keywords,omitempty"`
}

// labelEntryView is one label an account carries, as the entry shows it.
type labelEntryView struct {
	Label   string `json:"label"`
	Meaning string `json:"meaning"`

	// ShowsAs says how the label appears on a message, such as "blue
	// flag and keyword thane-contact".
	ShowsAs string `json:"shows_as"`

	// Apply names the rule under which Go applies the label itself; the
	// model never applies or removes such a label.
	Apply string `json:"apply,omitempty"`
}

// newLabelsView projects the labels an account carries and INBOX's
// keyword verdict for its entry.
func (s *Service) newLabelsView(cfg AccountConfig) labelsView {
	labels := s.manager.accountLabels(cfg.Name)
	if len(labels) == 0 {
		return labelsView{}
	}
	view := labelsView{Labels: make([]labelEntryView, 0, len(labels))}
	for _, l := range labels {
		view.Labels = append(view.Labels, labelEntryView{Label: l.Name, Meaning: l.Meaning, ShowsAs: l.showsAs(), Apply: l.Apply})
	}
	if client := s.manager.clients[cfg.Name]; client != nil {
		if verdict, ok := client.keywordVerdict(DefaultFolder); ok {
			view.Keywords = verdict
		}
	}
	return view
}

// labelRows fills each row of a list or search result from one account
// with the labels it carries and the label whose flag it shows. It reads
// each message's identity from the envelopes listed, not from the row:
// a row's message_id is cut to maxMessageIDOutput, and Thane's record is
// keyed by the whole Message-ID, so a row would lose flag_label that
// email_read shows for the same message.
func (s *Service) labelRows(account string, listed ListResult, resp *listResponse) {
	envs := make(map[uint32]Envelope, len(listed.Envelopes))
	for _, env := range listed.Envelopes {
		envs[env.UID] = env
	}
	for i := range resp.Messages {
		m := &resp.Messages[i]
		env, ok := envs[m.UID]
		if !ok {
			// No envelope names this row's copy, so it claims no flag.
			m.Labels, m.FlagLabel = s.labelsFor(account, messageCopy{}, "", m.Flags)
			continue
		}
		m.Labels, m.FlagLabel = s.labelsFor(account, newMessageCopy(listed.Folder, listed.UIDValidity, env.UID), env.MessageID, m.Flags)
	}
}

// labelsFor returns the labels the account carries whose keyword flags
// hold, and the label whose flag the message shows (flagLabel). c names
// the copy the flags were read from and messageID is its whole
// Message-ID. Both results are empty on an account that carries no
// label, so its rows render as they did before labels existed.
func (s *Service) labelsFor(account string, c messageCopy, messageID string, flags []string) ([]string, string) {
	if s == nil || s.manager == nil {
		return nil, ""
	}
	labels := s.manager.accountLabels(account)
	if len(labels) == 0 {
		return nil, ""
	}
	return labels.carried(flags), s.flagLabel(account, c, messageID, flags)
}

// flagLabel names the label whose flag a message shows: only when
// Thane's record says Thane wrote that flag on this very copy and the
// copy still carries it exactly as written. Go states this from the
// record, so the model never infers a label's flag from its colour,
// which the operator can set too. "" means the flag, if any, is
// someone's attention flag.
func (s *Service) flagLabel(account string, c messageCopy, messageID string, flags []string) string {
	if s.state == nil || messageID == "" || !flagged(flags) || !s.manager.labels.hasColors() {
		return ""
	}
	marks, err := loadLabelMarks(s.state, account, messageID)
	if err != nil {
		s.logger.Warn("email flag_label not shown: Thane's label record is unreadable", "account", account, "message_id", messageID, "error", err)
		return ""
	}
	if !marks.covers(c) || !marks.ownsFlag(flags) {
		return ""
	}
	if l, ok := s.manager.labels.byColor(marks.Color); ok {
		return l.Name
	}
	return ""
}
