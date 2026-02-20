package email

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Tools holds email tool dependencies. Each handler takes the raw
// argument map from the tool registry and returns formatted text for
// the LLM.
type Tools struct {
	manager *Manager
}

// NewTools creates email tools backed by the given manager.
func NewTools(mgr *Manager) *Tools {
	return &Tools{manager: mgr}
}

// HandleList lists recent emails in a folder.
func (t *Tools) HandleList(ctx context.Context, args map[string]any) (string, error) {
	opts := ListOptions{
		Folder:  stringArg(args, "folder"),
		Limit:   intArg(args, "limit"),
		Unseen:  boolArg(args, "unseen"),
		Account: stringArg(args, "account"),
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	envelopes, err := client.ListMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if len(envelopes) == 0 {
		folder := opts.Folder
		if folder == "" {
			folder = "INBOX"
		}
		return fmt.Sprintf("No messages in %s", folder), nil
	}

	return formatEnvelopeList(envelopes), nil
}

// HandleRead reads a single email by UID.
func (t *Tools) HandleRead(ctx context.Context, args map[string]any) (string, error) {
	uid := uint32(intArg(args, "uid"))
	folder := stringArg(args, "folder")
	account := stringArg(args, "account")

	if uid == 0 {
		return "", fmt.Errorf("uid is required")
	}

	client, err := t.manager.Account(account)
	if err != nil {
		return "", err
	}

	msg, err := client.ReadMessage(ctx, folder, uid)
	if err != nil {
		return "", err
	}

	return formatMessage(msg), nil
}

// HandleFolders lists all folders with message counts.
func (t *Tools) HandleFolders(ctx context.Context, args map[string]any) (string, error) {
	account := stringArg(args, "account")

	client, err := t.manager.Account(account)
	if err != nil {
		return "", err
	}

	folders, err := client.ListFolders(ctx)
	if err != nil {
		return "", err
	}

	if len(folders) == 0 {
		return "No folders found", nil
	}

	return formatFolderList(folders), nil
}

// HandleSearch searches for emails matching the given criteria.
func (t *Tools) HandleSearch(ctx context.Context, args map[string]any) (string, error) {
	opts := SearchOptions{
		Folder:  stringArg(args, "folder"),
		Query:   stringArg(args, "query"),
		From:    stringArg(args, "from"),
		Limit:   intArg(args, "limit"),
		Account: stringArg(args, "account"),
	}

	if s := stringArg(args, "since"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			opts.Since = t
		}
	}
	if s := stringArg(args, "before"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			opts.Before = t
		}
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	envelopes, err := client.SearchMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if len(envelopes) == 0 {
		return "No messages match the search criteria", nil
	}

	return formatEnvelopeList(envelopes), nil
}

// HandleMark modifies flags on specified messages.
func (t *Tools) HandleMark(ctx context.Context, args map[string]any) (string, error) {
	action := MarkAction{
		Folder:  stringArg(args, "folder"),
		Flag:    stringArg(args, "flag"),
		Add:     boolArg(args, "add"),
		Account: stringArg(args, "account"),
	}

	// Parse UIDs from array or single value.
	switch v := args["uids"].(type) {
	case []any:
		for _, u := range v {
			if n, ok := u.(float64); ok {
				action.UIDs = append(action.UIDs, uint32(n))
			}
		}
	case float64:
		action.UIDs = append(action.UIDs, uint32(v))
	}

	if len(action.UIDs) == 0 {
		return "", fmt.Errorf("uids is required")
	}
	if action.Flag == "" {
		return "", fmt.Errorf("flag is required (seen, flagged, answered)")
	}

	client, err := t.manager.Account(action.Account)
	if err != nil {
		return "", err
	}

	if err := client.MarkMessages(ctx, action); err != nil {
		return "", err
	}

	verb := "Added"
	if !action.Add {
		verb = "Removed"
	}
	return fmt.Sprintf("%s %q flag on %d message(s)", verb, action.Flag, len(action.UIDs)), nil
}

// --- Formatting helpers ---

func formatEnvelopeList(envelopes []Envelope) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d message(s):\n\n", len(envelopes)))

	for _, env := range envelopes {
		sb.WriteString(fmt.Sprintf("UID: %d\n", env.UID))
		sb.WriteString(fmt.Sprintf("From: %s\n", env.From))
		sb.WriteString(fmt.Sprintf("Subject: %s\n", env.Subject))
		sb.WriteString(fmt.Sprintf("Date: %s\n", env.Date.Format("2006-01-02 15:04")))

		if len(env.Flags) > 0 {
			sb.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(env.Flags, ", ")))
		}
		sb.WriteString(fmt.Sprintf("Size: %d bytes\n", env.Size))
		sb.WriteString("\n")
	}

	return sb.String()
}

func formatMessage(msg *Message) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("From: %s\n", msg.From))
	sb.WriteString(fmt.Sprintf("To: %s\n", strings.Join(msg.To, ", ")))
	sb.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))
	sb.WriteString(fmt.Sprintf("Date: %s\n", msg.Date.Format("2006-01-02 15:04 MST")))
	if len(msg.Flags) > 0 {
		sb.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(msg.Flags, ", ")))
	}
	sb.WriteString(fmt.Sprintf("UID: %d | Size: %d bytes\n", msg.UID, msg.Size))
	sb.WriteString("\n---\n\n")

	if msg.TextBody != "" {
		sb.WriteString(msg.TextBody)
	} else if msg.HTMLBody != "" {
		sb.WriteString("[HTML content — no plain text version available]\n\n")
		sb.WriteString(msg.HTMLBody)
	} else {
		sb.WriteString("[No text content available]")
	}

	return sb.String()
}

func formatFolderList(folders []Folder) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d folder(s):\n\n", len(folders)))

	for _, f := range folders {
		sb.WriteString(fmt.Sprintf("%-30s  %d messages", f.Name, f.Messages))
		if f.Unseen > 0 {
			sb.WriteString(fmt.Sprintf(" (%d unseen)", f.Unseen))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// --- Argument extraction helpers ---

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}

func boolArg(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}
