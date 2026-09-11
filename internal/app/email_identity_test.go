package app

import (
	"context"
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

func seedContact(t *testing.T, store *contacts.Store, name, addr, zone string) *contacts.Contact {
	t.Helper()
	tools := contacts.NewTools(store, nil)
	if _, err := tools.SaveContact(`{"name":"` + name + `","kind":"individual","facts":{"email":"` + addr + `"}}`); err != nil {
		t.Fatalf("SaveContact %s: %v", name, err)
	}
	c, err := store.FindByName(name)
	if err != nil {
		t.Fatalf("FindByName %s: %v", name, err)
	}
	if zone != "" {
		c.TrustZone = zone
		if _, err := store.Upsert(c); err != nil {
			t.Fatalf("Upsert zone: %v", err)
		}
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
