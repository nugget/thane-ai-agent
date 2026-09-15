package app

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// siteFolderName matches a folder name some server happens to use, as
// opposed to a role. Model-facing text names roles (junk, trash) and
// sends the model to the folder listing for the name.
var siteFolderName = regexp.MustCompile(`\b(Archive|Trash|Junk|Spam)\b|\[Gmail\]|All Mail`)

func emailDefaultHandlerSpec(t *testing.T) looppkg.Spec {
	t.Helper()
	cfg := &config.Config{Email: config.EmailConfig{Accounts: []config.EmailAccountConfig{{
		Name: "primary",
		IMAP: config.EmailIMAPConfig{Host: "imap.example.com", Username: "thane@example.com"},
	}}}}
	for _, spec := range builtInServiceDefinitionSpecs(cfg) {
		if spec.Name == email.DefaultHandlerLoopName {
			return spec
		}
	}
	t.Fatalf("no %s spec when email is configured", email.DefaultHandlerLoopName)
	return looppkg.Spec{}
}

// TestEmailDefaultHandlerTaskBranchesOnMailboxOwner pins the operator
// branch of the built-in handler: it is keyed on the Email Accounts
// entry's owner, never on the description, and it carries each rule an
// operator mailbox needs. Every other account keeps the triage-and-reply
// procedure, with a reply no longer promised where policy can refuse it.
func TestEmailDefaultHandlerTaskBranchesOnMailboxOwner(t *testing.T) {
	task := emailDefaultHandlerSpec(t).Task
	tests := []struct {
		name string
		want string
	}{
		{"branch keyed on owner", `If that account's entry shows owner: "operator"`},
		{"operator section", "### The operator's own mailboxes"},
		{"leave mail in INBOX", "Leave the message in INBOX."},
		{"only obvious spam moves, by the junk role", `uids: [uid], destination_role: "junk"}`},
		{"folder is the source", "and folder is the source."},
		{"no junk folder leaves the message", "If email_move answers that no folder has the junk role, leave the message where it is."},
		{"matched senders never go to junk", "Never move mail from a matched sender into junk"},
		{"guard refusals are taught", "lists each refused message under refused[] with its from, trust_zone, reason, and recovery"},
		{"a refused message is not retried", "A refused message stays in INBOX: do not retry the move"},
		{"a refused message reaches the operator", "bring it to them with request_core_attention if it cannot wait"},
		{"filing is enforced by move_into", "email_move refuses every folder outside the entry's move_into"},
		{"reads leave mail unread", "always pass mark_seen: false"},
		{"flag what needs the operator", `flag: "flagged"`},
		{"no reply or draft yet", "Do not reply or draft here"},
		{"undo reads junk_folder from the entry", "folder: <junk_folder from the account's entry>"},
		{"undo returns by the inbox role", `destination_role: "inbox"}`},
		{"undo without destination uids searches by message_id", "message_id: <its message_id from the move result's moved[]>"},
		{"other accounts may file by role", "or a destination_role such as junk or trash for Go to resolve"},
		{"other accounts learn the guard", "A move into junk in a wake refuses mail from the operator's own record"},
		{"the guard refuses what it cannot check", "or from a sender the directory could not look up"},
		{"the guard refuses shared addresses", "from an address several contacts share when one of them is or may be at such a zone"},
		{"other accounts section", "### Every other account"},
		{"step 5 says policy can refuse", "The policy can also refuse it"},
		{"step 5 forbids another account", "do not write the message from another account"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(task, tt.want) {
				t.Errorf("handler Task must contain %q", tt.want)
			}
		})
	}
	if strings.Contains(task, "description begins") {
		t.Error("the operator branch must be keyed on owner, not on the account description")
	}
	for _, stale := range []string{"the folder listed with role junk", "call email_folders {account} once", `destination: "INBOX"`} {
		if strings.Contains(task, stale) {
			t.Errorf("the operator branch files by destination_role now and must not teach %q", stale)
		}
	}
	if strings.Contains(task, "held in its Drafts folder for the operator to send and the result says disposition: drafted;") {
		t.Error("step 5 must not promise a draft wherever policy can refuse one")
	}
}

// TestEmailDefaultHandlerSpecFieldsUnchanged pins everything in the
// built-in spec except the task: the operator branch changed what the
// handler is told, not how it is routed or what it wears.
func TestEmailDefaultHandlerSpecFieldsUnchanged(t *testing.T) {
	spec := emailDefaultHandlerSpec(t)
	if spec.Operation != looppkg.OperationEventDriven || spec.Completion != looppkg.CompletionNone || spec.ParentName != pollersContainerName || !spec.Enabled {
		t.Errorf("spec shape = operation %q completion %q parent %q enabled %v", spec.Operation, spec.Completion, spec.ParentName, spec.Enabled)
	}
	if !slices.Equal(spec.Tags, []string{"email"}) {
		t.Errorf("tags = %v, want [email]", spec.Tags)
	}
	p := spec.Profile
	if p.Mission != "email_triage" || p.LocalOnly != "false" || p.QualityFloor != 5 || p.DelegationGating != "disabled" || len(p.ExtraHints) != 1 || p.ExtraHints["source"] != "email_poll" {
		t.Errorf("profile = %+v", p)
	}
	if spec.Metadata["subsystem"] != "email" || spec.Metadata["category"] != "default_handler" || len(spec.Metadata) != 2 {
		t.Errorf("metadata = %v", spec.Metadata)
	}
}

// TestEmailDefaultHandlerTaskNamesNoSiteFolder guards the task against
// teaching a folder name as a destination: code never knows what a site
// calls its folders, so the task names roles and the listing.
func TestEmailDefaultHandlerTaskNamesNoSiteFolder(t *testing.T) {
	if m := siteFolderName.FindString(emailDefaultHandlerSpec(t).Task); m != "" {
		t.Errorf("handler Task names the site folder %q; name the role and send the model to the folder listing instead", m)
	}
}
