package email

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestMoveIntoBindsOnlyUnattendedTurns pins the operator's decision to
// lift move_into in their own conversation: in an attended turn a move
// the rule does not allow goes ahead, the same move in a turn the
// operator is not present for is refused and nothing moves, and the
// drafts folder stays refused as a destination and as a source in an
// attended turn too. An operator mailbox at its default [role:junk] and
// an assistant mailbox with a limited move_into behave the same way.
func TestMoveIntoBindsOnlyUnattendedTurns(t *testing.T) {
	byRole := func(role string) map[string]any { return map[string]any{"destination_role": role} }
	byName := func(name string) map[string]any { return map[string]any{"destination": name} }
	limited := []string{"Receipts"}
	tests := []struct {
		name     string
		owner    string
		moveInto []string
		attended bool
		source   string
		target   map[string]any
		dest     string   // the folder the move names, resolved
		wantErr  []string // nil when the move goes ahead
	}{
		{"operator attended files into an unlisted folder", "operator", nil, true, "INBOX", byName("Receipts"), "Receipts", nil},
		{"operator attended files by an unlisted role", "operator", nil, true, "INBOX", byRole("archive"), "Archive", nil},
		{"operator unattended refuses the unlisted folder", "operator", nil, false, "INBOX", byName("Receipts"), "Receipts", []string{`cannot file into "Receipts"`, "in a turn the operator is not present for", "or in a channel conversation bound to their contact, where move_into does not apply", "is not their own turn and is refused the same way", "nothing was moved"}},
		{"operator unattended refuses the unlisted role", "operator", nil, false, "INBOX", byRole("archive"), "Archive", []string{`cannot file into "Archive"`, "nothing was moved"}},
		{"operator attended returns mail to INBOX from an unlisted folder", "operator", nil, true, "Receipts", byRole("inbox"), "INBOX", nil},
		{"operator unattended refuses that return", "operator", nil, false, "Receipts", byRole("inbox"), "INBOX", []string{`cannot return mail to INBOX from "Receipts"`, "nothing was moved"}},
		{"operator attended never files into the drafts folder by name", "operator", nil, true, "INBOX", byName("Drafts"), "Drafts", []string{`email_move cannot file mail into "Drafts"`, "drafts folder"}},
		{"operator attended never files by destination_role drafts", "operator", nil, true, "INBOX", byRole("drafts"), "Drafts", []string{`email_move cannot file mail into "Drafts"`, "drafts folder"}},
		{"operator attended never moves mail out of the drafts folder", "operator", nil, true, "Drafts", byName("Receipts"), "Receipts", []string{`email_move cannot act on mail in "Drafts"`, "nothing was moved"}},
		{"operator attended never returns drafts to INBOX", "operator", nil, true, "Drafts", byRole("inbox"), "INBOX", []string{`email_move cannot act on mail in "Drafts"`, "nothing was moved"}},
		{"limited attended files outside its list", "assistant", limited, true, "INBOX", byRole("trash"), "Trash", nil},
		{"limited unattended refuses outside its list", "assistant", limited, false, "INBOX", byRole("trash"), "Trash", []string{`its mailbox.move_into allows only ["Receipts"]`, "ask for it in their own message through Thane's native API or console", "nothing was moved"}},
		{"limited attended returns mail to INBOX from an unlisted folder", "assistant", limited, true, "Archive", byRole("inbox"), "INBOX", nil},
		{"limited unattended refuses that return", "assistant", limited, false, "Archive", byRole("inbox"), "INBOX", []string{`cannot return mail to INBOX from "Archive"`, "nothing was moved"}},
		{"limited attended never files into the drafts folder", "assistant", limited, true, "INBOX", byName("Drafts"), "Drafts", []string{`email_move cannot file mail into "Drafts"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), false, func(a *AccountConfig) {
				a.Mailbox.Owner = tt.owner
				a.Mailbox.MoveInto = tt.moveInto
			})
			uid := mem.append(tt.source, rawMessage("Bob <bob@example.org>", "alice@example.org", "filed", "x"))
			args := map[string]any{"uid": float64(uid), "folder": tt.source}
			maps.Copy(args, tt.target)
			ctx := context.Background()
			if tt.attended {
				ctx = attendedCtx()
			}

			out, err := svc.ToolProvider().HandleMove(ctx, args)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("move = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), tt.wantErr...)
				if got := subjectsIn(t, svc, tt.source); !slices.Equal(got, []string{"filed"}) {
					t.Errorf("%s after a refused move = %v; the message must stay", tt.source, got)
				}
				if got := subjectsIn(t, svc, tt.dest); slices.Contains(got, "filed") {
					t.Errorf("%s after a refused move = %v; nothing may arrive", tt.dest, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("HandleMove: %v", err)
			}
			resp := decodeMove(t, out)
			if resp.Action != "moved" || resp.DestinationFolder != tt.dest || len(resp.Moved) != 1 {
				t.Fatalf("move = %+v, want moved into %q", resp, tt.dest)
			}
			if got := subjectsIn(t, svc, tt.source); slices.Contains(got, "filed") {
				t.Errorf("%s after the move = %v; the message must have left", tt.source, got)
			}
			if got := subjectsIn(t, svc, tt.dest); !slices.Equal(got, []string{"filed"}) {
				t.Errorf("%s after the move = %v, want the moved message", tt.dest, got)
			}
		})
	}
}

// TestAttendedMoveOutsideMoveIntoIsLogged pins the forensic trail of
// the lifted rule: an attended move move_into would refuse logs one
// line once the server has moved the messages, naming them by UID and
// Message-ID and keyed to the request and tool call that asked; an
// attended move the rule allows logs no such line; an attended move to
// a folder the account lacks is refused in the operator's turn too,
// moves nothing, and logs no such line; and an unattended move outside
// the rule logs a refusal instead.
func TestAttendedMoveOutsideMoveIntoIsLogged(t *testing.T) {
	const allowed = "email move outside move_into allowed"
	const refused = "email move refused"
	turnKeys := []string{"request_id=r_move", "tool_call_id=call_move"}
	tests := []struct {
		name       string
		attended   bool
		target     map[string]any
		wantErr    []string // nil when the move goes ahead
		wantUIDs   bool     // the allowed line must name the moved UID
		want       []string
		wantAbsent []string
	}{
		{"attended outside move_into", true, map[string]any{"destination": "Receipts"}, nil, true,
			append([]string{allowed, "reason=operator_present", "account=primary", "source_folder=INBOX", "destination=Receipts", "owner=operator", "count=1", "destination_uids_known=true", "message_ids=[logged@example.com]"}, turnKeys...), []string{refused}},
		{"attended inside move_into", true, map[string]any{"destination_role": "junk"}, nil, false, nil, []string{allowed, refused}},
		{"attended to a folder the account lacks", true, map[string]any{"destination": "Reciepts"}, []string{`folder "Reciepts" does not exist in email account "primary"`}, false, nil, []string{allowed, refused}},
		{"unattended outside move_into", false, map[string]any{"destination": "Receipts"}, []string{`cannot file into "Receipts"`, "nothing was moved"}, false,
			append([]string{refused, "reason=outside_move_into"}, turnKeys...), []string{allowed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			svc, mem, _ := identityServiceWith(t, ServiceDependencies{Contacts: identityStub(), Logger: slog.New(slog.NewTextHandler(&buf, nil))}, func(cfg *Config) {
				cfg.Accounts[0].Mailbox.Owner = "operator"
			})
			mem.addFolder("Receipts")
			mem.addFolder(junkTestFolder, imap.MailboxAttrJunk)
			uid := mem.append("INBOX", rawMessage("Bob <bob@example.org>", "alice@example.org", "logged", "x"))
			args := map[string]any{"uid": float64(uid)}
			maps.Copy(args, tt.target)
			ctx := context.Background()
			if tt.attended {
				ctx = attendedCtx()
			}
			ctx = tools.WithToolCallID(tools.WithRequestID(ctx, "r_move"), "call_move")

			_, err := svc.ToolProvider().HandleMove(ctx, args)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatal("HandleMove succeeded, want a refusal")
				}
				mustContain(t, err.Error(), tt.wantErr...)
				if got := subjectsIn(t, svc, "INBOX"); !slices.Equal(got, []string{"logged"}) {
					t.Errorf("INBOX after a refused move = %v; the message must stay", got)
				}
			} else if err != nil {
				t.Fatalf("HandleMove: %v", err)
			}
			logged := buf.String()
			mustContain(t, logged, tt.want...)
			if tt.wantUIDs {
				// The leading space keeps destination_uids=[…] from
				// satisfying the source UID check.
				mustContain(t, logged, fmt.Sprintf(" uids=[%d]", uid))
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(logged, absent) {
					t.Errorf("log contains %q:\n%s", absent, logged)
				}
			}
		})
	}
}

// TestMoveIntoBindsNearMissTurns pins that the lift keys on attended and
// on nothing that merely resembles it. Each turn here carries some mark
// of the operator's own message (a message origin, the operator's
// channel binding, the API origin, the operator speaking through the
// Ollama-compatible shim) and is still one the operator is not present
// for, so on an operator mailbox at its default [role:junk] a move into
// the archive is refused and nothing moves.
func TestMoveIntoBindsNearMissTurns(t *testing.T) {
	bg := context.Background()
	owner := &memory.ChannelBinding{Channel: "signal", Address: "+1", IsOwner: true}
	other := &memory.ChannelBinding{Channel: "signal", Address: "+2", ContactID: "x", TrustZone: "household"}
	shim := &memory.ChannelBinding{Channel: "owu", Address: "ha"}
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{"owner-bound loop wake", tools.WithMessageOrigin(tools.WithChannelBinding(bg, owner), memory.OriginWake)},
		{"owner binding with no origin", tools.WithChannelBinding(bg, owner)},
		{"someone else's channel message", tools.WithMessageOrigin(tools.WithChannelBinding(bg, other), memory.OriginChannel)},
		{"API origin without the channel hint", tools.WithMessageOrigin(bg, memory.OriginAPI)},
		{"Ollama-compatible shim", tools.WithHints(tools.WithMessageOrigin(tools.WithChannelBinding(bg, shim), memory.OriginAPI), map[string]string{"channel": "ollama"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if attended(tt.ctx) {
				t.Fatal("the near-miss context reads as attended; the case would test nothing")
			}
			svc, mem := filingService(t, identityStub(), false, func(a *AccountConfig) { a.Mailbox.Owner = "operator" })
			uid := mem.append("INBOX", rawMessage("Bob <bob@example.org>", "alice@example.org", "filed", "x"))

			out, err := svc.ToolProvider().HandleMove(tt.ctx, map[string]any{"uid": float64(uid), "destination_role": "archive"})
			if err == nil {
				t.Fatalf("move = %s, want the move_into refusal", out)
			}
			mustContain(t, err.Error(), `cannot file into "Archive"`, "in a turn the operator is not present for", "nothing was moved")
			if got := subjectsIn(t, svc, "INBOX"); !slices.Equal(got, []string{"filed"}) {
				t.Errorf("INBOX after a refused move = %v; the message must stay", got)
			}
			if got := subjectsIn(t, svc, "Archive"); slices.Contains(got, "filed") {
				t.Errorf("Archive after a refused move = %v; nothing may arrive", got)
			}
		})
	}
}

// TestMoveDescriptionTeachesWhenMoveIntoBinds pins the email_move
// description's split: move_into binds a turn the operator is not
// present for, does not apply in their own turn, and the protections
// that are not filing policy hold in every turn. The old opening, which
// read as binding every turn, stays gone.
func TestMoveDescriptionTeachesWhenMoveIntoBinds(t *testing.T) {
	svc, _ := filingService(t, identityStub(), false, nil)
	var desc string
	for _, tool := range svc.ToolProvider().Tools() {
		if tool.Name == "email_move" {
			desc = tool.Description
		}
	}
	if desc == "" {
		t.Fatal("email_move has no description")
	}
	mustContain(t, desc,
		"In a turn the operator is not present for (attended: false in the Email Accounts block), each account allows moves only into the folders its mailbox.move_into names",
		"In the operator's own turn (attended: true) move_into does not apply: any folder the account has but the drafts folder is allowed.",
		"In every turn, a destination the account lacks is refused with the account's real folder list, the account's drafts folder is never a destination or a source",
	)
	for _, gone := range []string{
		"naming the gap. Each account allows moves only into",
		"An account whose entry shows owner: operator allows only its junk folder",
	} {
		if strings.Contains(desc, gone) {
			t.Errorf("email_move description still contains %q", gone)
		}
	}
}
