package email

import (
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// emailAccountDescription is the model-facing text for every email
// tool's account parameter. It states the binding rule rather than a
// bare primary-account default: a bound loop reads both this and its
// spec, and of the two it is likelier to act on the one attached to
// the tool it is about to call.
const emailAccountDescription = "Email account name (from email.accounts). Omit to use this loop's bound account, or the primary account when unbound; naming a different account than the one you are bound to is refused."

// Name implements [tools.Provider].
func (t *Tools) Name() string { return "email" }

// Tools implements [tools.Provider]. Email owns these declarations so
// its schemas, account policy, and handlers evolve as one model-facing
// contract.
func (t *Tools) Tools() []*tools.Tool {
	if t == nil {
		return nil
	}
	return t.toolDefinitions()
}

func accountParameter() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": emailAccountDescription,
	}
}

func folderParameter(role string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": role + " Folder names are exact and per account; take them from email_folders. Default: INBOX.",
	}
}

// draftParameter is the shared draft argument of email_send and
// email_reply.
func draftParameter() map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": "Hold the message in the account's Drafts folder for the operator to send instead of delivering it (default: false). The account's delivery policy may hold it there anyway; the result's disposition says which.",
	}
}

func uidsParameters() (map[string]any, map[string]any) {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "integer"},
		"description": "Message UIDs (integers) from an email_list or email_search result in the same account and folder. Required unless uid is provided.",
	}, map[string]any{
		"type":        "integer",
		"description": "A single message UID; convenience alternative to uids. At least one of the two must be set.",
	}
}

