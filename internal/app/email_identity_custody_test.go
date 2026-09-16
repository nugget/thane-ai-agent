package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

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

// TestConfigureContactToolsOperatorTiedLegacyNameFailsClosed pins the
// startup wiring when the legacy owner name ties two known records. The
// resolver marks neither IsOwner, and the tools pin that with the tie,
// so custody refuses the chain that would otherwise choose the next
// start's operator: an address planted on one holder, then the other
// holder's nickname changed or the other holder forgotten. contact_owner
// reports the tie rather than a missing contact. The control pins the
// same Nil with no reason, which is what the wiring used to pass, and
// shows the plant would then land.
func TestConfigureContactToolsOperatorTiedLegacyNameFailsClosed(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) (*contacts.Store, *contacts.Contact, *contacts.Contact) {
		t.Helper()
		store := newEmailIdentityStore(t)
		var tied []*contacts.Contact
		for _, name := range []string{"Alice Adams", "Alice Baker"} {
			c, err := store.UpsertWithProperties(&contacts.Contact{FormattedName: name, Nickname: "Boss", Kind: "individual", TrustZone: contacts.ZoneKnown}, nil)
			if err != nil {
				t.Fatal(err)
			}
			tied = append(tied, c)
		}
		return store, tied[0], tied[1]
	}
	save := func(tools *contacts.Tools, args string) error {
		_, err := tools.SaveContactFromModel(ctx, args, &contacts.PropertyProvenance{Source: "contact_save", RequestID: "r_tie"}, false)
		return err
	}

	store, adams, baker := seed(t)
	resolver := &contactChannelBindingResolver{store: store, legacyOwnerContactName: "Boss"}
	tools := contacts.NewTools(store, nil)
	configureContactToolsOperator(tools, resolver, contactIdentityConfig{legacyOwnerContactName: "Boss"})
	for _, c := range []*contacts.Contact{adams, baker} {
		if resolver.isOperator(c) {
			t.Errorf("%s is IsOwner under a tie", c.FormattedName)
		}
	}

	const refused = "check identity custody: nothing was changed"
	if err := save(tools, `{"name":"Alice Adams","facts":{"email":"mallory@example.com"}}`); err == nil || !strings.Contains(err.Error(), refused) {
		t.Errorf("an address on a tied holder = %v, want the custody refusal", err)
	}
	if err := save(tools, `{"name":"Alice Baker","nickname":"Bee"}`); err == nil || !strings.Contains(err.Error(), refused) {
		t.Errorf("breaking the tie by nickname = %v, want the custody refusal", err)
	}
	if _, err := tools.ForgetContactFromModel(ctx, `{"contact_id":"`+baker.ID.String()+`"}`, nil); err == nil || !strings.Contains(err.Error(), refused) {
		t.Errorf("breaking the tie by forget = %v, want the custody refusal", err)
	}
	var tie *contacts.AmbiguousNameError
	if _, err := store.ResolveContact("Boss"); !errors.As(err, &tie) || !tie.ExactTie || tie.Total != 2 {
		t.Errorf("ResolveContact(Boss) = %v, want the tie intact for the next start", err)
	}
	if _, err := tools.OwnerContact(""); err == nil || strings.Contains(err.Error(), "not found") ||
		!strings.Contains(err.Error(), adams.ID.String()) || !strings.Contains(err.Error(), baker.ID.String()) {
		t.Errorf("contact_owner = %v, want the tie with both contact_ids", err)
	}

	t.Run("control: a pin without its reason lets the plant through", func(t *testing.T) {
		store, _, _ := seed(t)
		tools := contacts.NewTools(store, nil)
		tools.SetOwnerContactName("Boss")
		tools.ConfigureLegacyOperatorContactID(uuid.Nil, nil)
		if err := save(tools, `{"name":"Alice Adams","facts":{"email":"mallory@example.com"}}`); err != nil {
			t.Errorf("control save = %v, want it to land when the tie is flattened to no operator", err)
		}
	})
}
