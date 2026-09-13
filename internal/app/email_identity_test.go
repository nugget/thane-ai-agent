package app

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

func newEmailIdentityStore(t *testing.T) *contacts.Store {
	t.Helper()
	db, err := database.Open(t.TempDir() + "/contacts.db")
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// seedContact seeds a directory contact through the operator path, the
// way CardDAV and /v1/contacts write: the record, its zone and its
// address in one store write. Seeding a directory is operator action,
// and contact_save refuses to give an address a second holder once one
// carries authority.
func seedContact(t *testing.T, store *contacts.Store, name, addr, zone string) *contacts.Contact {
	t.Helper()
	if zone == "" {
		zone = contacts.ZoneKnown
	}
	c, err := store.UpsertWithProperties(&contacts.Contact{
		FormattedName: name,
		Kind:          "individual",
		TrustZone:     zone,
	}, []contacts.Property{{Property: "EMAIL", Value: addr}})
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return c
}

// TestResolveEmailContact pins the app-side answer the email channel
// renders on every address: one record is a match with the same
// binding Signal would see, none is a stranger, several is ambiguous
// with the least privileged zone governing and nobody the operator.
func TestResolveEmailContact(t *testing.T) {
	store := newEmailIdentityStore(t)
	operator := seedContact(t, store, "Alice Operator", "alice@example.com", contacts.ZoneAdmin)
	seedContact(t, store, "Bob Twin", "twins@example.com", contacts.ZoneTrusted)
	seedContact(t, store, "Carol Twin", "twins@example.com", contacts.ZoneKnown)
	resolver := &contactChannelBindingResolver{store: store, operatorContactID: operator.ID}
	ctx := context.Background()

	got, err := resolver.ResolveEmailContact(ctx, "ALICE@example.com")
	if err != nil {
		t.Fatalf("matched: %v", err)
	}
	if got.Status != email.ContactMatched || got.TrustZone != contacts.ZoneAdmin || got.Binding == nil || got.Binding.ContactID != operator.ID.String() || got.Binding.ContactName != "Alice Operator" || !got.Binding.IsOwner || got.Binding.Channel != "email" || got.Binding.LinkSource != "email" {
		t.Errorf("matched = %+v (binding %+v)", got, got.Binding)
	}

	got, err = resolver.ResolveEmailContact(ctx, "nobody@example.com")
	if err != nil || got.Status != email.ContactUnmatched || got.TrustZone != contacts.ZoneUnknown || got.Binding != nil {
		t.Errorf("stranger = %+v, %v", got, err)
	}

	got, err = resolver.ResolveEmailContact(ctx, "twins@example.com")
	if err != nil {
		t.Fatalf("ambiguous: %v", err)
	}
	if got.Status != email.ContactAmbiguous || got.TrustZone != contacts.ZoneKnown || got.Binding != nil || len(got.Candidates) != 2 {
		t.Errorf("ambiguous = %+v", got)
	}
	for _, c := range got.Candidates {
		if c.ID == "" || c.Name == "" || c.TrustZone == "" {
			t.Errorf("candidate incomplete: %+v", c)
		}
	}

	var nilResolver *contactChannelBindingResolver
	if got, err := nilResolver.ResolveEmailContact(ctx, "alice@example.com"); err != nil || got.Status != email.ContactUnmatched {
		t.Errorf("nil resolver = %+v, %v", got, err)
	}
}

func TestLeastPrivilegedZone(t *testing.T) {
	mk := func(zones ...string) []*contacts.Contact {
		out := make([]*contacts.Contact, 0, len(zones))
		for _, z := range zones {
			out = append(out, &contacts.Contact{TrustZone: z})
		}
		return out
	}
	tests := []struct {
		name  string
		zones []string
		want  string
	}{
		{name: "admin and household", zones: []string{contacts.ZoneAdmin, contacts.ZoneHousehold}, want: contacts.ZoneHousehold},
		{name: "trusted and known", zones: []string{contacts.ZoneKnown, contacts.ZoneTrusted}, want: contacts.ZoneKnown},
		{name: "unknown zone value ranks lowest", zones: []string{contacts.ZoneAdmin, "vip"}, want: contacts.ZoneUnknown},
		{name: "empty", zones: nil, want: contacts.ZoneUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leastPrivilegedZone(mk(tt.zones...)); got != tt.want {
				t.Errorf("leastPrivilegedZone(%v) = %q, want %q", tt.zones, got, tt.want)
			}
		})
	}
}

