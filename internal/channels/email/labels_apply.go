package email

import (
	"context"
	"slices"
)

// emailPollerAuthor is who the label record names for marks the poller
// wrote.
const emailPollerAuthor = "email_poller"

// applyDerivedLabels takes every mark Thane's records say it set out of
// the new INBOX messages' flags (withoutThaneMarks), and applies every
// label whose apply rule is contact_matched to the messages whose sender
// matches exactly one contact record, on an account whose mailbox.labels
// names it. envs were listed from INBOX under uidValidity. It returns
// envs with those marks taken out, which is what the wake shows. The
// marks come out whatever the account carries today: a message Thane
// marked before the operator took the label off the account still
// carries Thane's marks, and the record still names them.
//
// The contact directory decides, not the model, and before the wake is
// dispatched, so the pass that reads a message finds the label already
// there. Each label goes on a message at most once, so a mark the
// operator takes off stays off when a failed dispatch lists the message
// again. A message with no Message-ID is skipped, because Thane's record
// of what it set is keyed by it. Every failure is logged and the poll
// goes on: a missing label must never cost a wake. Nothing here marks
// mail seen.
func (p *Poller) applyDerivedLabels(ctx context.Context, account string, client *Client, uidValidity uint32, envs []Envelope) []Envelope {
	if len(envs) == 0 || p.state == nil {
		return envs
	}
	shown := p.withoutThaneMarks(account, uidValidity, envs)
	labels := p.manager.accountLabels(account).appliedBy(LabelApplyContactMatched)
	if len(labels) == 0 || client == nil {
		return shown
	}
	lookup := newIdentityLookup(ctx, p.contacts, p.logger)
	var uids []uint32
	for _, env := range envs {
		if env.MessageID == "" || lookup.resolve(env.From).Status != ContactMatched {
			continue
		}
		uids = append(uids, env.UID)
	}
	if len(uids) == 0 {
		return shown
	}
	slices.Sort(uids)

	err := client.withFlagSession(ctx, DefaultFolder, func(s *flagSession) error {
		if s.uidValidity != uidValidity {
			// The folder was rebuilt since it was listed, so the listed
			// UIDs now name other messages, whose senders nobody looked up.
			p.logger.Info("email labels not applied: INBOX's UIDVALIDITY changed since it was listed", "account", account, "listed_uid_validity", uidValidity, "uid_validity", s.uidValidity)
			return nil
		}
		if s.verdict != KeywordsPermanent && client.claimVerdictWarning(DefaultFolder) {
			p.logger.Warn("email folder keeps no keywords permanently; label keywords and colours are skipped there",
				"account", account,
				"folder", DefaultFolder,
				"keywords", s.verdict,
				"permanent_flags", s.permanentList(),
			)
		}
		for _, uid := range uids {
			// Each message's flags are read just before it is written,
			// not once for the batch, so a flag the operator sets while
			// Thane works through the batch is seen rather than coloured.
			states, err := s.states([]uint32{uid})
			if err != nil {
				return err
			}
			if st, ok := states[uid]; ok {
				p.applyLabelsTo(s, account, uid, st, labels)
			}
		}
		return nil
	})
	if err != nil {
		p.logger.Warn("email labels not applied", "account", account, "folder", DefaultFolder, "messages", len(uids), "error", err)
	}
	return shown
}

// withoutThaneMarks returns envs with the marks Thane's records say it
// set on each listed copy taken out of its flags, so a wake's flags never
// include a mark Thane still claims. A message listed again after a
// failed dispatch, or one email_move put back into INBOX, already carries
// Thane's labels; without this its wake would show Go's flag as if
// someone had flagged the message. A copy the record does not describe,
// such as a second copy or one the operator moved back, keeps its flags:
// its marks are the operator's. envs is not changed. A record Thane
// cannot read leaves the flags as listed.
func (p *Poller) withoutThaneMarks(account string, uidValidity uint32, envs []Envelope) []Envelope {
	out := slices.Clone(envs)
	for i, env := range out {
		if env.MessageID == "" || len(env.Flags) == 0 {
			continue
		}
		marks, err := loadLabelMarks(p.state, account, env.MessageID)
		if err != nil {
			p.logger.Warn("email wake flags may include Thane's label marks: Thane's label record is unreadable", "account", account, "uid", env.UID, "message_id", env.MessageID, "error", err)
			continue
		}
		if marks.claims() && marks.covers(newMessageCopy(DefaultFolder, uidValidity, env.UID)) {
			out[i].Flags = marks.strip(env.Flags)
		}
	}
	return out
}

// applyLabelsTo writes each derived label the message has not had yet
// and records what Thane set. A failed STORE is logged and the next
// label is still tried; what was written before the failure stays
// recorded, and the failed label may be tried again if the message is
// listed again.
func (p *Poller) applyLabelsTo(s *flagSession, account string, uid uint32, st *flagState, labels labelSet) {
	if st.MessageID == "" {
		return
	}
	copyID := s.copyOf(uid)
	if !copyID.known() {
		p.logger.Info("email labels not applied: INBOX reports no UIDVALIDITY, so Thane could not record which copy it marked", "account", account, "folder", DefaultFolder, "uid", uid, "message_id", st.MessageID)
		return
	}
	marks, err := loadLabelMarks(p.state, account, st.MessageID)
	if err != nil {
		p.logger.Warn("email labels not applied: Thane's label record is unreadable", "account", account, "folder", DefaultFolder, "uid", uid, "message_id", st.MessageID, "error", err)
		return
	}
	if !marks.covers(copyID) {
		p.logger.Info("email labels not applied: Thane's label record for this Message-ID describes another copy of the message",
			"account", account, "folder", DefaultFolder, "uid", uid, "uid_validity", copyID.UIDValidity, "message_id", st.MessageID,
			"record_folder", marks.Copy.Folder, "record_uid_validity", marks.Copy.UIDValidity, "record_uid", marks.Copy.UID)
		return
	}
	changed := marks.reconcile(st.Flags)
	for _, l := range labels {
		if marks.hasDerived(l.Name) {
			continue
		}
		w, err := s.writeLabel(uid, st, l, &marks)
		changed = changed || w.keywordAdded || w.colorWritten
		if err != nil {
			p.logger.Warn("email label not applied", "account", account, "folder", DefaultFolder, "uid", uid, "message_id", st.MessageID, "label", l.Name, "keyword_added", w.keywordAdded, "color_written", w.colorWritten, "error", err)
			continue
		}
		marks.bind(copyID)
		marks.addDerived(l.Name)
		changed = true
		p.logger.Info("email label applied",
			"account", account,
			"folder", DefaultFolder,
			"uid", uid,
			"message_id", st.MessageID,
			"label", l.Name,
			"rule", l.Apply,
			"keyword_added", w.keywordAdded,
			"color_written", w.colorWritten,
			"color_skipped", w.colorSkipped,
		)
	}
	if !changed {
		return
	}
	marks.By = emailPollerAuthor
	if err := saveLabelMarks(p.state, &marks); err != nil {
		p.logger.Warn("email label record not written; Thane will read these marks as the operator's", "account", account, "uid", uid, "message_id", st.MessageID, "error", err)
	}
}
