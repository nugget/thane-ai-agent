package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// routingCustodyHA records the Home Assistant notify services a test
// sends through.
type routingCustodyHA struct {
	services []string
}

func (h *routingCustodyHA) CallService(_ context.Context, domain, service string, _ map[string]any) error {
	h.services = append(h.services, domain+"."+service)
	return nil
}

// TestNotificationRoutingStaysWithTheOperator pins #1566 where it
// matters: at delivery. A model that tries to point the operator's
// notifications at another device, or to create a contact that answers
// to the operator's nickname, is refused, and a send to "nugget" reaches
// no device it planted. Kind decides nothing. The attended and operator
// paths still route.
func TestNotificationRoutingStaysWithTheOperator(t *testing.T) {
	store := newEmailIdentityStore(t)
	alice, err := store.UpsertWithProperties(&contacts.Contact{
		FormattedName: "Alice Operator",
		Nickname:      "nugget",
		Kind:          "individual",
		TrustZone:     contacts.ZoneAdmin,
	}, []contacts.Property{{Property: "EMAIL", Value: "alice@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	bob := seedContact(t, store, "Bob Household", "bob@example.com", contacts.ZoneHousehold)
	tools := contacts.NewTools(store, nil)
	tools.ConfigureOperatorContactID(alice.ID)
	resolver := &contactChannelBindingResolver{store: store, operatorContactID: alice.ID}

	ha := &routingCustodyHA{}
	sender := notifications.NewSender(ha, store, nil, "thane", slog.Default())
	router := notifications.NewNotificationRouter(store, nil, slog.Default())
	router.RegisterProvider(notifications.NewHAPushProvider(sender))

	ctx := context.Background()
	save := func(args string, attended bool) error {
		_, err := tools.SaveContactFromModel(ctx, args, &contacts.PropertyProvenance{Source: "contact_save", RequestID: "r_routing"}, attended)
		return err
	}
	send := func(recipient string) error {
		return router.Send(ctx, notifications.Notification{Recipient: recipient, Message: "door left open"})
	}
	isOwner := func() bool {
		t.Helper()
		match, err := resolver.ResolveEmailContact(ctx, "alice@example.com")
		if err != nil {
			t.Fatal(err)
		}
		return match.Binding != nil && match.Binding.IsOwner
	}

	if err := save(`{"name":"Alice Operator","facts":{"ha_companion_app":"mobile_app_mallory"}}`, false); err == nil || !strings.Contains(err.Error(), "operator's own contact") {
		t.Fatalf("an unattended device on the operator must be refused: %v", err)
	}
	if err := save(`{"name":"nugget","kind":"individual","facts":{"ha_companion_app":"mobile_app_mallory"}}`, false); err == nil || !strings.Contains(err.Error(), `Alice Operator already goes by "nugget"`) {
		t.Fatalf("a new contact under the operator's nickname must be refused: %v", err)
	}
	if _, err := store.FindByName("nugget"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a refused create must not create the record: %v", err)
	}
	if err := save(`{"name":"Bob Household","nickname":"nugget"}`, false); err == nil {
		t.Fatal("Bob taking the operator's nickname must be refused")
	}
	if err := send("nugget"); err == nil || len(ha.services) != 0 {
		t.Fatalf("send to nugget after refusals = %v, HA calls %v, want no device", err, ha.services)
	}

	// Positive control: the operator's own message routes her device.
	if err := save(`{"name":"Alice Operator","facts":{"ha_companion_app":"mobile_app_alice"}}`, true); err != nil {
		t.Fatalf("attended device save: %v", err)
	}
	if err := send("nugget"); err != nil || len(ha.services) != 1 || ha.services[0] != "notify.mobile_app_alice" {
		t.Fatalf("send to nugget = %v, HA calls %v", err, ha.services)
	}

	// Kind is model-writable and decides nothing: kind=org on the
	// operator moves neither delivery nor IsOwner.
	if !isOwner() {
		t.Fatal("alice@ must bind as the owner before the kind change")
	}
	if err := save(`{"name":"Alice Operator","kind":"org"}`, false); err != nil {
		t.Fatalf("kind save: %v", err)
	}
	if got, err := store.Get(alice.ID); err != nil || got.Kind != "org" {
		t.Fatalf("kind after save = %+v, %v", got, err)
	}
	if !isOwner() {
		t.Error("kind=org moved IsOwner")
	}
	if err := send("nugget"); err != nil || len(ha.services) != 2 || ha.services[1] != "notify.mobile_app_alice" {
		t.Errorf("send after kind=org = %v, HA calls %v", err, ha.services)
	}

	// The operator path routes in any case: CardDAV and /v1/contacts
	// write upper-case property names.
	if err := store.AddProperty(bob.ID, &contacts.Property{Property: "HA_COMPANION_APP", Value: "mobile_app_bob"}); err != nil {
		t.Fatal(err)
	}
	if err := send("Bob Household"); err != nil || len(ha.services) != 3 || ha.services[2] != "notify.mobile_app_bob" {
		t.Errorf("send to Bob = %v, HA calls %v", err, ha.services)
	}
}

// TestSharedNicknameReachesTheOperator pins operator-first nickname
// resolution through the app's own wiring. An ordinary known contact
// that already shares the operator's nickname, with the lower id, must
// not take the operator's notifications or conversation context, under
// either operator selector.
func TestSharedNicknameReachesTheOperator(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity func(operator *contacts.Contact) contactIdentityConfig
	}{
		{"operator_contact_id", func(operator *contacts.Contact) contactIdentityConfig {
			return contactIdentityConfig{operatorContactID: operator.ID}
		}},
		{"legacy owner name", func(operator *contacts.Contact) contactIdentityConfig {
			return contactIdentityConfig{legacyOwnerContactName: operator.FormattedName}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newEmailIdentityStore(t)
			seed := func(name, device string) *contacts.Contact {
				t.Helper()
				c, err := store.UpsertWithProperties(&contacts.Contact{
					FormattedName: name,
					Nickname:      "nugget",
					Kind:          "individual",
					TrustZone:     contacts.ZoneKnown,
				}, []contacts.Property{{Property: contacts.PropertyHACompanionApp, Value: device}})
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			kim := seed("Kim Known", "mobile_app_kim")
			alice := seed("Alice Operator", "mobile_app_alice")
			if kim.ID.String() > alice.ID.String() {
				t.Fatal("seed order must give the ordinary contact the lower id")
			}

			identity := tc.identity(alice)
			resolver := &contactChannelBindingResolver{
				store:                  store,
				operatorContactID:      identity.operatorContactID,
				legacyOwnerContactName: identity.legacyOwnerContactName,
			}
			configureContactToolsOperator(contacts.NewTools(store, nil), resolver, identity)

			ha := &routingCustodyHA{}
			router := notifications.NewNotificationRouter(store, nil, slog.Default())
			router.RegisterProvider(notifications.NewHAPushProvider(notifications.NewSender(ha, store, nil, "thane", slog.Default())))
			ctx := context.Background()
			if err := router.Send(ctx, notifications.Notification{Recipient: "nugget", Message: "door left open"}); err != nil || len(ha.services) != 1 || ha.services[0] != "notify.mobile_app_alice" {
				t.Errorf("send to nugget = %v, HA calls %v, want notify.mobile_app_alice", err, ha.services)
			}
			lookup := &contactNameLookup{store: store, logger: slog.Default()}
			if cc := lookup.LookupContact(ctx, "nugget", "signal"); cc == nil || cc.ID != alice.ID.String() {
				t.Errorf("context for nugget = %+v, want Alice Operator", cc)
			}
		})
	}
}
