package config

import (
	"slices"
	"strings"
	"testing"
)

// TestEmailMoveIntoDefaults pins the effective move_into per owner:
// an operator mailbox files only into its junk role unless the operator
// said otherwise, every other mailbox files anywhere, and ApplyDefaults
// writes the effective list back.
func TestEmailMoveIntoDefaults(t *testing.T) {
	tests := []struct {
		name         string
		owner        string
		moveInto     []string
		want         []string
		wantAnywhere bool
	}{
		{"assistant default is every folder", "", nil, []string{"*"}, true},
		{"operator default is the junk role", EmailMailboxOwnerOperator, nil, []string{"role:junk"}, false},
		{"operator explicit list", EmailMailboxOwnerOperator, []string{"role:junk", " Receipts "}, []string{"role:junk", "Receipts"}, false},
		{"operator may allow every folder", EmailMailboxOwnerOperator, []string{"*"}, []string{"*"}, true},
		{"assistant limited", EmailMailboxOwnerAssistant, []string{"Receipts"}, []string{"Receipts"}, false},
		{"empty list takes the owner default", EmailMailboxOwnerOperator, []string{}, []string{"role:junk"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := EmailAccountConfig{Name: "a", Mailbox: EmailMailboxConfig{Owner: tt.owner, MoveInto: tt.moveInto}}
			if got := acct.MoveIntoTokens(); !slices.Equal(got, tt.want) {
				t.Errorf("MoveIntoTokens() = %q, want %q", got, tt.want)
			}
			if got := acct.MovesAnywhere(); got != tt.wantAnywhere {
				t.Errorf("MovesAnywhere() = %v, want %v", got, tt.wantAnywhere)
			}
			cfg := EmailConfig{Accounts: []EmailAccountConfig{acct}}
			cfg.Accounts[0].IMAP = EmailIMAPConfig{Host: "imap.example.org", Username: "alice@example.org"}
			cfg.ApplyDefaults()
			if got := cfg.Accounts[0].Mailbox.MoveInto; !slices.Equal(got, tt.want) {
				t.Errorf("ApplyDefaults mailbox.move_into = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEmailFilingValidate pins every refusal of a move_into entry and
// the filing note's byte budget, each naming the account and the entry.
func TestEmailFilingValidate(t *testing.T) {
	imap := EmailIMAPConfig{Host: "imap.example.org", Port: 993, Username: "alice@example.org"}
	tests := []struct {
		name     string
		drafts   string
		moveInto []string
		note     string
		wantErr  string
	}{
		{"role token", "", []string{"role:junk"}, "", ""},
		{"every destination role", "", []string{"role:inbox", "role:sent", "role:trash", "role:junk", "role:archive", "role:all", "role:flagged", "role:important"}, "", ""},
		{"name token", "", []string{"Receipts"}, "", ""},
		{"role and name", "", []string{"role:junk", "Receipts"}, "", ""},
		{"star alone", "", []string{"*"}, "", ""},
		{"star combined", "", []string{"*", "role:junk"}, "", `mailbox.move_into[0] is "*", which allows every folder and cannot be combined`},
		{"empty entry", "", []string{"role:junk", "  "}, "", "mailbox.move_into[1] is empty"},
		{"unknown role", "", []string{"role:spam"}, "", `mailbox.move_into[0] "role:spam" names no special-use role; use role: with one of inbox, sent, trash, junk, archive, all, flagged, important`},
		{"roles are lower case", "", []string{"role:Junk"}, "", `"role:Junk" names no special-use role`},
		{"bare role prefix", "", []string{"role:"}, "", `"role:" names no special-use role`},
		{"drafts role", "", []string{"role:drafts"}, "", `mailbox.move_into[0] "role:drafts" names the drafts folder, which is never a move destination`},
		{"drafts folder by name", "Operator Drafts", []string{"Operator Drafts"}, "", `"Operator Drafts" names the drafts folder`},
		{"drafts folder by name in another case", "Operator Drafts", []string{"role:junk", "operator drafts"}, "", `mailbox.move_into[1] "operator drafts" names the drafts folder`},
		{"a name that only shares a prefix with drafts", "Operator Drafts", []string{"Operator Drafts Old"}, "", ""},
		{"note at the limit", "", nil, strings.Repeat("a", maxEmailFilingNoteBytes), ""},
		{"note over the limit", "", nil, strings.Repeat("a", maxEmailFilingNoteBytes+1), "mailbox.filing_note is 301 bytes, over the 300-byte limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := EmailConfig{Accounts: []EmailAccountConfig{{
				Name: "personal", IMAP: imap, DraftsFolder: tt.drafts,
				Mailbox: EmailMailboxConfig{Owner: EmailMailboxOwnerOperator, MoveInto: tt.moveInto, FilingNote: tt.note},
			}}}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "email.accounts[0] (personal)") {
				t.Fatalf("Validate() = %v, want an error naming the account and containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestEmailFolderRolesIsACopy pins that callers cannot change the
// vocabulary validation reads.
func TestEmailFolderRolesIsACopy(t *testing.T) {
	roles := EmailFolderRoles()
	roles[0] = "changed"
	if EmailFolderRoles()[0] != "inbox" {
		t.Fatal("EmailFolderRoles must return a copy")
	}
}
