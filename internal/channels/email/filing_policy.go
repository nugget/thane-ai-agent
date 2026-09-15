package email

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// Filing policy: where email_move may put an account's mail. Go
// enforces one rule per account (mailbox.move_into) and never names a
// folder: the rule's entries are special-use roles or names the
// operator wrote. INBOX is always a legal destination out of a listed
// folder, so undoing a move or rescuing mail from junk needs no rule
// of its own.

// moveRequest is one email_move call's arguments, validated.
type moveRequest struct {
	opts MoveOptions

	// role is destination_role, when the call named its target that
	// way. Exactly one of role and opts.Destination is set.
	role FolderRole
}

// parseMoveRequest reads email_move's arguments and reports every
// problem with them together, so one retry can fix them all.
func parseMoveRequest(args map[string]any) (moveRequest, []string) {
	req := moveRequest{opts: MoveOptions{
		Folder:      toolargs.TrimmedString(args, "folder"),
		Destination: toolargs.TrimmedString(args, "destination"),
		Account:     toolargs.TrimmedString(args, "account"),
		UIDs:        toolargs.Uint32Slice(args, "uids"),
	}}
	if len(req.opts.UIDs) == 0 {
		if uid := toolargs.Uint32(args, "uid"); uid != 0 {
			req.opts.UIDs = []uint32{uid}
		}
	}

	// folder is always the source. A call that names only folder is
	// refused rather than read as a destination: guessing which folder
	// the model meant is how mail lands somewhere nobody chose.
	var problems []string
	if len(req.opts.UIDs) == 0 {
		problems = append(problems, "uids is required: pass uids (array of integers) or uid (single integer) from an email_list or email_search result in the same account and folder")
	}
	roleName := toolargs.TrimmedString(args, "destination_role")
	switch {
	case req.opts.Destination != "" && roleName != "":
		problems = append(problems, "pass destination or destination_role, not both: destination names a folder exactly and destination_role names its special-use role, and nothing was moved")
	case req.opts.Destination == "" && roleName == "":
		problems = append(problems, "destination or destination_role is required; folder is the source: pass destination as a folder name exactly as email_folders or the Email Accounts block lists it for this account, or destination_role as a special-use role such as junk or trash, and nothing was moved")
	case roleName != "":
		role, ok := parseFolderRole(roleName)
		if !ok {
			problems = append(problems, fmt.Sprintf("destination_role %q is not a special-use role; use one of %s, and nothing was moved", roleName, strings.Join(destinationRoleNames(), ", ")))
		}
		req.role = role
	}
	return req, problems
}

// resolveMoveDestination returns the folder a move files into: the
// destination as given, or destination_role resolved through r. The
// drafts role resolves the way the send path does, so the drafts
// refusal answers it. An unresolved role is refused with the gap it
// names.
func (s *Service) resolveMoveDestination(ctx context.Context, acct ResolvedAccount, r *folderResolver, req moveRequest) (string, error) {
	switch req.role {
	case "":
		return req.opts.Destination, nil
	case RoleDrafts:
		return s.draftsFolder(ctx, acct), nil
	}
	folder := r.folder(ctx, req.role)
	if r.err != nil {
		return "", r.err
	}
	if folder != "" {
		return folder, nil
	}
	s.logMoveRefusal(ctx, acct, "destination_role_unresolved", "", string(req.role))
	if key := roleConfigKey(req.role); key != "" {
		return "", fmt.Errorf("email_move cannot resolve destination_role %q on account %q: no folder there has the %s special-use role and the account sets no %s, so there is nowhere to move the mail until the operator configures %s; leave it where it is. Nothing was moved", req.role, acct.Name, req.role, key, key)
	}
	return "", fmt.Errorf("email_move cannot resolve destination_role %q on account %q: no folder there has the %s special-use role; pass destination with a folder name exactly as email_folders lists it, or leave the mail where it is. Nothing was moved", req.role, acct.Name, req.role)
}

// filingPolicy is an account's mailbox.move_into resolved to folder
// names for one call or one render.
type filingPolicy struct {
	// anywhere is true when move_into is ["*"]: every folder is allowed.
	anywhere bool

	// folders are the folders move_into resolves to, in the order
	// listed, without repeats.
	folders []string

	// unresolved are the roles move_into names that no folder on the
	// account is known to hold.
	unresolved []FolderRole
}

// resolveFilingPolicy resolves an account's move_into. resolve answers
// one role with a folder name, or "" when it cannot.
func resolveFilingPolicy(cfg AccountConfig, resolve func(FolderRole) string) filingPolicy {
	if cfg.MovesAnywhere() {
		return filingPolicy{anywhere: true}
	}
	var p filingPolicy
	for _, token := range cfg.MoveIntoTokens() {
		folder := token
		if name, isRole := strings.CutPrefix(token, platformconfig.EmailMoveIntoRolePrefix); isRole {
			role, ok := parseFolderRole(name)
			if !ok {
				// Config validation refuses an unknown role at load.
				continue
			}
			if folder = resolve(role); folder == "" {
				p.unresolved = append(p.unresolved, role)
				continue
			}
		}
		if !p.holds(folder) {
			p.folders = append(p.folders, folder)
		}
	}
	return p
}

