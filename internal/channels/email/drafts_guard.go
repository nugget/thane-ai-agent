package email

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// The drafts protections keep email_move and email_mark away from the
// account's drafts folder, as a source and as a destination: it holds
// drafts waiting for the operator to send or discard, the operator's
// own and Thane's alike. A call resolves that folder once, by its role,
// through the call's folder resolver, and judges its source and its
// destination against that one answer. Resolution fails closed: a LIST
// that fails refuses the call rather than guess a name, because a guess
// that missed would let the call act on the real drafts folder. An
// account with no drafts folder at all has nothing to protect.

// protectedDrafts returns the drafts folder one email_move or
// email_mark call protects, or "" when the account has none. When the
// folder cannot be resolved, the error is the call's refusal and
// outcome finishes its sentence.
func (s *Service) protectedDrafts(ctx context.Context, tool string, acct ResolvedAccount, r *folderResolver, outcome string) (string, error) {
	drafts := r.folder(ctx, RoleDrafts)
	if r.err == nil {
		return drafts, nil
	}
	s.logger.Warn("email drafts folder unresolved; call refused",
		"tool", tool,
		"account", acct.Name,
		"error", r.err,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return "", fmt.Errorf("%s cannot tell which folder is account %q's drafts folder, because listing the account's folders failed (%w). That folder holds drafts waiting for the operator to send or discard, so the call stops rather than guess; %s. Try again once email_folders answers for this account", tool, acct.Name, r.err, outcome)
}

// refuseDraftsSource returns the refusal for a tool acting on mail in
// the drafts folder, or nil for any other folder and on an account with
// no drafts folder. Neither moving mail out of it nor changing its
// flags is Thane's to do. outcome finishes the sentence.
func (s *Service) refuseDraftsSource(ctx context.Context, tool string, acct ResolvedAccount, drafts, folder, outcome string) error {
	if drafts == "" || !namesFolder(normalizeFolder(folder), drafts) {
		return nil
	}
	s.logger.Info("email drafts folder refused as source",
		"tool", tool,
		"account", acct.Name,
		"folder", drafts,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return fmt.Errorf("%s cannot act on mail in %q: it is account %q's drafts folder, which holds drafts waiting for the operator to send or discard, and they are the operator's to handle; %s", tool, drafts, acct.Name, outcome)
}

// refuseDraftsDestination returns the refusal for a move into the
// drafts folder, or nil for any other destination and on an account
// with no drafts folder.
func (s *Service) refuseDraftsDestination(ctx context.Context, acct ResolvedAccount, drafts, source, destination string) error {
	if drafts == "" || !namesFolder(destination, drafts) {
		return nil
	}
	s.logMoveRefusal(ctx, acct, "drafts_destination", source, destination)
	return fmt.Errorf("email_move cannot file mail into %q: it is account %q's drafts folder, which holds drafts waiting for the operator to send or discard, and a moved message there would look like one of them. Pick another destination, or report the need", destination, acct.Name)
}
