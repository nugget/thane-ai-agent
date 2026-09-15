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
		{"only obvious spam moves, to the junk role", "destination: <the folder listed with role junk in that account's folders>"},
		{"destination always explicit", "Always pass destination; folder is the source."},
		{"matched senders never go to junk", "Never move mail from a matched sender into junk"},
		{"reads leave mail unread", "always pass mark_seen: false"},
		{"flag what needs the operator", `flag: "flagged"`},
		{"no reply or draft yet", "Do not reply or draft here"},
		{"undo names INBOX", `destination: "INBOX"`},
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
