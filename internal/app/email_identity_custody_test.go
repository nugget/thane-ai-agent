package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// TestLaunderedIdentityStaysUnmatched pins identity custody where it
// matters: at the resolvers. A model that tries to lend a household
// contact's zone to a stranger's address, claim the operator's Signal
// number, or give the operator's address a second holder is refused,
// and the send gate and Signal binding see the directory unchanged.
func TestLaunderedIdentityStaysUnmatched(t *testing.T) {
	store := newEmailIdentityStore(t)
	alice := seedContact(t, store, "Alice Operator", "alice@example.com", contacts.ZoneAdmin)
	bob := seedContact(t, store, "Bob Household", "bob@example.com", contacts.ZoneHousehold)
	tools := contacts.NewTools(store, nil)
	tools.ConfigureOperatorContactID(alice.ID)
	resolver := &contactChannelBindingResolver{store: store, operatorContactID: alice.ID}
	ctx := context.Background()
	save := func(args string) error {
		_, err := tools.SaveContactFromModel(ctx, args, &contacts.PropertyProvenance{Source: "contact_save", RequestID: "r_launder"}, false)
		return err
	}

	if err := save(`{"name":"Bob Household","facts":{"email":"mallory@example.net"}}`); err == nil || !strings.Contains(err.Error(), "operator-custodied") {
		t.Fatalf("lending Bob's zone to mallory@ must be refused: %v", err)
	}
	trust := email.CheckRecipientTrust(ctx, resolver, []string{"mallory@example.net"})
	if len(trust.Allowed) != 0 || len(trust.Assessments) != 1 || trust.Assessments[0].ContactStatus != email.ContactUnmatched {
		t.Errorf("mallory@ after refusal = %+v", trust)
	}
	if match, err := resolver.ResolveEmailContact(ctx, "mallory@example.net"); err != nil || match.TrustZone != contacts.ZoneUnknown {
		t.Errorf("mallory@ resolves as %+v, %v", match, err)
	}

	if err := save(`{"name":"Alice Operator","facts":{"phone":"+15559990000"}}`); err == nil || !strings.Contains(err.Error(), "operator's own contact") {
		t.Fatalf("a number on the operator's contact must be refused: %v", err)
	}
	if binding := resolver.ResolveChannelBinding("signal", "15559990000"); binding == nil || binding.ContactID != "" || binding.IsOwner {
		t.Errorf("signal 15559990000 binds as %+v", binding)
	}

	if err := save(`{"name":"Mallory","kind":"individual","facts":{"email":"alice@example.com"}}`); err == nil || !strings.Contains(err.Error(), "already held by Alice Operator") {
		t.Fatalf("a second holder of the operator's address must be refused: %v", err)
	}
	if _, err := store.FindByName("Mallory"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a refused create must not create the record: %v", err)
	}
	match, err := resolver.ResolveEmailContact(ctx, "alice@example.com")
	if err != nil || match.Status != email.ContactMatched || match.TrustZone != contacts.ZoneAdmin || match.Binding == nil || !match.Binding.IsOwner {
		t.Errorf("alice@ = %+v, %v", match, err)
	}

	// The negative control proves the refusal does the work: the same
	// address added through the operator path lends Bob's zone.
	if err := store.AddProperty(bob.ID, &contacts.Property{Property: "EMAIL", Value: "mallory@example.net"}); err != nil {
		t.Fatal(err)
	}
	trust = email.CheckRecipientTrust(ctx, resolver, []string{"mallory@example.net"})
	if len(trust.Allowed) != 1 || trust.Assessments[0].ContactStatus != email.ContactMatched || trust.Assessments[0].TrustZone != contacts.ZoneHousehold {
		t.Errorf("operator-added mallory@ = %+v", trust)
	}
}

// TestConfigureContactToolsOperatorPinsLegacyResolution pins the startup
// wiring for the legacy owner-name selector: the contact tools and the
// channel resolver share one resolution, so an operator write that later
// gives the name an exact match moves neither IsOwner nor custody.
func TestConfigureContactToolsOperatorPinsLegacyResolution(t *testing.T) {
	store := newEmailIdentityStore(t)
	operator, err := store.UpsertWithProperties(&contacts.Contact{FormattedName: "Alice Operator", Nickname: "Alice", Kind: "individual", TrustZone: contacts.ZoneKnown}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &contactChannelBindingResolver{store: store, legacyOwnerContactName: "Alice"}
	tools := contacts.NewTools(store, nil)
	configureContactToolsOperator(tools, resolver, contactIdentityConfig{legacyOwnerContactName: "Alice"})

	if _, err := store.Upsert(&contacts.Contact{FormattedName: "Alice", Kind: "individual"}); err != nil {
		t.Fatal(err)
	}
	if got := resolver.resolvedOperatorContactID(); got != operator.ID {
		t.Errorf("resolver operator = %v, want the startup resolution %v", got, operator.ID)
	}
	_, err = tools.SaveContactFromModel(context.Background(), `{"name":"Alice Operator","facts":{"email":"mallory@example.net"}}`,
		&contacts.PropertyProvenance{Source: "contact_save", RequestID: "r_pin"}, false)
	if err == nil || !strings.Contains(err.Error(), "Alice Operator is the operator's own contact") {
		t.Errorf("custody must protect the record the resolver marks IsOwner: %v", err)
	}
}
