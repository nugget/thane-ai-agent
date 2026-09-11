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
		return "", err
	}
	t.service.recordOp("email_list", acct.Name, listed.Folder, fmt.Sprintf("%d of %d", len(listed.Envelopes), listed.TotalMatched))
	return marshalResponse(newListResponse(acct.Name, listed, time.Now()))
}

// HandleRead reads a single message by UID.
func (t *Tools) HandleRead(ctx context.Context, args map[string]any) (string, error) {
	uid, ok := toolargs.Uint32OK(args, "uid")
	if !ok || uid == 0 {
		return "", fmt.Errorf("uid is required (integer > 0): the UID of the message from an email_list or email_search result in the same account and folder")
	}
	folder := normalizeFolder(toolargs.TrimmedString(args, "folder"))
	markSeen := toolargs.BoolOr(args, "mark_seen", true)

	acct, err := t.service.ResolveAccount(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}

	msg, err := acct.Client.ReadMessage(ctx, ReadOptions{Folder: folder, UID: uid, Peek: !markSeen})
	if err != nil {
		return "", err
	}
	t.service.recordOp("email_read", acct.Name, folder, strconv.FormatUint(uint64(uid), 10))
	return renderRead(newReadResponse(acct.Name, folder, msg, markSeen, time.Now()), msg)
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
	t.service.recordOp("email_folders", acct.Name, "", strconv.Itoa(len(folders))+" folders")
	return marshalResponse(foldersResponse{Account: acct.Name, Count: len(folders), Folders: folders})
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
		return "", err
	}
	t.service.recordOp("email_search", acct.Name, found.Folder, fmt.Sprintf("%d matched", found.TotalMatched))
	return marshalResponse(newListResponse(acct.Name, found, now))
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

	result, err := acct.Client.MarkMessages(ctx, action)
	if err != nil {
		return "", err
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

// HandleMove moves messages between folders of one account.
func (t *Tools) HandleMove(ctx context.Context, args map[string]any) (string, error) {
	opts := MoveOptions{
		Folder:      toolargs.TrimmedString(args, "folder"),
		Destination: toolargs.TrimmedString(args, "destination"),
		Account:     toolargs.TrimmedString(args, "account"),
	}

	// Models sometimes pass "folder" meaning the destination (e.g.,
	// "Trash") without providing "destination". When the destination
	// key is truly absent and folder is set, promote folder to
	// destination and let the source default to INBOX. An explicitly
	// empty destination still triggers the required-field error.
	_, hasDestination := args["destination"]
	if !hasDestination && opts.Destination == "" && opts.Folder != "" {
		opts.Destination = opts.Folder
		opts.Folder = ""
	}

	opts.UIDs = toolargs.Uint32Slice(args, "uids")
	if len(opts.UIDs) == 0 {
		if uid := toolargs.Uint32(args, "uid"); uid != 0 {
			opts.UIDs = []uint32{uid}
		}
	}

	var problems []string
	if len(opts.UIDs) == 0 {
		problems = append(problems, "uids is required: pass uids (array of integers) or uid (single integer) from an email_list or email_search result in the same account and folder")
	}
	if opts.Destination == "" {
		problems = append(problems, "destination is required: an existing folder in the same account (see email_folders)")
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	acct, err := t.service.ResolveAccount(ctx, opts.Account)
	if err != nil {
		return "", err
	}

	result, err := acct.Client.MoveMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	resp := moveResponse{
		Action:               "moved",
		Account:              acct.Name,
		SourceFolder:         result.SourceFolder,
		DestinationFolder:    result.Destination,
		UIDs:                 nonNilUIDs(result.UIDs),
		DestinationUIDs:      nonNilUIDs(result.DestUIDs),
		DestinationUIDsKnown: result.DestUIDsKnown,
	}
	if !result.DestUIDsKnown {
		resp.Note = "the server did not report the messages' new UIDs; list " + result.Destination + " to find them"
	}
	t.service.recordOp("email_move", acct.Name, result.SourceFolder, fmt.Sprintf("%d to %s", len(result.UIDs), result.Destination))
	return marshalResponse(resp)
}
