package email

import (
	"context"
	"slices"
)

// emailPollerAuthor is who the label record names for marks the poller
// wrote.
const emailPollerAuthor = "email_poller"

// applyDerivedLabels applies every label whose apply rule is
// contact_matched to the new INBOX messages whose sender matches exactly
// one contact record, on an account whose mailbox.labels names it. The
// contact directory decides, not the model, and before the wake is
// dispatched, so the pass that reads a message finds the label already
// there. It returns envs with every mark Thane's records say it set taken
// out of their flags, which is what the wake shows (withoutThaneMarks).
// Each label goes on a message at most once, so a mark the operator takes
// off stays off when a failed dispatch lists the message again. A message
// with no Message-ID is skipped, because Thane's record of what it set is
// keyed by it. Every failure is logged and the poll goes on: a missing
// label must never cost a wake. Nothing here marks mail seen.
func (p *Poller) applyDerivedLabels(ctx context.Context, account string, client *Client, envs []Envelope) []Envelope {
	carried := p.manager.accountLabels(account)
	if len(carried) == 0 || len(envs) == 0 || p.state == nil || client == nil {
		return envs
	}
	envs = p.withoutThaneMarks(account, envs)
	labels := carried.appliedBy(LabelApplyContactMatched)
	if len(labels) == 0 {
		return envs
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
		return envs
	}
	slices.Sort(uids)

	err := client.withFlagSession(ctx, DefaultFolder, func(s *flagSession) error {
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
	return envs
}

// withoutThaneMarks returns envs with the marks Thane's records say it
// set taken out of each message's flags, so a wake's flags never include
// a mark Thane set. A message listed again after a failed dispatch, or
// moved back into INBOX, already carries Thane's labels; without this
// its wake would show Go's flag as if someone had flagged the message.
// envs is not changed. A record Thane cannot read leaves the flags as
// listed.
func (p *Poller) withoutThaneMarks(account string, envs []Envelope) []Envelope {
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
		if marks.covers(env.Size) {
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
	marks, err := loadLabelMarks(p.state, account, st.MessageID)
	if err != nil {
		p.logger.Warn("email labels not applied: Thane's label record is unreadable", "account", account, "folder", DefaultFolder, "uid", uid, "message_id", st.MessageID, "error", err)
		return
	}
	if !marks.covers(st.Size) {
		p.logger.Info("email labels not applied: another copy of this message carries Thane's label record",
			"account", account, "folder", DefaultFolder, "uid", uid, "message_id", st.MessageID, "size", st.Size, "record_size", marks.Size)
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
		marks.bind(st.Size)
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
