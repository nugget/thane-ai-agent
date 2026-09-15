package email

import (
	"context"
	"fmt"
)

// The drafts folder is resolved by role, never by a name Go assumes:
// the account's drafts_folder, then the folder the cached listing marks
// \Drafts, then one fresh LIST. When none of those answers, a message
// the policy would draft is refused, and nothing is sent in its place.

// draftsFolder returns the folder holding the account's drafts role, or
// "" when none does or the listing failed. The refuse-only guards that
// call it (drafts as a move source or destination, drafts as a mark
// target) have nothing to refuse without a folder; the send path uses
// sendDraftsFolder, which tells a failed listing from an absence.
func (s *Service) draftsFolder(ctx context.Context, acct ResolvedAccount) string {
	return s.newFolderResolver(acct).folder(ctx, RoleDrafts)
}

// sendDraftsFolder resolves where a drafted message goes. An empty
// folder with a nil error means no folder has the drafts role, which
// refuses the draft; an error is a failed listing, a fault in delivery
// rather than a decision.
func (s *Service) sendDraftsFolder(ctx context.Context, acct ResolvedAccount) (string, error) {
	r := s.newFolderResolver(acct)
	folder := r.folder(ctx, RoleDrafts)
	if folder == "" && r.err != nil {
		return "", fmt.Errorf("list the folders of account %q to find its drafts folder: %w", acct.Name, r.err)
	}
	return folder, nil
}

// noDraftsFolderReason is the one-sentence refusal for a draft with no
// folder to go to. It names the gap and the only fix, which is the
// operator's. When the caller asked for the draft, it also forbids the
// one retry that would get the message out: the same call without
// draft: true, which the policy may send.
func noDraftsFolderReason(cfg AccountConfig, requested bool) string {
	next := "and do not write the message from any other account"
	if requested {
		next = "and until they do, neither resend it without the draft: true you asked for nor write it from any other account"
	}
	return fmt.Sprintf("Email not drafted: no folder on account %q has the drafts role, because drafts_folder is not configured and the server marks no folder with the drafts special-use attribute, so nothing was sent or drafted; ask the operator to configure drafts_folder for this account, %s.", cfg.Name, next)
}
