package email

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// Tools holds the email tool handlers. Each handler takes the raw
// argument map from the tool registry, resolves its account through
// [Service.ResolveAccount], and returns a JSON result for the model.
type Tools struct {
	service  *Service
	contacts ContactResolver
	logger   *slog.Logger
}

// newTools creates the tool set for a service. contacts may be nil, in
// which case outbound recipients are not trust-gated.
func newTools(service *Service, contacts ContactResolver, logger *slog.Logger) *Tools {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tools{service: service, contacts: contacts, logger: logger}
}

// HandleList lists recent messages in a folder.
func (t *Tools) HandleList(ctx context.Context, args map[string]any) (string, error) {
	opts := ListOptions{
		Folder: toolargs.TrimmedString(args, "folder"),
		Limit:  toolargs.IntOr(args, "limit", DefaultListLimit),
		Unseen: toolargs.Bool(args, "unseen"),
	}

	acct, err := t.service.ResolveAccount(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}

	listed, err := acct.Client.ListMessages(ctx, opts)
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	t.service.recordOp("email_list", acct.Name, listed.Folder, fmt.Sprintf("%d of %d", len(listed.Envelopes), listed.TotalMatched))
	return marshalListResponse(newListResponse(acct.Name, listed, newIdentityLookup(ctx, t.contacts, t.logger), time.Now()))
}

// HandleRead reads a single message by UID.
func (t *Tools) HandleRead(ctx context.Context, args map[string]any) (string, error) {
	uid, ok := toolargs.Uint32OK(args, "uid")
	if !ok || uid == 0 {
		return "", fmt.Errorf("uid is required (integer > 0): the UID of the message from an email_list or email_search result in the same account and folder")
	}
	folder := normalizeFolder(toolargs.TrimmedString(args, "folder"))

	acct, err := t.service.ResolveAccount(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}
	markSeen := readMarksSeen(args, acct.Config)
	if markSeen {
		// The operator-mailbox rule answers before the read-level
		// downgrade below, so an unattended mark_seen: true on an operator
		// mailbox is refused whatever the account's access.
		if err := t.service.refuseUnattendedSeen(ctx, "email_read", acct, "nothing was read, so retry with mark_seen: false, which leaves the message unread"); err != nil {
			return "", err
		}
	}
	var accessNote string
	if acct.Config.AccessLevel() == AccessRead && markSeen {
		// A read-level account is never mutated, not even by the seen
		// flag a read would ordinarily set.
		markSeen = false
		accessNote = "policy.access is read for this account, so the message was not marked seen"
	}

	msg, err := acct.Client.ReadMessage(ctx, ReadOptions{Folder: folder, UID: uid, Peek: !markSeen})
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	auth := t.service.authenticate(ctx, acct.Name, msg)
	t.service.recordOp("email_read", acct.Name, folder, strconv.FormatUint(uint64(uid), 10))
	header := newReadResponse(acct.Name, folder, msg, markSeen, auth, newIdentityLookup(ctx, t.contacts, t.logger), time.Now())
	header.AccessNote = accessNote
	return renderRead(header, msg)
}

// HandleFolders lists all folders with roles and counts.
func (t *Tools) HandleFolders(ctx context.Context, args map[string]any) (string, error) {
	acct, err := t.service.ResolveAccount(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}

	folders, err := t.service.listFolders(ctx, acct)
	if err != nil {
		return "", err
	}
	if folders == nil {
		folders = []Folder{}
	}
	shown, truncated := capFolders(folders)
	t.service.recordOp("email_folders", acct.Name, "", strconv.Itoa(len(folders))+" folders")
	return marshalResponse(foldersResponse{Account: acct.Name, Count: len(shown), Total: len(folders), Truncated: truncated, Folders: shown})
}

// maxFolderResults caps the folders one email_folders result lists. A
// label-heavy account can have thousands; the cache the Email Accounts
// block renders from keeps the full listing.
const maxFolderResults = 200

