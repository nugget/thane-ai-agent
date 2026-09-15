package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// email_mark's label path and the flag rules labels bring: adding flag
// "flagged" turns a colour Thane wrote into the operator's attention
// flag, removing it takes Thane's colour with it, and flag "answered" is
// refused on a drafts account.

// errNoLabelRecord refuses a model-applied label on a service without a
// state store.
var errNoLabelRecord = errors.New("email_mark cannot apply or remove a label here: this Thane runs without an operational state store, so it could not record which marks it set and could never tell them from the operator's; nothing was changed. Flag the message with flag \"flagged\" if it needs the operator")

// applyRuleMeanings says, for a refusal, when Go applies a label.
var applyRuleMeanings = map[string]string{
	LabelApplyContactMatched: "at poll time, to new INBOX mail whose sender matches a contact record",
}

// labelMarkResponse is the result of email_mark with label.
type labelMarkResponse struct {
	Action       string         `json:"action"`
	Account      string         `json:"account"`
	Folder       string         `json:"folder"`
	Label        string         `json:"label"`
	UIDsAffected []uint32       `json:"uids_affected"`
	UIDsNotFound []uint32       `json:"uids_not_found"`
	Refused      []labelRefusal `json:"refused"`
}

// labelRefusal is one message a label call left alone, and why.
type labelRefusal struct {
	UID    uint32 `json:"uid"`
	Reason string `json:"reason"`
}

// markProblems validates email_mark's arguments together, so one error
// names every problem, and returns the label a label call names.
func (t *Tools) markProblems(action MarkAction, labelName string) (mailLabel, []string) {
	var problems []string
	if len(action.UIDs) == 0 {
		problems = append(problems, "uids is required: pass uids (array of integers) or uid (single integer) from an email_list or email_search result in the same account and folder")
	}
	if p := batchProblem("email_mark", len(action.UIDs), "nothing was changed"); p != "" {
		problems = append(problems, p)
	}
	markable := t.labels().markable()
	var l mailLabel
	switch {
	case action.Flag != "" && labelName != "":
		problems = append(problems, fmt.Sprintf("pass flag or label, not both (got flag %q and label %q): make one call per change", action.Flag, labelName))
	case labelName != "":
		var p string
		l, p = t.labelProblem(labelName, markable)
		if p != "" {
			problems = append(problems, p)
		}
	case action.Flag == "":
		p := fmt.Sprintf("flag is required (one of %s)", strings.Join(ValidFlagNames(), ", "))
		if len(markable) > 0 {
			p = fmt.Sprintf("flag (one of %s) or label (one of %s) is required", strings.Join(ValidFlagNames(), ", "), strings.Join(markable, ", "))
		}
		problems = append(problems, p)
	default:
		if _, ok := ValidFlag(action.Flag); !ok {
			problems = append(problems, fmt.Sprintf("flag %q is not supported (one of %s)", action.Flag, strings.Join(ValidFlagNames(), ", ")))
		}
	}
	return l, problems
}

// labelProblem resolves a label email_mark was given, or says why it
// cannot take it.
func (t *Tools) labelProblem(name string, markable []string) (mailLabel, string) {
	l, ok := t.labels().named(name)
	switch {
	case !ok && len(markable) == 0:
		return l, fmt.Sprintf("label %q is not one email_mark can set: this Thane declares no label the model applies", name)
	case !ok:
		return l, fmt.Sprintf("label %q is not declared (email_mark takes one of %s)", name, strings.Join(markable, ", "))
	case l.Apply != "":
		return l, fmt.Sprintf("label %q is applied by Go (apply: %s, %s), never by email_mark, and removing it is Go's too; nothing was changed. Flag the message with flag \"flagged\" if it needs the operator", name, l.Apply, applyRuleMeanings[l.Apply])
	}
	return l, ""
}

// names returns the labels' names.
func (s labelSet) names() []string {
	out := make([]string, len(s))
	for i, l := range s {
		out[i] = l.Name
	}
	return out
}

// accountLabelRefusal refuses a label the account's mailbox does not
// carry, naming the ones it does.
func (s *Service) accountLabelRefusal(tool, verb, outcome, account, name string) error {
	lists := "no label"
	if names := s.manager.accountLabels(account).names(); len(names) > 0 {
		lists = "only " + strings.Join(names, ", ")
	}
	return fmt.Errorf("%s cannot %s label %q on account %q: its mailbox carries %s, as its Email Accounts entry lists; %s", tool, verb, name, account, lists, outcome)
}

