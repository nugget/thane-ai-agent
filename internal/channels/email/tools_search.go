package email

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// searchProperties is email_search's parameter schema, and the closed
// set of arguments its handler accepts: an argument the schema does not
// declare is refused rather than silently ignored, because a search that
// drops a criterion answers a different question than the one asked.
func (t *Tools) searchProperties() map[string]any {
	props := map[string]any{
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
		"unseen":    map[string]any{"type": "boolean", "description": "Only messages not marked seen."},
		"flagged":   map[string]any{"type": "boolean", "description": "Only messages carrying \\Flagged, whoever set it; a label's flag colour is a flag too."},
		"unflagged": map[string]any{"type": "boolean", "description": "Only messages without \\Flagged. Cannot be combined with flagged: true."},
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
	}
	if names := t.labels().searchable(); len(names) > 0 {
		props["label"] = map[string]any{
			"type":        "string",
			"enum":        names,
			"description": "Only messages carrying this label's keyword. The account's Email Accounts entry lists each label it carries, with its meaning and how it shows; a label the entry does not list is refused, and a label that shows only as a flag colour has no keyword and cannot be searched.",
		}
	}
	return props
}

// labels returns the declared label vocabulary.
func (t *Tools) labels() labelSet {
	if t == nil || t.service == nil || t.service.manager == nil {
		return nil
	}
	return t.service.manager.labels
}

// undeclaredArguments returns, sorted, the argument names args carries
// that props does not declare.
func undeclaredArguments(args map[string]any, props map[string]any) []string {
	var extra []string
	for name := range args {
		if _, ok := props[name]; !ok {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	return extra
}

// undeclaredRefusal refuses arguments a tool does not declare, naming
// every one and listing what the tool takes, so one retry can succeed.
func undeclaredRefusal(tool, outcome string, extra []string, props map[string]any) error {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)
	quoted := make([]string, len(extra))
	for i, name := range extra {
		quoted[i] = strconv.Quote(name)
	}
	noun := "argument"
	if len(extra) > 1 {
		noun = "arguments"
	}
	err := fmt.Errorf("%s does not take the %s %s; %s. It takes only %s: retry with those, dropping the rest", tool, noun, strings.Join(quoted, ", "), outcome, strings.Join(names, ", "))
	return toolargs.Rejected(err, extra...)
}

// searchLabelProblems resolves email_search's label and unflagged
// arguments into opts, and returns what is wrong with them.
func (t *Tools) searchLabelProblems(args map[string]any, opts *SearchOptions) []string {
	var problems []string
	if opts.Flagged && opts.Unflagged {
		problems = append(problems, "flagged and unflagged cannot both be true: pass one of them")
	}
	name := toolargs.TrimmedString(args, "label")
	if name == "" {
		return problems
	}
	if l, ok := t.labels().named(name); ok && l.Keyword != "" {
		opts.Keyword = l.Keyword
		return problems
	}
	return append(problems, fmt.Sprintf("label %q is not one email_search can find (one of %s)", name, strings.Join(t.labels().searchable(), ", ")))
}

// searchLabelCarried refuses a label search on an account whose mailbox
// does not carry the label, where a keyword search would answer a
// question about marks Thane never writes there.
func (t *Tools) searchLabelCarried(acct ResolvedAccount, args map[string]any) error {
	name := toolargs.TrimmedString(args, "label")
	if name == "" {
		return nil
	}
	if _, ok := t.service.manager.accountLabels(acct.Name).named(name); ok {
		return nil
	}
	return t.service.accountLabelRefusal("email_search", "search", "nothing was searched", acct.Name, name)
}