// capFolders returns at most maxFolderResults folders and whether any
// were dropped. When the list must be cut, role-bearing folders sort
// first so the folders a model files into survive, then by name.
func capFolders(folders []Folder) ([]Folder, bool) {
	if len(folders) <= maxFolderResults {
		return folders, false
	}
	sorted := slices.Clone(folders)
	slices.SortStableFunc(sorted, func(a, b Folder) int {
		aRole, bRole := a.Role != "", b.Role != ""
		if aRole != bRole {
			if aRole {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return sorted[:maxFolderResults], true
}

// refreshOnFolderMiss re-lists an account's folders after an operation
// failed because a folder does not exist, so the Email Accounts block
// the next turn reads carries the real names, and returns err
// unchanged. The listing is best-effort: the caller's error is the
// answer either way.
func (t *Tools) refreshOnFolderMiss(ctx context.Context, acct ResolvedAccount, err error) error {
	if FailureKindOf(err) != FailureFolderNotFound {
		return err
	}
	if _, listErr := t.service.listFolders(ctx, acct); listErr != nil {
		t.logger.Debug("email folder re-list after folder miss failed", "account", acct.Name, "error", listErr)
	}
	return err
}

// HandleSearch searches for messages matching the given criteria.
func (t *Tools) HandleSearch(ctx context.Context, args map[string]any) (string, error) {
	opts := SearchOptions{
		Folder:    toolargs.TrimmedString(args, "folder"),
		Query:     toolargs.TrimmedString(args, "query"),
		From:      toolargs.TrimmedString(args, "from"),
		To:        toolargs.TrimmedString(args, "to"),
		Subject:   toolargs.TrimmedString(args, "subject"),
		Unseen:    toolargs.Bool(args, "unseen"),
		Flagged:   toolargs.Bool(args, "flagged"),
		MessageID: toolargs.TrimmedString(args, "message_id"),
		InReplyTo: toolargs.TrimmedString(args, "in_reply_to"),
		Limit:     toolargs.IntOr(args, "limit", DefaultListLimit),
	}

	now := time.Now()
	var problems []string
	if s := toolargs.TrimmedString(args, "since"); s != "" {
		since, err := parseSearchDate(s, now)
		if err != nil {
			problems = append(problems, fmt.Sprintf("since must be YYYY-MM-DD, RFC 3339, or a delta like -7d (got %q)", s))
		}
		opts.Since = since
	}
	if s := toolargs.TrimmedString(args, "before"); s != "" {
		before, err := parseSearchDate(s, now)
		if err != nil {
			problems = append(problems, fmt.Sprintf("before must be YYYY-MM-DD, RFC 3339, or a delta like -1d (got %q)", s))
		}
		opts.Before = before
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	acct, err := t.service.ResolveAccount(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}

	found, err := acct.Client.SearchMessages(ctx, opts)
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	t.service.recordOp("email_search", acct.Name, found.Folder, fmt.Sprintf("%d matched", found.TotalMatched))
	return marshalListResponse(newListResponse(acct.Name, found, newIdentityLookup(ctx, t.contacts, t.logger), now))
}

// parseSearchDate accepts the shapes a model plausibly sends for a
// date bound: a bare YYYY-MM-DD, an RFC 3339 instant, or a delta such
// as "-7d" relative to now.
func parseSearchDate(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return promptfmt.ParseTimeOrDelta(s, now)
}

// HandleMark adds or removes a flag on messages.
func (t *Tools) HandleMark(ctx context.Context, args map[string]any) (string, error) {
	action := parseMarkAction(args)

	var problems []string
	if len(action.UIDs) == 0 {
		problems = append(problems, "uids is required: pass uids (array of integers) or uid (single integer) from an email_list or email_search result in the same account and folder")
	}
	if p := batchProblem("email_mark", len(action.UIDs), "nothing was changed"); p != "" {
		problems = append(problems, p)
	}
	if action.Flag == "" {
		problems = append(problems, fmt.Sprintf("flag is required (one of %s)", strings.Join(ValidFlagNames(), ", ")))
	} else if _, ok := ValidFlag(action.Flag); !ok {
		problems = append(problems, fmt.Sprintf("flag %q is not supported (one of %s)", action.Flag, strings.Join(ValidFlagNames(), ", ")))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	acct, err := t.service.ResolveAccount(ctx, action.Account)
	if err != nil {
		return "", err
	}
	if err := t.service.requireOrganize(acct, "email_mark"); err != nil {
		return "", err
	}
	drafts, err := t.service.protectedDrafts(ctx, "email_mark", acct, t.service.newFolderResolver(acct), "nothing was changed")
	if err != nil {
		return "", err
	}
	if err := t.service.refuseDraftsSource(ctx, "email_mark", acct, drafts, action.Folder, "nothing was changed"); err != nil {
		return "", err
	}
	if action.Add && action.Flag == "seen" {
		if err := t.service.refuseUnattendedSeen(ctx, "email_mark", acct, "nothing was changed, and a message that needs the operator gets flag \"flagged\" instead"); err != nil {
			return "", err
		}
	}

	result, err := acct.Client.MarkMessages(ctx, action)
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}

	verb := "flag_added"
	if !action.Add {
		verb = "flag_removed"
	}
	t.service.recordOp("email_mark", acct.Name, result.Folder, fmt.Sprintf("%s %s on %d", verb, action.Flag, len(result.Affected)))
	return marshalResponse(markResponse{
		Action:       verb,
		Account:      acct.Name,
		Folder:       result.Folder,
		Flag:         action.Flag,
		UIDsAffected: nonNilUIDs(result.Affected),
		UIDsNotFound: nonNilUIDs(missingUIDs(result.Requested, result.Affected)),
	})
}

// missingUIDs returns the requested UIDs the server did not report back.
func missingUIDs(requested, affected []uint32) []uint32 {
	var missing []uint32
	for _, uid := range requested {
		if !slices.Contains(affected, uid) {
			missing = append(missing, uid)
		}
	}
	return missing
}

// parseMarkAction translates the raw tool-argument map into a
// [MarkAction]. Extracted from [Tools.HandleMark] so unit tests can
// assert the args→action translation (especially the `add`
// default-true contract from #930) without standing up a Service.
//
// Default semantics:
//   - `add` omitted defaults to true, matching the schema's
//     "default: true" — marking seen after triage is the common case.
//   - `uids` accepts an array; `uid` is a single-message convenience.
//     If both are absent the returned action has UIDs == nil and the
//     caller (HandleMark) surfaces "uids is required" to the model.
func parseMarkAction(args map[string]any) MarkAction {
	action := MarkAction{
		Folder:  toolargs.TrimmedString(args, "folder"),
		Flag:    toolargs.TrimmedString(args, "flag"),
		Add:     toolargs.BoolOr(args, "add", true),
		Account: toolargs.TrimmedString(args, "account"),
	}
	action.UIDs = toolargs.Uint32Slice(args, "uids")
	if len(action.UIDs) == 0 {
		if uid := toolargs.Uint32(args, "uid"); uid != 0 {
			action.UIDs = []uint32{uid}
		}
	}
	return action
}

// HandleMove moves messages between folders of one account. What may
// move is decided before anything does: the destination and filing
// policy in filing_policy.go, the junk guard in junk_guard.go.
func (t *Tools) HandleMove(ctx context.Context, args map[string]any) (string, error) {
	req, problems := parseMoveRequest(args)
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	acct, err := t.service.ResolveAccount(ctx, req.opts.Account)
	if err != nil {
		return "", err
	}
	if err := t.service.requireOrganize(acct, "email_move"); err != nil {
		return "", err
	}
	resp, err := t.move(ctx, acct, req)
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	return marshalMoveResponse(resp)
}
