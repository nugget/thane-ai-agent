package email

import (
	"context"
	"slices"

	"github.com/emersion/go-imap/v2"
)

// A row in the drafts folder that is one of Thane's open drafts carries
// thane_draft {draft_id} in email_list, email_search, and email_read, so
// the model can tell its own drafts from the operator's without asking.
// The match is on the pair the ledger recorded, UID and Message-ID
// together: a UID alone can come back after the folder is rebuilt,
// holding the operator's draft. A row marked \Deleted is never
// annotated, because the identity proof never passes it. The annotation
// reads the ledger, and lists the account's folders at most once when
// its drafts folder is not known yet and it has open drafts to mark; the
// draft tools prove ownership again before they act.

// thaneDraftRef marks a row as one of Thane's open drafts.
type thaneDraftRef struct {
	DraftID string `json:"draft_id"`
}

// draftRowKey is the pair a row must carry to be annotated.
type draftRowKey struct {
	UID       uint32
	MessageID string
}

// draftIndex maps drafts-folder rows to the open drafts they hold.
type draftIndex map[draftRowKey]string

// draftIndexFor returns the index for rows listed from folder, or nil
// when the account has no open draft to mark there, folder is not the
// account's drafts folder, or the ledger cannot be read.
func (s *Service) draftIndexFor(ctx context.Context, acct ResolvedAccount, folder string) draftIndex {
	if s.state == nil {
		return nil
	}
	entries, err := s.draftEntries(acct.Name)
	if err != nil {
		s.logger.Debug("email draft annotation skipped", "account", acct.Name, "error", err)
		return nil
	}
	folder = normalizeFolder(folder)
	idx := draftIndex{}
	for _, e := range entries {
		if e.Stage != DraftStageOpen || e.UID == 0 || !namesFolder(e.Folder, folder) {
			continue
		}
		idx[draftRowKey{UID: e.UID, MessageID: normalizeMessageID(e.MessageID)}] = e.ID
	}
	if len(idx) == 0 {
		return nil
	}
	drafts := s.newFolderResolver(acct).folder(ctx, RoleDrafts)
	if drafts == "" || !namesFolder(folder, drafts) {
		return nil
	}
	return idx
}

// ref returns the annotation for a row, or nil.
func (idx draftIndex) ref(uid uint32, messageID string) *thaneDraftRef {
	id, ok := idx[draftRowKey{UID: uid, MessageID: normalizeMessageID(messageID)}]
	if !ok {
		return nil
	}
	return &thaneDraftRef{DraftID: id}
}

// refRow returns the annotation for a row with the given flags, or nil
// for a row marked \Deleted.
func (idx draftIndex) refRow(uid uint32, messageID string, flags []string) *thaneDraftRef {
	if len(idx) == 0 || slices.Contains(flags, string(imap.FlagDeleted)) {
		return nil
	}
	return idx.ref(uid, messageID)
}

// annotate marks the rows of a list or search result.
func (idx draftIndex) annotate(resp *listResponse) {
	if len(idx) == 0 {
		return
	}
	for i := range resp.Messages {
		m := &resp.Messages[i]
		m.ThaneDraft = idx.refRow(m.UID, m.MessageID, m.Flags)
	}
}
