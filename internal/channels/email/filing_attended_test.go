package email

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
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
		{"operator unattended refuses the unlisted folder", "operator", nil, false, "INBOX", byName("Receipts"), "Receipts", []string{`cannot file into "Receipts"`, "in a turn the operator is not present for", "ask for it in their own conversation, where move_into does not apply", "nothing was moved"}},
		{"operator unattended refuses the unlisted role", "operator", nil, false, "INBOX", byRole("archive"), "Archive", []string{`cannot file into "Archive"`, "nothing was moved"}},
		{"operator attended returns mail to INBOX from an unlisted folder", "operator", nil, true, "Receipts", byRole("inbox"), "INBOX", nil},
		{"operator unattended refuses that return", "operator", nil, false, "Receipts", byRole("inbox"), "INBOX", []string{`cannot return mail to INBOX from "Receipts"`, "nothing was moved"}},
		{"operator attended never files into the drafts folder by name", "operator", nil, true, "INBOX", byName("Drafts"), "Drafts", []string{`email_move cannot file mail into "Drafts"`, "drafts folder"}},
		{"operator attended never files by destination_role drafts", "operator", nil, true, "INBOX", byRole("drafts"), "Drafts", []string{`email_move cannot file mail into "Drafts"`, "drafts folder"}},
		{"operator attended never moves mail out of the drafts folder", "operator", nil, true, "Drafts", byName("Receipts"), "Receipts", []string{`email_move cannot act on mail in "Drafts"`, "nothing was moved"}},
		{"operator attended never returns drafts to INBOX", "operator", nil, true, "Drafts", byRole("inbox"), "INBOX", []string{`email_move cannot act on mail in "Drafts"`, "nothing was moved"}},
		{"limited attended files outside its list", "assistant", limited, true, "INBOX", byRole("trash"), "Trash", nil},
		{"limited unattended refuses outside its list", "assistant", limited, false, "INBOX", byRole("trash"), "Trash", []string{`its mailbox.move_into allows only ["Receipts"]`, "ask for it in their own conversation", "nothing was moved"}},
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
// line saying it went ahead because the operator was present, an
// attended move the rule allows logs no such line, and an unattended
// move outside the rule logs a refusal instead.
func TestAttendedMoveOutsideMoveIntoIsLogged(t *testing.T) {
	const allowed = "email move outside move_into allowed"
	const refused = "email move refused"
	tests := []struct {
		name        string
		attended    bool
		target      map[string]any
		wantRefusal bool
		want        []string
		wantAbsent  []string
	}{
		{"attended outside move_into", true, map[string]any{"destination": "Receipts"}, false, []string{allowed, "reason=operator_present", "account=primary", "source_folder=INBOX", "destination=Receipts", "owner=operator"}, []string{refused}},
		{"attended inside move_into", true, map[string]any{"destination_role": "junk"}, false, nil, []string{allowed, refused}},
		{"unattended outside move_into", false, map[string]any{"destination": "Receipts"}, true, []string{refused, "reason=outside_move_into"}, []string{allowed}},
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

			_, err := svc.ToolProvider().HandleMove(ctx, args)
			if (err != nil) != tt.wantRefusal {
				t.Fatalf("HandleMove error = %v, want refused = %v", err, tt.wantRefusal)
			}
			logged := buf.String()
			mustContain(t, logged, tt.want...)
			for _, absent := range tt.wantAbsent {
				if strings.Contains(logged, absent) {
					t.Errorf("log contains %q:\n%s", absent, logged)
				}
			}
		})
	}
}