// sameFolder compares folder names: exactly, except INBOX, which IMAP
// names without regard to case (RFC 3501 §5.1).
func sameFolder(a, b string) bool {
	if strings.EqualFold(a, DefaultFolder) && strings.EqualFold(b, DefaultFolder) {
		return true
	}
	return a == b
}

// namesFolder compares folder names without regard to case, for the
// checks that only refuse (the drafts folder, the junk guard). Some
// servers fold the case of every mailbox name, so a refusal keyed on
// the exact spelling would let a case variant reach the same folder;
// a refusal may be wider than the server, never narrower. The
// move_into allow check stays exact, because there exact fails closed.
func namesFolder(a, b string) bool {
	return strings.EqualFold(a, b)
}

// holds reports whether folder is one move_into resolves to.
func (p filingPolicy) holds(folder string) bool {
	return slices.ContainsFunc(p.folders, func(f string) bool { return sameFolder(f, folder) })
}

// allows reports whether mail may move from source into destination:
// destination is listed, or destination is INBOX and source is listed,
// which is how a move is undone or mail rescued from junk.
func (p filingPolicy) allows(source, destination string) bool {
	if p.anywhere || p.holds(destination) {
		return true
	}
	return sameFolder(destination, DefaultFolder) && p.holds(source)
}

// describe renders the resolved list for a refusal: each folder quoted,
// and each role no folder holds named as such.
func (p filingPolicy) describe() string {
	parts := make([]string, 0, len(p.folders)+len(p.unresolved))
	for _, f := range p.folders {
		parts = append(parts, strconv.Quote(f))
	}
	for _, r := range p.unresolved {
		parts = append(parts, "the "+string(r)+" role, which no folder here has")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// entryNames renders the resolved list for the Email Accounts entry:
// the folder names, then role:<role> for each role no folder is known
// to hold yet, so the entry never reads as allowing nothing.
func (p filingPolicy) entryNames() []string {
	names := slices.Clone(p.folders)
	for _, r := range p.unresolved {
		names = append(names, platformconfig.EmailMoveIntoRolePrefix+string(r))
	}
	return names
}

// refuseFiling returns the refusal for a move p does not allow, or nil.
// On an operator mailbox the refusal says what INBOX is to the
// operator, because that is why the move cannot happen, and the next
// move (flag it) follows from it.
func (s *Service) refuseFiling(ctx context.Context, acct ResolvedAccount, p filingPolicy, source, destination string) error {
	if p.allows(source, destination) {
		return nil
	}
	if sameFolder(destination, DefaultFolder) && !sameFolder(source, DefaultFolder) {
		// INBOX is refused only because the source is not listed, so
		// the refusal names the source, not INBOX.
		s.logMoveRefusal(ctx, acct, "inbox_return_outside_move_into", source, destination)
		return fmt.Errorf("email_move cannot return mail to INBOX from %q on account %q: mail goes back to INBOX only out of a folder its mailbox.move_into lists, %s, and %q is not one of them. Leave the mail where it is, and if the operator asked for the move, tell them it is theirs to make; nothing was moved", source, acct.Name, p.describe(), source)
	}
	s.logMoveRefusal(ctx, acct, "outside_move_into", source, destination)
	if acct.Config.OperatorMailbox() {
		return fmt.Errorf("email_move cannot file into %q on account %q: it is the operator's own mailbox, where INBOX is their worklist and the server keeps its own filing tree; mail may move only into %s, and back to INBOX from there. Flag it instead; nothing was moved", destination, acct.Name, p.describe())
	}
	return fmt.Errorf("email_move cannot file into %q on account %q: its mailbox.move_into allows only %s, and back to INBOX from there. Choose one of those or report the need; nothing was moved", destination, acct.Name, p.describe())
}

// refuseDraftsSource returns the refusal for a tool acting on mail in
// the account's drafts folder, or nil for any other folder. The drafts
// folder holds what waits for the operator to send or discard, their
// own drafts and Thane's alike, so neither moving mail out of it nor
// changing its flags is Thane's to do. outcome finishes the sentence.
func (s *Service) refuseDraftsSource(ctx context.Context, tool string, acct ResolvedAccount, folder, outcome string) error {
	drafts := s.draftsFolder(ctx, acct)
	if !namesFolder(normalizeFolder(folder), drafts) {
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

// logMoveRefusal records one refused move, keyed to the loop and
// conversation that asked.
func (s *Service) logMoveRefusal(ctx context.Context, acct ResolvedAccount, reason, source, destination string) {
	s.logger.Info("email move refused",
		"account", acct.Name,
		"reason", reason,
		"source_folder", source,
		"destination", destination,
		"owner", acct.Config.MailboxOwner(),
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
}