func (t *Tools) toolDefinitions() []*tools.Tool {
	uidsParam, uidParam := uidsParameters()
	return []*tools.Tool{
		{
			Name: "email_list",
			Description: "List messages in one folder of one account, newest first. Returns JSON " +
				"{account, folder, count, total_matched, truncated, messages:[{uid, from:{name,address}, to, cc, subject, date, message_id, flags, size}]}; " +
				"date is a delta such as -2h13m. An empty folder returns count 0 with an empty array. " +
				"UIDs are scoped to the account and folder they were listed from — pass both back to email_read, email_mark, and email_move. " +
				"limit defaults to 20 and caps at 100; total_matched says how many messages matched before the cap.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"folder": folderParameter("Folder to list."),
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum messages to return (integer). Default 20, maximum 100.",
					},
					"unseen": map[string]any{
						"type":        "boolean",
						"description": "Only messages not yet marked seen (default: false).",
					},
					"account": accountParameter(),
				},
			},
			Handler: t.HandleList,
		},
		{
			Name: "email_read",
			Description: "Read one message by UID. Returns a JSON header object " +
				"{account, folder, uid, message_id, in_reply_to, references, from, to, cc, reply_to, subject, date, flags, size, marked_seen, body_source, body_truncated, attachments:[{filename, content_type, size, inline}]} " +
				"followed by a line containing only --- and then the readable body: the text/plain part, or the HTML part rendered to text when body_source is \"html\". " +
				"Bodies over 32 KB are cut and body_truncated is true. Attachments are described, never downloaded. " +
				"Reading marks the message seen unless mark_seen is false. " +
				"The UID must be given with the account and folder it was listed from; a UID the folder does not hold is an error naming both.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uid": map[string]any{
						"type":        "integer",
						"description": "Message UID (integer) from an email_list or email_search result.",
					},
					"folder": folderParameter("Folder containing the message."),
					"mark_seen": map[string]any{
						"type":        "boolean",
						"description": "Mark the message seen as a side effect of reading (default: true). Pass false to read without changing its unseen state.",
					},
					"account": accountParameter(),
				},
				"required": []string{"uid"},
			},
			Handler: t.HandleRead,
		},
		{
			Name: "email_folders",
			Description: "List every folder (mailbox) in one account with its special-use role, whether it can hold messages, and its message and unseen counts. Returns JSON " +
				"{account, count, folders:[{name, role, selectable, delimiter, messages, unseen}]}; role is one of inbox, drafts, sent, trash, junk, archive, or empty. " +
				"Folder names are exact and per account: use them verbatim as email_move destinations. No email tool creates folders.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"account": accountParameter(),
				},
			},
			Handler: t.HandleFolders,
		},
		{
			Name: "email_search",
			Description: "Server-side search in one folder of one account. Every criterion is optional and they combine with AND: " +
				"query (text anywhere in the message), from, to, subject (header substrings), since and before (YYYY-MM-DD, RFC 3339, or a delta such as -7d; IMAP compares dates, not times), " +
				"unseen, flagged, and message_id or in_reply_to (an exact Message-ID without angle brackets — how to find an original message or an existing reply to it). " +
				"Returns the same JSON shape as email_list, newest first; limit defaults to 20 and caps at 100.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Text to match anywhere in the message (headers and body)."},
					"from":    map[string]any{"type": "string", "description": "Substring of the From header (name or address)."},
					"to":      map[string]any{"type": "string", "description": "Substring of the To header."},
					"subject": map[string]any{"type": "string", "description": "Substring of the Subject header."},
					"since": map[string]any{
						"type":        "string",
						"description": "Messages on or after this date: YYYY-MM-DD, RFC 3339, or a delta like -7d.",
					},
					"before": map[string]any{
						"type":        "string",
						"description": "Messages before this date: YYYY-MM-DD, RFC 3339, or a delta like -1d.",
					},
					"unseen":  map[string]any{"type": "boolean", "description": "Only messages not marked seen."},
					"flagged": map[string]any{"type": "boolean", "description": "Only flagged messages."},
					"message_id": map[string]any{
						"type":        "string",
						"description": "Exact Message-ID to find, without angle brackets.",
					},
					"in_reply_to": map[string]any{
						"type":        "string",
						"description": "Find replies to this Message-ID (without angle brackets).",
					},
					"folder": folderParameter("Folder to search."),
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results (integer). Default 20, maximum 100.",
					},
					"account": accountParameter(),
				},
			},
			Handler: t.HandleSearch,
		},
		{
			Name: "email_mark",
			Description: "Add or remove a flag on messages in one folder: seen, flagged, or answered. Provide uids (array of integers) or uid (single integer) " +
				"from an email_list or email_search result in the same account and folder; add defaults to true. Returns JSON " +
				"{action: flag_added|flag_removed, account, folder, flag, uids_affected, uids_not_found}. " +
				"A UID under uids_not_found no longer exists in that folder (moved or deleted) — list again rather than retrying.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uids": uidsParam,
					"uid":  uidParam,
					"flag": map[string]any{
						"type":        "string",
						"enum":        ValidFlagNames(),
						"description": "Flag to add or remove.",
					},
					"add": map[string]any{
						"type":        "boolean",
						"description": "true adds the flag, false removes it (default: true).",
					},
					"folder":  folderParameter("Folder containing the messages."),
					"account": accountParameter(),
				},
				"required": []string{"flag"},
			},
			Handler: t.HandleMark,
		},
		{
			Name: "email_send",
			Description: "Compose a new message from one account and hand it to the send decision. body is markdown and is rendered to both text and HTML. " +
				"Every recipient in to and cc must be in the contact directory at a trust zone whose send policy is not blocked, and the account's policy then decides the disposition: " +
				"sent (delivered by SMTP; cannot be recalled), drafted (held in the account's Drafts folder for the operator to send; nothing has left the mailbox), or refused. " +
				"The Email Accounts block lists, per account and for this turn, which zones it sends_directly_to, drafts_for, and refuses. " +
				"The configured bcc_owner audit copy is added automatically. Returns JSON " +
				"{disposition: sent|drafted, account, message_id, to, cc, bcc_count, subject, sent_folder_copy, drafts_folder, draft_uid, signed, recipients, note, decision}; " +
				"a refusal is one sentence followed by the decision JSON naming every recipient at issue and how to recover, and nothing is sent or drafted.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Recipient addresses, each \"Name <addr>\" or bare addr.",
					},
					"cc": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Cc addresses.",
					},
					"subject": map[string]any{"type": "string", "description": "Subject line."},
					"body": map[string]any{
						"type":        "string",
						"description": "Body in markdown; converted to text/plain and text/html. Supports temp:LABEL references.",
					},
					"draft":   draftParameter(),
					"account": accountParameter(),
				},
				"required": []string{"to", "subject", "body"},
			},
			ContentResolveExempt: []string{"to", "cc", "account"},
			Handler:              t.HandleSend,
		},
		{
			Name: "email_reply",
			Description: "Reply to a message by UID, preserving In-Reply-To and References so the reply threads in the recipient's client. " +
				"The reply goes to the original Reply-To (else From); reply_all adds the original To and Cc minus this account's own address. " +
				"Recipients pass through the same trust gate and send decision as email_send: any refused recipient refuses the whole reply, and the account's policy decides whether the reply is sent or held in Drafts for the operator. " +
				"body is markdown. Returns the same JSON shape as email_send with in_reply_to set.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uid": map[string]any{
						"type":        "integer",
						"description": "UID (integer) of the message being replied to, from an email_list or email_read result.",
					},
					"folder": folderParameter("Folder containing the original message."),
					"body": map[string]any{
						"type":        "string",
						"description": "Reply body in markdown. Supports temp:LABEL references.",
					},
					"reply_all": map[string]any{
						"type":        "boolean",
						"description": "Also reply to the original To and Cc recipients (default: false).",
					},
					"draft":   draftParameter(),
					"account": accountParameter(),
				},
				"required": []string{"uid", "body"},
			},
			ContentResolveExempt: []string{"account", "folder"},
			Handler:              t.HandleReply,
		},
		{
			Name: "email_move",
			Description: "Move messages to another folder in the same account. Provide uids (array of integers) or uid (single integer), and destination: " +
				"an existing folder name for this account from email_folders (no tool creates folders, and there are no cross-account moves). " +
				"If only folder is given without destination, folder is treated as the destination and INBOX as the source. Returns JSON " +
				"{action: moved, account, source_folder, destination_folder, uids, destination_uids, destination_uids_known}; " +
				"when destination_uids_known is false the server did not report the new UIDs and the destination must be listed to find them. " +
				"A destination the account lacks is refused with the account's real folder list.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"uids": uidsParam,
					"uid":  uidParam,
					"folder": map[string]any{
						"type":        "string",
						"description": "Source folder (default: INBOX). If destination is omitted, folder is treated as the destination instead.",
					},
					"destination": map[string]any{
						"type":        "string",
						"description": "Target folder, an existing name in the same account (for example Archive or Trash). Required unless folder is used as the destination alias.",
					},
					"account": accountParameter(),
				},
				// One of (uids|uid) and one of (destination|folder) are
				// validated by the handler; left out of required so
				// the standard JSON-schema validator does not reject
				// the folder-as-destination alias path.
			},
			ContentResolveExempt: []string{"folder", "destination", "account"},
			Handler:              t.HandleMove,
		},
	}
}

var _ tools.Provider = (*Tools)(nil)
