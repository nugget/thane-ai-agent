package tools

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/email"
)

// SetEmailTools adds email tools to the registry.
func (r *Registry) SetEmailTools(et *email.Tools) {
	r.emailTools = et
	r.registerEmailTools()
}

func (r *Registry) registerEmailTools() {
	if r.emailTools == nil {
		return
	}

	r.Register(&Tool{
		Name:        "email_list",
		Description: "List recent emails from a mailbox folder. Returns sender, subject, date, and flags for each message. Use the UID from results with email_read to view full content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"folder": map[string]any{
					"type":        "string",
					"description": "Mailbox folder to list (default: INBOX)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of messages to return (default: 20)",
				},
				"unseen": map[string]any{
					"type":        "boolean",
					"description": "Only show unread messages (default: false)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleList(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_read",
		Description: "Read a single email by its UID. Returns full headers and body content. Get UIDs from email_list or email_search results.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uid": map[string]any{
					"type":        "integer",
					"description": "Message UID to read",
				},
				"folder": map[string]any{
					"type":        "string",
					"description": "Mailbox folder containing the message (default: INBOX)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
			"required": []string{"uid"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleRead(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_folders",
		Description: "List all email folders (mailboxes) with message counts and unseen counts.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleFolders(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_search",
		Description: "Search emails by text content, sender, or date range. Returns matching messages newest-first.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Text to search for in message content and headers",
				},
				"from": map[string]any{
					"type":        "string",
					"description": "Filter by sender address or name",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "Messages on or after this date (YYYY-MM-DD)",
				},
				"before": map[string]any{
					"type":        "string",
					"description": "Messages before this date (YYYY-MM-DD)",
				},
				"folder": map[string]any{
					"type":        "string",
					"description": "Mailbox folder to search (default: INBOX)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results (default: 20)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleSearch(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_mark",
		Description: "Add or remove flags on email messages. Supports marking as read/unread, flagged/unflagged, or answered.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "Message UIDs to modify",
				},
				"flag": map[string]any{
					"type":        "string",
					"enum":        []string{"seen", "flagged", "answered"},
					"description": "Flag to add or remove",
				},
				"add": map[string]any{
					"type":        "boolean",
					"description": "True to add the flag, false to remove it (default: true)",
				},
				"folder": map[string]any{
					"type":        "string",
					"description": "Mailbox folder containing the messages (default: INBOX)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
			"required": []string{"uids", "flag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleMark(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_send",
		Description: "Compose and send a new email. The body is written in markdown and automatically converted to both plain text and HTML. All recipients must have a contact record with appropriate trust zone.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Recipient email addresses",
				},
				"cc": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "CC email addresses",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Email subject line",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Email body in markdown format. Will be converted to text/plain and text/html automatically.",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
			"required": []string{"to", "subject", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleSend(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_reply",
		Description: "Reply to an existing email. Preserves threading headers (In-Reply-To, References) for proper conversation threading. The body is written in markdown. Use reply_all to include all original recipients.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uid": map[string]any{
					"type":        "integer",
					"description": "UID of the message to reply to (from email_list or email_read)",
				},
				"folder": map[string]any{
					"type":        "string",
					"description": "Folder containing the original message (default: INBOX)",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Reply body in markdown format",
				},
				"reply_all": map[string]any{
					"type":        "boolean",
					"description": "Reply to all original recipients (default: false)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
			"required": []string{"uid", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleReply(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "email_move",
		Description: "Move email messages between folders. Useful for archiving messages after reading or replying.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "Message UIDs to move",
				},
				"folder": map[string]any{
					"type":        "string",
					"description": "Source folder (default: INBOX)",
				},
				"destination": map[string]any{
					"type":        "string",
					"description": "Destination folder (e.g., Archive, Trash)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Email account name (default: primary account)",
				},
			},
			"required": []string{"uids", "destination"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.emailTools.HandleMove(ctx, args)
		},
	})
}