// markLabel applies or removes a model-applied label on the given
// messages. Each message is judged on its own: one carrying any flag but
// this label's own refuses a colour label, and a removal leaves whatever
// Thane did not record setting; both are listed under refused. A label
// the account does not carry, or a folder that does not keep keywords,
// refuses the whole call before anything changes.
func (t *Tools) markLabel(ctx context.Context, acct ResolvedAccount, action MarkAction, l mailLabel) (string, error) {
	if t.service.state == nil {
		return "", errNoLabelRecord
	}
	folder := normalizeFolder(action.Folder)
	resp := labelMarkResponse{Action: "label_added", Account: acct.Name, Folder: folder, Label: l.Name, UIDsAffected: []uint32{}, UIDsNotFound: []uint32{}, Refused: []labelRefusal{}}
	verb := "apply"
	if !action.Add {
		resp.Action, verb = "label_removed", "remove"
	}
	if _, ok := t.service.manager.accountLabels(acct.Name).named(l.Name); !ok {
		return "", t.service.accountLabelRefusal("email_mark", verb, "nothing was changed", acct.Name, l.Name)
	}
	by := draftAuthor(ctx)
	err := acct.Client.withFlagSession(ctx, folder, func(s *flagSession) error {
		if l.needsKeywords() && s.verdict != KeywordsPermanent {
			return labelKeywordRefusal(acct.Name, folder, l, action.Add, s)
		}
		states, err := s.states(action.UIDs)
		if err != nil {
			return err
		}
		for _, uid := range action.UIDs {
			st, ok := states[uid]
			if !ok {
				resp.UIDsNotFound = append(resp.UIDsNotFound, uid)
				continue
			}
			reason, err := t.service.markLabelOne(s, acct.Name, uid, st, l, action.Add, by)
			if err != nil {
				return fmt.Errorf("email_mark stopped at uid %d in folder %q of account %q, with uids %v already done: %w", uid, folder, acct.Name, resp.UIDsAffected, err)
			}
			if reason != "" {
				resp.Refused = append(resp.Refused, labelRefusal{UID: uid, Reason: reason})
				continue
			}
			resp.UIDsAffected = append(resp.UIDsAffected, uid)
		}
		return nil
	})
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	if len(resp.UIDsAffected) == 0 && len(resp.Refused) > 0 {
		resp.Action = "refused"
	}
	t.service.logger.Info("email label marked",
		"account", acct.Name,
		"folder", folder,
		"label", l.Name,
		"action", resp.Action,
		"affected", len(resp.UIDsAffected),
		"refused", len(resp.Refused),
		"not_found", len(resp.UIDsNotFound),
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	t.service.recordOp("email_mark", acct.Name, folder, fmt.Sprintf("%s %s on %d", resp.Action, l.Name, len(resp.UIDsAffected)))
	return marshalResponse(resp)
}

// markLabelOne applies or removes l on one message. It returns the
// reason it left the message alone, or "" when the message is now in the
// requested state as far as Thane's marks go. An error is a STORE the
// server refused or a record Thane could not read or write.
func (s *Service) markLabelOne(fs *flagSession, account string, uid uint32, st *flagState, l mailLabel, add bool, by string) (string, error) {
	if st.MessageID == "" {
		return "it has no Message-ID, which is how Thane records the marks it sets, so Thane could never tell them from the operator's; nothing was changed on it", nil
	}
	marks, err := loadLabelMarks(s.state, account, st.MessageID)
	if err != nil {
		return "", err
	}
	if !marks.covers(st.Size) {
		return "another copy of this message, under the same Message-ID, carries the marks Thane recorded, and Thane keeps one record per Message-ID, so it could not tell its own marks on this copy from the operator's; nothing was changed on it", nil
	}
	marks.reconcile(st.Flags)
	var reason string
	switch {
	case add:
		if reason = colorRefusal(st, l, marks, s.manager.labels); reason == "" {
			_, err = fs.writeLabel(uid, st, l, &marks)
		}
	default:
		var r labelRemoval
		r, err = fs.removeLabel(uid, st, l, &marks)
		if len(r.kept) > 0 && !r.removed {
			reason = fmt.Sprintf("its %s for label %s is not one Thane set, so it stays; only the operator removes it", strings.Join(r.kept, " and "), l.Name)
		}
	}
	marks.By = by
	if serr := saveLabelMarks(s.state, &marks); serr != nil {
		s.logger.Warn("email label record not written; Thane will read these marks as the operator's", "account", account, "uid", uid, "message_id", st.MessageID, "label", l.Name, "error", serr)
		err = errors.Join(err, serr)
	}
	return reason, err
}

// colorRefusal says why l cannot go on a message, or "" when it can. A
// label's colour is written only on a message without a flag, and kept on
// one already carrying the flag Thane wrote for l; any other flag, the
// operator's or the one Thane wrote for another label, refuses it.
func colorRefusal(st *flagState, l mailLabel, marks labelMarks, vocabulary labelSet) string {
	if _, hasColor := l.colorBits(); !hasColor || !flagged(st.Flags) {
		return ""
	}
	if !marks.ownsFlag(st.Flags) {
		return fmt.Sprintf("it already carries a flag Thane did not set, most likely the operator's, and label %s's %s colour would change what that flag says; nothing was changed on it", l.Name, l.Color)
	}
	if marks.Color == l.Color {
		return ""
	}
	other := "a label"
	if o, ok := vocabulary.byColor(marks.Color); ok {
		other = "label " + o.Name
	}
	return fmt.Sprintf("it already carries the %s flag Thane wrote for %s, and a message shows one flag colour, so label %s's %s would replace it; nothing was changed on it", marks.Color, other, l.Name, l.Color)
}

// labelKeywordRefusal refuses a label the folder cannot keep, quoting the
// server's PERMANENTFLAGS.
func labelKeywordRefusal(account, folder string, l mailLabel, add bool, s *flagSession) error {
	verb := "apply"
	if !add {
		verb = "remove"
	}
	why := "list no flags at all, so no keyword is known to stay"
	if s.verdict == KeywordsSessionOnly {
		why = "lack \\*, so a new keyword would last only for Thane's session"
	}
	return fmt.Errorf("email_mark cannot %s label %q in folder %q of account %q: the label shows as %s, which is written with IMAP keywords, and this folder's PERMANENTFLAGS %s %s; nothing was changed. If the message needs the operator, flag it with flag \"flagged\" instead", verb, l.Name, folder, account, l.showsAs(), s.permanentList(), why)
}

// answeredRefusal refuses flag answered on an account whose delivery is
// drafts.
func (s *Service) answeredRefusal(ctx context.Context, acct ResolvedAccount) error {
	s.logger.Info("email answered mark refused",
		"account", acct.Name,
		"reason", "drafts_delivery",
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return fmt.Errorf("email_mark cannot set or clear answered on account %q: its delivery is drafts, so what Thane writes there is a draft, and a draft is not an answer; Thane cannot know when, or whether, the operator sends one; nothing was changed. A reply Thane drafted shows as thane_draft in the drafts folder", acct.Name)
}

// releaseThaneFlags hands every flag among uids that Thane wrote for a
// label over to the operator, and stops claiming it. With keepFlag,
// which follows email_mark adding flagged, it removes the colour bits
// Thane wrote, so the flag reads plain, as the operator's attention
// flag. Without it, which comes before email_mark removes flagged, it
// removes \Flagged together with those bits, so no colour keyword of
// Thane's is left behind to colour a flag the operator sets later. Bits
// the operator set, flags Thane did not write, and flags on another copy
// of a message are never touched. It returns the UIDs whose flag was
// Thane's.
func (s *Service) releaseThaneFlags(ctx context.Context, acct ResolvedAccount, folder string, uids []uint32, keepFlag bool) ([]uint32, error) {
	if s.state == nil || !s.manager.labels.hasColors() || len(uids) == 0 {
		return nil, nil
	}
	var released []uint32
	err := acct.Client.withFlagSession(ctx, folder, func(fs *flagSession) error {
		states, err := fs.states(uids)
		if err != nil {
			return err
		}
		done := make(map[uint32]bool, len(uids))
		for _, uid := range uids {
			st, ok := states[uid]
			if !ok || st.MessageID == "" || done[uid] {
				continue
			}
			done[uid] = true
			owned, err := s.releaseThaneFlag(ctx, fs, acct.Name, uid, st, keepFlag)
			if owned {
				released = append(released, uid)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return released, err
}

// releaseThaneFlag hands one message's flag over when Thane wrote it,
// and reports whether it did.
func (s *Service) releaseThaneFlag(ctx context.Context, fs *flagSession, account string, uid uint32, st *flagState, keepFlag bool) (bool, error) {
	marks, err := loadLabelMarks(s.state, account, st.MessageID)
	if err != nil || !marks.covers(st.Size) {
		return false, err
	}
	changed := marks.reconcile(st.Flags)
	owned := marks.SetFlagged
	if owned {
		del := make([]imap.Flag, 0, len(marks.ColorBits)+1)
		if !keepFlag {
			del = append(del, imap.FlagFlagged)
		}
		for _, b := range marks.ColorBits {
			del = append(del, imap.Flag(b))
		}
		if err := fs.store(uid, imap.StoreFlagsDel, del); err != nil {
			return false, err
		}
		s.logger.Info("email label flag handed to the operator",
			"account", account,
			"folder", fs.folder,
			"uid", uid,
			"message_id", st.MessageID,
			"color", marks.Color,
			"flag_kept", keepFlag,
			"loop_id", tools.LoopIDFromContext(ctx),
			"conversation_id", tools.ConversationIDFromContext(ctx),
		)
		marks.clearColor()
		changed = true
	}
	if !changed {
		return owned, nil
	}
	marks.By = draftAuthor(ctx)
	return owned, saveLabelMarks(s.state, &marks)
}

// flaggedClearNote is email_mark's note when flagged was added but a
// colour Thane wrote could not be cleared.
func flaggedClearNote(err error) string {
	return "The flag is set, but a flag colour Thane wrote for a label could not be cleared, so it may still show that colour: " + err.Error()
}

// unflagStopped is email_mark's error when removing flagged stopped
// because a flag Thane wrote could not be removed with its colour.
func unflagStopped(account, folder string, released []uint32, err error) error {
	return fmt.Errorf("email_mark did not remove flagged in folder %q of account %q: removing a flag Thane wrote for a label together with its colour keywords failed first, and removing the flag alone would leave those keywords behind; the flag stays on every message except uids %v, whose flag Thane had already removed. Retry the call: %w", folder, account, nonNilUIDs(released), err)
}