// TestEmailInteractionRecorderWritesNewerOnly pins the recorder the
// poller uses: it lands on the contact with the email channel and
// direction, and an older replay does not move it back.
func TestEmailInteractionRecorderWritesNewerOnly(t *testing.T) {
	store := newEmailIdentityStore(t)
	alice := seedContact(t, store, "Alice", "alice@example.com", "")
	recorder := &emailInteractionRecorder{store: store}
	ctx := context.Background()

	at := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	if err := recorder.RecordEmailInteraction(ctx, email.Interaction{ContactID: alice.ID.String(), At: at, Direction: email.DirectionInbound, Account: "primary", MessageID: "m1@example.com"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := recorder.RecordEmailInteraction(ctx, email.Interaction{ContactID: alice.ID.String(), At: at.Add(-time.Hour), Direction: email.DirectionInbound, Account: "primary", MessageID: "m0@example.com"}); err != nil {
		t.Fatalf("older record: %v", err)
	}
	got, err := store.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastInteraction.Equal(at) || got.LastInteractionMeta == nil || got.LastInteractionMeta.Channel != "email" || got.LastInteractionMeta.Direction != email.DirectionInbound || got.LastInteractionMeta.MessageID != "m1@example.com" {
		t.Errorf("contact after records: %v %+v", got.LastInteraction, got.LastInteractionMeta)
	}

	if err := recorder.RecordEmailInteraction(ctx, email.Interaction{ContactID: "not-a-uuid", At: at}); err == nil {
		t.Error("a malformed contact id must be an error")
	}
	if err := recorder.RecordEmailInteraction(ctx, email.Interaction{ContactID: uuid.New().String(), At: at}); err == nil {
		t.Error("an unknown contact must be an error")
	}
}

// TestResolveEmailContactCountsEveryDuplicate pins that the candidate
// cap is not silent: past ten records the match still carries the full
// count, and the least privileged zone is computed over all of them.
func TestResolveEmailContactCountsEveryDuplicate(t *testing.T) {
	store := newEmailIdentityStore(t)
	for i := 0; i < 55; i++ {
		zone := contacts.ZoneTrusted
		if i == 54 {
			zone = contacts.ZoneKnown // sorts last, past the rendered cap
		}
		seedContact(t, store, fmt.Sprintf("Dup %02d", i), "shared@example.com", zone)
	}
	resolver := &contactChannelBindingResolver{store: store}
	got, err := resolver.ResolveEmailContact(context.Background(), "shared@example.com")
	if err != nil {
		t.Fatalf("ResolveEmailContact: %v", err)
	}
	if got.Status != email.ContactAmbiguous || len(got.Candidates) != maxAmbiguousCandidates || got.CandidatesTotal != 55 || got.TrustZone != contacts.ZoneKnown {
		t.Errorf("match = status %s, %d candidates, total %d, zone %s", got.Status, len(got.Candidates), got.CandidatesTotal, got.TrustZone)
	}
}

// TestAutomatedAddressCappedThroughRealStore runs the send gate over the
// real directory: a notifications address filed on an admin record is
// refused at known, while the directory itself still answers admin, so
// the cap lives in the email normaliser and not in the store.
func TestAutomatedAddressCappedThroughRealStore(t *testing.T) {
	store := newEmailIdentityStore(t)
	forge := seedContact(t, store, "Forge Notices", "notifications@forge.example", contacts.ZoneAdmin)
	seedContact(t, store, "Bob Admin", "bob@example.com", contacts.ZoneAdmin)
	resolver := &contactChannelBindingResolver{store: store}
	ctx := context.Background()

	raw, err := resolver.ResolveEmailContact(ctx, "notifications@forge.example")
	if err != nil || raw.TrustZone != contacts.ZoneAdmin || raw.Automated {
		t.Fatalf("the raw directory answer = %+v, %v; want admin, unmarked", raw, err)
	}

	result := email.CheckRecipientTrust(ctx, resolver, []string{"notifications@forge.example", "bob@example.com"})
	if len(result.Assessments) != 2 {
		t.Fatalf("assessments = %+v", result.Assessments)
	}
	automated := result.Assessments[0]
	if automated.Allowed || !automated.Automated || automated.TrustZone != contacts.ZoneKnown || automated.Contact == nil || automated.Contact.ID != forge.ID.String() {
		t.Errorf("automated recipient = %+v", automated)
	}
	if human := result.Assessments[1]; !human.Allowed || human.Automated || human.TrustZone != contacts.ZoneAdmin {
		t.Errorf("human admin recipient = %+v", human)
	}
}
