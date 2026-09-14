package contacts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"
)

func custodyArgs(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// seedContactAt seeds a contact the way the operator does, through the
// store: the record, its zone and its properties in one write.
func seedContactAt(t *testing.T, store *Store, name, zone string, props ...Property) *Contact {
	t.Helper()
	c, err := store.UpsertWithProperties(&Contact{FormattedName: name, Kind: "individual", TrustZone: zone}, props)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return c
}

// newCountingTools returns contact tools whose mutation sink counts
// committed changes.
func newCountingTools(t *testing.T) (*Tools, *int) {
	t.Helper()
	calls := 0
	tools := NewTools(newTestStore(t), func(context.Context, ContactMutation) error {
		calls++
		return nil
	})
	return tools, &calls
}

func modelSave(tools *Tools, args string, attended bool) (string, error) {
	return tools.SaveContactFromModel(context.Background(), args, &PropertyProvenance{Source: "contact_save", RequestID: "r_custody"}, attended)
}

func requireContains(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal containing %q", wants)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%v", want, err)
		}
	}
}

// identityFacts covers every spelling an address or number can arrive
// in, with the canonical row a known record stores it as.
var identityFacts = []struct {
	key, value   string
	wantProperty string
	wantValue    string
	topLevel     bool
}{
	{key: "email", value: "bob.new@example.com", wantProperty: "EMAIL", wantValue: "bob.new@example.com"},
	{key: "EMAIL", value: "bob.new@example.com", wantProperty: "EMAIL", wantValue: "bob.new@example.com"},
	{key: "Email", value: "bob.new@example.com", wantProperty: "EMAIL", wantValue: "bob.new@example.com"},
	{key: "phone", value: "+15551230001", wantProperty: "TEL", wantValue: "+15551230001"},
	{key: "tel", value: "+15551230001", wantProperty: "TEL", wantValue: "+15551230001"},
	{key: "TEL", value: "+15551230001", wantProperty: "TEL", wantValue: "+15551230001"},
	{key: "signal", value: "+15551230002", wantProperty: "IMPP", wantValue: "signal:+15551230002"},
	{key: "matrix", value: "@bob:matrix.org", wantProperty: "IMPP", wantValue: "matrix:@bob:matrix.org"},
	{key: "IMPP", value: "signal:+15551230003", wantProperty: "IMPP", wantValue: "signal:+15551230003"},
	{key: "email", value: "bob.top@example.com", wantProperty: "EMAIL", wantValue: "bob.top@example.com", topLevel: true},
}

func identityFactArgs(name, key, value string, topLevel bool) map[string]any {
	args := map[string]any{"name": name}
	if topLevel {
		args[key] = value
	} else {
		args["facts"] = map[string]string{key: value}
	}
	return args
}

// TestSaveContact_IdentityCustody pins the target rule: outside the
// operator's own turn, no address or number is added to a contact above
// known, in any spelling, and a refusal leaves nothing behind.
func TestSaveContact_IdentityCustody(t *testing.T) {
	for _, zone := range []string{ZoneAdmin, ZoneHousehold, ZoneTrusted} {
		for _, f := range identityFacts {
			name := zone + "/" + f.key
			if f.topLevel {
				name += "/top-level"
			}
			t.Run(name, func(t *testing.T) {
				tools, calls := newCountingTools(t)
				target := seedContactAt(t, tools.store, "Bob Smith", zone)
				_, err := modelSave(tools, custodyArgs(t, identityFactArgs("Bob Smith", f.key, f.value, f.topLevel)), false)
				requireContains(t, err, "operator-custodied", fmt.Sprintf("%q", f.key), "Bob Smith is "+zone, "nothing was saved", "CardDAV")
				props, err := tools.store.GetProperties(target.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(props) != 0 {
					t.Errorf("refused save wrote %+v", props)
				}
				if *calls != 0 {
					t.Errorf("refused save signaled %d mutations", *calls)
				}
			})
		}
	}

	t.Run("known record stores canonical identity", func(t *testing.T) {
		for _, f := range identityFacts {
			tools, _ := newCountingTools(t)
			target := seedContactAt(t, tools.store, "Kim Known", ZoneKnown)
			if _, err := modelSave(tools, custodyArgs(t, identityFactArgs("Kim Known", f.key, f.value, f.topLevel)), false); err != nil {
				t.Fatalf("%s on a known record: %v", f.key, err)
			}
			props, err := tools.store.GetProperties(target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(props) != 1 || props[0].Property != f.wantProperty || props[0].Value != f.wantValue {
				t.Errorf("%s=%q stored as %+v, want %s %q", f.key, f.value, props, f.wantProperty, f.wantValue)
			}
		}
	})

	// ha_companion_app was this subtest's example of an open fact until
	// #1566 put routing facts under the target rule; it now sits in
	// TestSaveContact_RoutingFactCustody's refusal matrix.
	t.Run("uncustodied facts on a household record", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		target := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold)
		if _, err := modelSave(tools, `{"name":"Bob Smith","ai_summary":"Prefers Signal","facts":{"favorite_color":"green"}}`, false); err != nil {
			t.Fatalf("uncustodied save: %v", err)
		}
		got, err := tools.store.GetPropertiesMap(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got["favorite_color"], []string{"green"}) {
			t.Errorf("properties = %v", got)
		}
	})

	t.Run("re-saving a held address is a no-op", func(t *testing.T) {
		tools, calls := newCountingTools(t)
		seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold, Property{Property: "EMAIL", Value: "bob@example.com"})
		result, err := modelSave(tools, `{"name":"Bob Smith","facts":{"email":"BOB@Example.com"}}`, false)
		if err != nil || !strings.Contains(result, "Contact unchanged") {
			t.Fatalf("re-save = %q, %v", result, err)
		}
		if *calls != 0 {
			t.Errorf("no-op signaled %d mutations", *calls)
		}
	})
}

// TestSaveContact_AuthorityHolderDuplicate pins the holder rule: a
// value a contact with authority already holds never gets a second
// holder, in any spelling the resolvers treat as the same.
func TestSaveContact_AuthorityHolderDuplicate(t *testing.T) {
	forms := []struct {
		name       string
		held       Property
		key, value string
	}{
		{"email case variant", Property{Property: "EMAIL", Value: "carol@example.com"}, "email", "CAROL@Example.com"},
		{"tel without plus, phone with plus", Property{Property: "TEL", Value: "15551234567"}, "phone", "+15551234567"},
		{"tel holder, signal fact", Property{Property: "TEL", Value: "+15551234567"}, "signal", "+15551234567"},
		{"signal holder, phone fact", Property{Property: "IMPP", Value: "signal:+15551234567"}, "phone", "15551234567"},
	}
	holders := []struct {
		name     string
		zone     string
		operator bool
	}{
		{"admin", ZoneAdmin, false},
		{"household", ZoneHousehold, false},
		{"trusted", ZoneTrusted, false},
		{"known operator", ZoneKnown, true},
	}
	for _, h := range holders {
		for _, form := range forms {
			t.Run(h.name+"/"+form.name, func(t *testing.T) {
				tools, calls := newCountingTools(t)
				holder := seedContactAt(t, tools.store, "Carol Jones", h.zone, form.held)
				if h.operator {
					tools.ConfigureOperatorContactID(holder.ID)
				}
				target := seedContactAt(t, tools.store, "Dan Known", ZoneKnown)
				wants := []string{"operator-custodied", "already held by Carol Jones (" + h.zone + ", " + holder.ID.String(), "nothing was saved"}
				if h.operator {
					wants = append(wants, "the operator's own contact")
				}

				_, err := modelSave(tools, custodyArgs(t, identityFactArgs("Dan Known", form.key, form.value, false)), false)
				requireContains(t, err, wants...)
				props, err := tools.store.GetProperties(target.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(props) != 0 {
					t.Errorf("refused save wrote %+v", props)
				}

				// Creating a new record with the value is refused the same way.
				_, err = modelSave(tools, custodyArgs(t, map[string]any{"name": "Mallory", "kind": "individual", "facts": map[string]string{form.key: form.value}}), false)
				requireContains(t, err, wants...)
				if _, err := tools.store.FindByName("Mallory"); !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("a refused create must not create the record: %v", err)
				}

				matches, err := tools.store.FindAllByPropertyExact(context.Background(), form.held.Property, form.held.Value)
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) != 1 || matches[0].ID != holder.ID {
					t.Errorf("holder no longer resolves uniquely: %+v", matches)
				}
				if *calls != 0 {
					t.Errorf("refused saves signaled %d mutations", *calls)
				}
			})
		}
	}

	t.Run("a plain known holder allows the duplicate", func(t *testing.T) {
		for _, form := range forms {
			tools, _ := newCountingTools(t)
			seedContactAt(t, tools.store, "Carol Jones", ZoneKnown, form.held)
			target := seedContactAt(t, tools.store, "Dan Known", ZoneKnown)
			if _, err := modelSave(tools, custodyArgs(t, identityFactArgs("Dan Known", form.key, form.value, false)), false); err != nil {
				t.Fatalf("%s: known-to-known duplicate must be allowed: %v", form.name, err)
			}
			props, err := tools.store.GetProperties(target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(props) != 1 {
				t.Errorf("%s: target properties = %+v", form.name, props)
			}
		}
	})
}

// TestSaveContact_OperatorRecordAtKnown pins that the operator's own
// record is custody at any zone, under either operator selector.
func TestSaveContact_OperatorRecordAtKnown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Tools, *Contact)
	}{
		{"operator_contact_id", func(tools *Tools, c *Contact) { tools.ConfigureOperatorContactID(c.ID) }},
		{"legacy owner name", func(tools *Tools, c *Contact) { tools.SetOwnerContactName(c.FormattedName) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, calls := newCountingTools(t)
			operator := seedContactAt(t, tools.store, "Alice Operator", ZoneKnown)
			seedContactAt(t, tools.store, "Bob Known", ZoneKnown)
			tc.configure(tools, operator)

			_, err := modelSave(tools, `{"name":"Alice Operator","facts":{"phone":"+15559990000"}}`, false)
			requireContains(t, err, "operator-custodied", "Alice Operator is the operator's own contact")
			props, err := tools.store.GetProperties(operator.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(props) != 0 || *calls != 0 {
				t.Errorf("refused save wrote %+v and signaled %d", props, *calls)
			}

			if _, err := modelSave(tools, `{"name":"Bob Known","facts":{"phone":"+15559990000"}}`, false); err != nil {
				t.Errorf("a second known record must accept the number: %v", err)
			}
		})
	}
}

// TestSaveContactFromModel_AttendedLiftsTargetCustody pins the
// operator's carve-out: their own turn may add an address to a contact
// above known and to their own contact, and nothing else is lifted.
func TestSaveContactFromModel_AttendedLiftsTargetCustody(t *testing.T) {
	tools, calls := newCountingTools(t)
	alice := seedContactAt(t, tools.store, "Alice Operator", ZoneAdmin)
	tools.ConfigureOperatorContactID(alice.ID)
	bob := seedContactAt(t, tools.store, "Bob Household", ZoneHousehold)
	seedContactAt(t, tools.store, "Carol Holder", ZoneTrusted, Property{Property: "EMAIL", Value: "carol@example.com"})
	seedContactAt(t, tools.store, "Dan Known", ZoneKnown)

	if _, err := modelSave(tools, `{"name":"Bob Household","facts":{"email":"bob.new@example.com"}}`, true); err != nil {
		t.Fatalf("attended save to a household contact: %v", err)
	}
	if matches, err := tools.store.FindAllByPropertyExact(context.Background(), "EMAIL", "bob.new@example.com"); err != nil || len(matches) != 1 || matches[0].ID != bob.ID {
		t.Errorf("bob.new@ holders = %+v, %v", matches, err)
	}
	if _, err := modelSave(tools, `{"name":"Alice Operator","facts":{"phone":"+15559990000"}}`, true); err != nil {
		t.Fatalf("attended save to the operator's contact: %v", err)
	}

	for _, target := range []string{"Dan Known", "Bob Household"} {
		_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": target, "facts": map[string]string{"email": "carol@example.com"}}), true)
		requireContains(t, err, "already held by Carol Holder (trusted")
		if err != nil && strings.Contains(err.Error(), "not the operator's own message") {
			t.Errorf("an attended refusal must not claim the turn is unattended: %v", err)
		}
	}
	if *calls != 2 {
		t.Errorf("mutations = %d, want the two attended additions", *calls)
	}
}

// TestSaveContact_RefusesCustodyHeaders pins that Thane's own headers
// are not fact keys, in any case, in facts or at top level.
func TestSaveContact_RefusesCustodyHeaders(t *testing.T) {
	for _, key := range []string{"X-THANE-TRUST-ZONE", "x-thane-ha-person", "X-Thane-Trust-Zone"} {
		for _, topLevel := range []bool{false, true} {
			tools := newTestTools(t)
			args := identityFactArgs("Header Holder", key, "admin", topLevel)
			args["kind"] = "individual"
			_, err := tools.SaveContact(custodyArgs(t, args))
			requireContains(t, err, "operator-custodied", fmt.Sprintf("%q", key), "nothing was saved")
			if _, err := tools.store.FindByName("Header Holder"); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%s (top-level %v): a refused save must not create the contact", key, topLevel)
			}
		}
	}
}

// TestSaveContact_RefusesVCardSyntaxInFactKeys pins the fact-key
// grammar: a key or value that a vCard would read as another property
// is refused, and one error names every offender.
func TestSaveContact_RefusesVCardSyntaxInFactKeys(t *testing.T) {
	tools := newTestTools(t)
	refused := map[string]string{
		"item1.email":               "mallory@example.net",
		"EMAIL;TYPE=work":           "mallory@example.net",
		"NOTE:x\r\nEMAIL":           "mallory@example.net",
		"X-THANE-TRUST-ZONE;PREF=1": "admin",
		"g.KEY":                     "attacker-key",
		"x-thane-ha-person":         "person.mallory",
		"X-Thane-Origin-Tag":        "owner",
		"home phone":                "+15550001111",
		"timezone":                  "America/Chicago\rEMAIL:mallory@example.net",
	}
	facts := map[string]string{"ha_companion_app": "mobile_app_x"}
	for key, value := range refused {
		facts[key] = value
	}
	_, err := tools.SaveContact(custodyArgs(t, map[string]any{"name": "Syntax Holder", "kind": "individual", "facts": facts}))
	wants := []string{fmt.Sprintf("refused %d fact(s); nothing was saved", len(refused))}
	for key := range refused {
		wants = append(wants, fmt.Sprintf("%q", key))
	}
	requireContains(t, err, wants...)
	if err != nil && strings.Contains(err.Error(), `"ha_companion_app"`) {
		t.Errorf("a valid fact was named as refused: %v", err)
	}
	if _, err := tools.store.FindByName("Syntax Holder"); !errors.Is(err, sql.ErrNoRows) {
		t.Error("a refused save must not create the contact")
	}

	if _, err := tools.SaveContact(`{"name":"Syntax Holder","kind":"individual","facts":{"ha_companion_app":"mobile_app_x","notification_preference":"signal"}}`); err != nil {
		t.Errorf("plain keys must pass: %v", err)
	}
}

// TestApplyContactSave_ConcurrentChangeAborts pins the in-transaction
// snapshot check: a save read before an operator zone change or delete
// neither reverts the zone nor resurrects the record.
func TestApplyContactSave_ConcurrentChangeAborts(t *testing.T) {
	store := newTestStore(t)
	note := []Property{{Property: "timezone", Value: "America/Chicago"}}

	promoted, err := store.Upsert(&Contact{FormattedName: "Snap Shot", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.GetWithProperties(promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Get(promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.TrustZone = ZoneHousehold
	if _, err := store.Upsert(fresh); err != nil {
		t.Fatal(err)
	}
	stale.Note = "stale note"
	if _, _, err := store.applyContactSave(stale, true, note, nil, identityGuard{snapshotZone: ZoneKnown}); !errors.Is(err, errContactChangedConcurrently) {
		t.Fatalf("stale zone save error = %v, want errContactChangedConcurrently", err)
	}
	got, err := store.GetWithProperties(promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustZone != ZoneHousehold || got.Note != "" || len(got.Properties) != 0 {
		t.Errorf("aborted save changed the record: zone %q note %q props %+v", got.TrustZone, got.Note, got.Properties)
	}

	deleted, err := store.Upsert(&Contact{FormattedName: "Gone Away", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}
	staleDeleted, err := store.GetWithProperties(deleted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(deleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyContactSave(staleDeleted, true, note, nil, identityGuard{snapshotZone: ZoneKnown}); !errors.Is(err, errContactChangedConcurrently) {
		t.Fatalf("deleted save error = %v, want errContactChangedConcurrently", err)
	}
	if _, err := store.Get(deleted.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("aborted save resurrected the contact: %v", err)
	}

	current, err := store.GetWithProperties(promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Note = "fresh note"
	if _, outcome, err := store.applyContactSave(current, true, note, nil, identityGuard{snapshotZone: ZoneHousehold}); err != nil || !outcome.changed {
		t.Fatalf("current snapshot save = changed %v, %v", outcome.changed, err)
	}
	got, err = store.Get(promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "fresh note" || got.TrustZone != ZoneHousehold {
		t.Errorf("current save = note %q zone %q", got.Note, got.TrustZone)
	}

	// An operator nickname edit between the read and the save aborts the
	// save too, so the snapshot re-upsert never reverts it.
	named, err := store.Upsert(&Contact{FormattedName: "Nick Named", Nickname: "Nick", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}
	staleNamed, err := store.GetWithProperties(named.ID)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := store.Get(named.ID)
	if err != nil {
		t.Fatal(err)
	}
	edited.Nickname = "Nico"
	if _, err := store.Upsert(edited); err != nil {
		t.Fatal(err)
	}
	staleNamed.Note = "stale note"
	if _, _, err := store.applyContactSave(staleNamed, true, nil, nil, identityGuard{snapshotZone: ZoneKnown, snapshotNickname: staleNamed.Nickname}); !errors.Is(err, errContactChangedConcurrently) {
		t.Fatalf("stale nickname save error = %v, want errContactChangedConcurrently", err)
	}
	if got, err := store.Get(named.ID); err != nil || got.Nickname != "Nico" || got.Note != "" {
		t.Errorf("aborted save changed the record: %+v, %v", got, err)
	}
	freshNamed, err := store.GetWithProperties(named.ID)
	if err != nil {
		t.Fatal(err)
	}
	freshNamed.Note = "fresh note"
	if _, outcome, err := store.applyContactSave(freshNamed, true, nil, nil, identityGuard{snapshotZone: ZoneKnown, snapshotNickname: freshNamed.Nickname}); err != nil || !outcome.changed {
		t.Fatalf("current nickname snapshot save = changed %v, %v", outcome.changed, err)
	}
	if got, err := store.Get(named.ID); err != nil || got.Nickname != "Nico" || got.Note != "fresh note" {
		t.Errorf("current save = %+v, %v", got, err)
	}
}

// TestForgetContact_Custody pins that contact_forget removes only a
// known contact that is neither the operator's nor bound to a Home
// Assistant person, and names what it removed.
func TestForgetContact_Custody(t *testing.T) {
	cases := []struct {
		name       string
		zone       string
		setup      func(*testing.T, *Tools, *Contact)
		wantReason string
	}{
		{name: "admin", zone: ZoneAdmin, wantReason: "it is admin"},
		{name: "household", zone: ZoneHousehold, wantReason: "it is household"},
		{name: "trusted", zone: ZoneTrusted, wantReason: "it is trusted"},
		{name: "known operator", zone: ZoneKnown, wantReason: "it is the operator's own contact",
			setup: func(_ *testing.T, tools *Tools, c *Contact) { tools.ConfigureOperatorContactID(c.ID) }},
		{name: "known legacy owner", zone: ZoneKnown, wantReason: "it is the operator's own contact",
			setup: func(_ *testing.T, tools *Tools, c *Contact) { tools.SetOwnerContactName(c.FormattedName) }},
		{name: "known bound to a Home Assistant person", zone: ZoneKnown, wantReason: "bound to Home Assistant person person.erin",
			setup: func(t *testing.T, tools *Tools, c *Contact) {
				if err := tools.store.SetHAPersonEntity(c.ID, "person.erin"); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := newTestTools(t)
			c := seedContactAt(t, tools.store, "Erin Custody", tc.zone)
			if tc.setup != nil {
				tc.setup(t, tools, c)
			}
			_, err := tools.ForgetContact(`{"name":"Erin Custody"}`)
			requireContains(t, err,
				fmt.Sprintf("contact_forget refused Erin Custody (%s, %s)", tc.zone, c.ID),
				tc.wantReason, "operator-custodied", "Nothing was removed", "DELETE /v1/contacts/"+c.ID.String())
			if _, err := tools.store.Get(c.ID); err != nil {
				t.Errorf("refused forget removed the record: %v", err)
			}
		})
	}

	t.Run("a plain known contact is forgotten and named", func(t *testing.T) {
		tools := newTestTools(t)
		c := seedContactAt(t, tools.store, "Grace Known", ZoneKnown)
		result, err := tools.ForgetContact(`{"name":"Grace Known"}`)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("Forgot contact: Grace Known (known, %s)", c.ID); result != want {
			t.Errorf("result = %q, want %q", result, want)
		}
		if _, err := tools.store.Get(c.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("forgotten contact still active: %v", err)
		}
	})

	t.Run("forgetting by nickname names the resolved record", func(t *testing.T) {
		tools := newTestTools(t)
		c, err := tools.store.Upsert(&Contact{FormattedName: "Henry Ford", Nickname: "Hank", Kind: "individual"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := tools.ForgetContact(`{"name":"Hank"}`)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("Forgot contact: Henry Ford (known, %s)", c.ID); result != want {
			t.Errorf("result = %q, want %q", result, want)
		}
	})
}

func importText(t *testing.T, tools *Tools, vcf string, extra map[string]any) string {
	t.Helper()
	args := map[string]any{"text": vcf}
	for k, v := range extra {
		args[k] = v
	}
	out, err := tools.ImportVCF(custodyArgs(t, args))
	if err != nil {
		t.Fatalf("ImportVCF: %v", err)
	}
	return out
}

// TestImportVCF_IdentityCustody pins the import path: identity values
// the target rule or holder rule refuses are dropped and counted, the
// rest of the card still imports, and the rows carry provenance.
func TestImportVCF_IdentityCustody(t *testing.T) {
	t.Run("merge into a trusted record drops its new identity", func(t *testing.T) {
		tools := newTestTools(t)
		target := seedContactAt(t, tools.store, "Tess Trusted", ZoneTrusted, Property{Property: "EMAIL", Value: "tess@example.com"})
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Tess Imported\r\nEMAIL:tess@example.com\r\nEMAIL:tess.second@example.com\r\nTEL:+15550001111\r\nORG:Acme\r\nEND:VCARD\r\n"

		dry := importText(t, tools, vcf, map[string]any{"dry_run": true})
		if !strings.Contains(dry, "Would merge") || !strings.Contains(dry, "2 address(es)/number(s) were not imported") {
			t.Errorf("dry run = %q", dry)
		}
		if got, _ := tools.store.GetWithProperties(target.ID); got.Org != "" || len(got.Properties) != 1 {
			t.Errorf("dry run wrote: org %q props %+v", got.Org, got.Properties)
		}

		out := importText(t, tools, vcf, nil)
		if !strings.Contains(out, "1 merged") || !strings.Contains(out, "2 address(es)/number(s) were not imported") || !strings.Contains(out, "CardDAV") {
			t.Errorf("import = %q", out)
		}
		got, err := tools.store.GetWithProperties(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Org != "Acme" {
			t.Errorf("ORG = %q, the rest of the card must still merge", got.Org)
		}
		if len(got.Properties) != 1 || got.Properties[0].Value != "tess@example.com" {
			t.Errorf("trusted record identity = %+v", got.Properties)
		}
	})

	t.Run("a new record never takes an authority holder's value", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin, Property{Property: "EMAIL", Value: "ada@example.com"})
		provenance := &PropertyProvenance{Source: "contact_import_vcf", RequestID: "r_import", LoopID: "loop-1"}
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada Clone\r\nEMAIL:ada@example.com\r\nTEL:+15550002222\r\nEND:VCARD\r\n"
		out, err := tools.ImportVCFFromModel(context.Background(), custodyArgs(t, map[string]any{"text": vcf, "merge": false}), provenance)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "1 created") || !strings.Contains(out, "1 address(es)/number(s) were not imported") {
			t.Errorf("import = %q", out)
		}
		clone, err := tools.store.FindByName("Ada Clone")
		if err != nil {
			t.Fatal(err)
		}
		props, err := tools.store.GetProperties(clone.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(props) != 1 || props[0].Property != "TEL" || !reflect.DeepEqual(props[0].Provenance, provenance) {
			t.Errorf("clone properties = %+v", props)
		}
		matches, err := tools.store.FindAllByPropertyExact(context.Background(), "EMAIL", "ada@example.com")
		if err != nil || len(matches) != 1 {
			t.Errorf("ada@ holders = %+v, %v", matches, err)
		}
	})

	t.Run("malformed names are dropped by grammar", func(t *testing.T) {
		tools := newTestTools(t)
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Nested Group\r\na.b.EMAIL:nested@example.com\r\nEND:VCARD\r\n", map[string]any{"merge": false})
		if !strings.Contains(out, "1 propert(ies) were not imported: their names are not plain vCard property names") {
			t.Errorf("import = %q", out)
		}
		c, err := tools.store.FindByName("Nested Group")
		if err != nil {
			t.Fatal(err)
		}
		if props, _ := tools.store.GetProperties(c.ID); len(props) != 0 {
			t.Errorf("grouped name stored: %+v", props)
		}
	})

	t.Run("legacy entry point stamps the import source", func(t *testing.T) {
		tools := newTestTools(t)
		importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Legacy Import\r\nTEL:+15550005555\r\nEND:VCARD\r\n", nil)
		c, err := tools.store.FindByName("Legacy Import")
		if err != nil {
			t.Fatal(err)
		}
		props, err := tools.store.GetProperties(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(props) != 1 || props[0].Provenance == nil || props[0].Provenance.Source != "contact_import_vcf" {
			t.Errorf("legacy import provenance = %+v", props)
		}
	})

	t.Run("a failed property write is counted", func(t *testing.T) {
		tools := newTestTools(t)
		if _, err := tools.store.db.Exec(`
			CREATE TRIGGER fail_import_property
			BEFORE INSERT ON contact_properties
			WHEN NEW.property = 'X-FAIL'
			BEGIN
				SELECT RAISE(ABORT, 'forced import failure');
			END
		`); err != nil {
			t.Fatal(err)
		}
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Write Failure\r\nX-FAIL:boom\r\nTEL:+15550006666\r\nEND:VCARD\r\n", nil)
		if !strings.Contains(out, "1 propert(ies) failed to write") {
			t.Errorf("import = %q", out)
		}
		c, err := tools.store.FindByName("Write Failure")
		if err != nil {
			t.Fatal(err)
		}
		if props, _ := tools.store.GetProperties(c.ID); len(props) != 1 || props[0].Property != "TEL" {
			t.Errorf("properties after a failed write = %+v", props)
		}
	})

	t.Run("merge into a plain known record adds its identity", func(t *testing.T) {
		tools := newTestTools(t)
		target := seedContactAt(t, tools.store, "Kim Known", ZoneKnown, Property{Property: "EMAIL", Value: "kim@example.com"})
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Kim Known\r\nEMAIL:kim@example.com\r\nTEL:+15550003333\r\nEND:VCARD\r\n", nil)
		if strings.Contains(out, "were not imported") {
			t.Errorf("known merge dropped identity: %q", out)
		}
		props, err := tools.store.GetPropertiesMap(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(props["TEL"], []string{"+15550003333"}) {
			t.Errorf("known merge properties = %v", props)
		}
	})
}

// TestEmittablePropertyName pins the emission guard: rows whose names
// are vCard syntax or a codec-owned header never reach a vCard.
func TestEmittablePropertyName(t *testing.T) {
	for name, want := range map[string]bool{
		"EMAIL":                     true,
		"ha_companion_app":          true,
		"home phone":                true,
		"X-THANE-ORIGIN-TAG":        true,
		"item1.EMAIL":               false,
		"EMAIL;TYPE=work":           false,
		"NOTE:x":                    false,
		"a\r\nEMAIL":                false,
		"a\vEMAIL":                  false,
		"a\fEMAIL":                  false,
		"a\x00EMAIL":                false,
		"a\x1eEMAIL":                false,
		"a\u0085EMAIL":              false,
		"a\u2028EMAIL":              false,
		"a\u2029EMAIL":              false,
		"a\tEMAIL":                  false,
		"X-THANE-TRUST-ZONE":        false,
		"x-thane-ha-person":         false,
		"X-THANE-TRUST-ZONE;PREF=1": false,
		"FN":                        false,
		"":                          false,
	} {
		if got := EmittablePropertyName(name); got != want {
			t.Errorf("EmittablePropertyName(%q) = %v, want %v", name, got, want)
		}
	}

	card := ContactToCard(&Contact{
		FormattedName: "Bob Household",
		TrustZone:     ZoneHousehold,
		Properties: []Property{
			{Property: "EMAIL", Value: "bob@example.com"},
			{Property: "item1.EMAIL", Value: "mallory@example.net"},
			{Property: "X-THANE-TRUST-ZONE", Value: "admin"},
			{Property: "X-LEGACY\u2028EMAIL", Value: "mallory@example.net"},
			{Property: "X-LEGACY\vEMAIL", Value: "mallory@example.net"},
		},
	})
	if got := card.Values(vcard.FieldEmail); !reflect.DeepEqual(got, []string{"bob@example.com"}) {
		t.Errorf("EMAIL = %v", got)
	}
	for name := range card {
		if strings.IndexFunc(name, isLineBreakOrControl) >= 0 {
			t.Errorf("a row named %q, which a client can read as a new line, was emitted", name)
		}
	}
	var encoded bytes.Buffer
	if err := vcard.NewEncoder(&encoded).Encode(card); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded.String(), "mallory@example.net") {
		t.Errorf("a planted row reached the wire:\n%s", encoded.String())
	}
	if got := card.Values("X-THANE-TRUST-ZONE"); !reflect.DeepEqual(got, []string{ZoneHousehold}) {
		t.Errorf("X-THANE-TRUST-ZONE = %v", got)
	}
	if _, ok := card["item1.EMAIL"]; ok {
		t.Error("a grouped row was emitted")
	}
}

// TestIdentityPropertyFor pins key canonicalization.
func TestIdentityPropertyFor(t *testing.T) {
	for key, want := range map[string]string{
		"email": "EMAIL", "Email": "EMAIL", "EMAIL": "EMAIL",
		"phone": "TEL", "Phone": "TEL", "tel": "TEL", "TEL": "TEL",
		"signal": "IMPP", "matrix": "IMPP", "IMPP": "IMPP", "impp": "IMPP",
		"timezone": "", "ha_companion_app": "",
	} {
		got, ok := identityPropertyFor(key)
		if got != want || ok != (want != "") {
			t.Errorf("identityPropertyFor(%q) = %q, %v; want %q", key, got, ok, want)
		}
	}
}

// TestDeleteIfUncustodied pins contact_forget's in-flight guard: the
// delete lands only while the record is still active, known and bound
// to no Home Assistant person, so an operator promotion or binding that
// lands between the forget's check and its write is never undone.
func TestDeleteIfUncustodied(t *testing.T) {
	cases := []struct {
		name       string
		change     func(*testing.T, *Store, *Contact)
		wantDelete bool
		wantZone   string
	}{
		{name: "known and unbound", wantDelete: true},
		{name: "promoted to household", wantZone: ZoneHousehold, change: func(t *testing.T, s *Store, c *Contact) {
			c.TrustZone = ZoneHousehold
			if _, err := s.Upsert(c); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "bound to a Home Assistant person", wantZone: ZoneKnown, change: func(t *testing.T, s *Store, c *Contact) {
			if err := s.SetHAPersonEntity(c.ID, "person.erin"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "already deleted", change: func(t *testing.T, s *Store, c *Contact) {
			if err := s.Delete(c.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			c := seedContactAt(t, store, "Erin Inflight", ZoneKnown)
			if tc.change != nil {
				tc.change(t, store, c)
			}
			deleted, err := store.deleteIfUncustodied(context.Background(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != tc.wantDelete {
				t.Fatalf("deleted = %v, want %v", deleted, tc.wantDelete)
			}
			got, err := store.Get(c.ID)
			if tc.wantZone == "" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("record still active: %+v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a custodied record was deleted: %v", err)
			}
			if got.TrustZone != tc.wantZone {
				t.Errorf("zone = %q, want %q", got.TrustZone, tc.wantZone)
			}
		})
	}
}

// TestForgetContact_RecoveryFitsTheReason pins that a refused forget
// offers only the remedies that clear it: demotion for a zone above
// known, unbinding for a Home Assistant person, and deletion alone for
// the operator's own contact.
func TestForgetContact_RecoveryFitsTheReason(t *testing.T) {
	bind := func(t *testing.T, tools *Tools, c *Contact) {
		if err := tools.store.SetHAPersonEntity(c.ID, "person.erin"); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		zone    string
		setup   func(*testing.T, *Tools, *Contact)
		want    []string
		notWant []string
	}{
		{name: "household", zone: ZoneHousehold, want: []string{"or demote it to known"}, notWant: []string{"binding"}},
		{name: "bound known", zone: ZoneKnown, setup: bind, want: []string{"or remove its Home Assistant person binding"}, notWant: []string{"demote"}},
		{name: "bound household", zone: ZoneHousehold, setup: bind, want: []string{"or demote it to known and remove its Home Assistant person binding"}},
		{name: "operator", zone: ZoneKnown, setup: func(_ *testing.T, tools *Tools, c *Contact) { tools.ConfigureOperatorContactID(c.ID) }, notWant: []string{"demote", "binding"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := newTestTools(t)
			c := seedContactAt(t, tools.store, "Erin Custody", tc.zone)
			if tc.setup != nil {
				tc.setup(t, tools, c)
			}
			_, err := tools.ForgetContact(`{"name":"Erin Custody"}`)
			requireContains(t, err, append([]string{"Ask the operator to delete it through CardDAV or DELETE /v1/contacts/" + c.ID.String()}, tc.want...)...)
			for _, unwanted := range tc.notWant {
				if err != nil && strings.Contains(err.Error(), unwanted) {
					t.Errorf("recovery offers %q, which does not clear this refusal: %v", unwanted, err)
				}
			}
		})
	}
}

// TestLegacyOwnerName_NoSecondRecordClaimsIt pins the legacy selector.
// With only an owner name configured, the operator is whichever record
// ResolveContact finds for it, exact name first and then nickname, so no
// model writer may create a record or set a nickname that would win that
// lookup, in any turn, and custody keeps protecting the real operator.
func TestLegacyOwnerName_NoSecondRecordClaimsIt(t *testing.T) {
	setup := func(t *testing.T) (*Tools, *int, *Contact) {
		t.Helper()
		tools, calls := newCountingTools(t)
		operator, err := tools.store.UpsertWithProperties(&Contact{FormattedName: "Alice Operator", Nickname: "Alice", Kind: "individual", TrustZone: ZoneKnown}, nil)
		if err != nil {
			t.Fatal(err)
		}
		tools.SetOwnerContactName("Alice")
		return tools, calls, operator
	}

	t.Run("creating a contact under the owner name is refused", func(t *testing.T) {
		tools, calls, _ := setup(t)
		for _, args := range []string{
			`{"name":"alice","facts":{"email":"mallory@example.net"}}`,
			`{"name":"Mallory","nickname":"ALICE","facts":{"email":"mallory@example.net"}}`,
		} {
			for _, attended := range []bool{false, true} {
				_, err := modelSave(tools, args, attended)
				requireContains(t, err, "refused to create", "the name Thane recognizes the operator by", "Nothing was saved", "contact_owner")
			}
		}
		for _, name := range []string{"alice", "Mallory"} {
			if _, err := tools.store.FindByName(name); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("a refused create left %q behind: %v", name, err)
			}
		}
		_, err := modelSave(tools, `{"name":"Alice Operator","facts":{"email":"mallory@example.net"}}`, false)
		requireContains(t, err, "Alice Operator is the operator's own contact")
		if *calls != 0 {
			t.Errorf("mutations = %d, want none", *calls)
		}
	})

	t.Run("giving another contact the owner name as nickname is refused", func(t *testing.T) {
		tools, _, operator := setup(t)
		bob := seedContactAt(t, tools.store, "Bob Known", ZoneKnown)
		_, err := modelSave(tools, `{"name":"Bob Known","nickname":"Alice"}`, true)
		requireContains(t, err, `refused nickname "Alice" for Bob Known`, "Nothing was saved")
		if got, err := tools.store.Get(bob.ID); err != nil || got.Nickname != "" {
			t.Errorf("bob after refusal = %+v, %v", got, err)
		}
		if _, err := modelSave(tools, `{"name":"Alice Operator","nickname":"alice","note":"still the operator"}`, false); err != nil {
			t.Errorf("the operator's own record keeps its nickname: %v", err)
		}
		if got, err := tools.store.Get(operator.ID); err != nil || got.Note != "still the operator" {
			t.Errorf("operator after save = %+v, %v", got, err)
		}
	})

	// Until #1566 the UUID selector freed the name; the name-holder rule
	// now keeps the operator's nickname the operator's own there too.
	t.Run("a configured operator_contact_id still keeps the operator's nickname its own", func(t *testing.T) {
		tools, _, operator := setup(t)
		tools.ConfigureOperatorContactID(operator.ID)
		_, err := modelSave(tools, `{"name":"Alice","kind":"individual"}`, false)
		requireContains(t, err, `Alice Operator already goes by "Alice"`, "the operator's own contact", "nothing was saved")
		if _, err := tools.store.FindByName("Alice"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("a refused create left Alice behind: %v", err)
		}
		// The negative control proves the refusal does the work: the store
		// takes the same record through the operator path. Since #1545
		// that record no longer wins the name notifications and decision
		// requests resolve through; the pinned operator's nickname does.
		alice, err := tools.store.UpsertWithProperties(&Contact{FormattedName: "Alice", Kind: "individual", TrustZone: ZoneKnown}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := tools.store.FindByName("Alice"); err != nil || got.ID != alice.ID {
			t.Errorf("FindByName(Alice) = %+v, %v, want the operator-path record", got, err)
		}
		if got, err := tools.store.ResolveContact("Alice"); err != nil || got.ID != operator.ID {
			t.Errorf("ResolveContact(Alice) = %+v, %v, want the pinned operator", got, err)
		}
	})

	t.Run("import leaves an owner-name card and nickname out", func(t *testing.T) {
		tools, _, _ := setup(t)
		bob := seedContactAt(t, tools.store, "Bob Known", ZoneKnown)
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Alice\r\nEMAIL:mallory@example.net\r\nEND:VCARD\r\n" +
			"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob Known\r\nNICKNAME:Alice\r\nORG:Acme\r\nEND:VCARD\r\n"
		dry := importText(t, tools, vcf, map[string]any{"dry_run": true})
		if !strings.Contains(dry, "0 would be created, 1 would be merged") || !strings.Contains(dry, "2 name(s) were not imported") {
			t.Errorf("dry run = %q", dry)
		}
		out := importText(t, tools, vcf, nil)
		if !strings.Contains(out, "0 created, 1 merged") || !strings.Contains(out, "2 name(s) were not imported") {
			t.Errorf("import = %q", out)
		}
		if _, err := tools.store.FindByName("Alice"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("an owner-name card was created: %v", err)
		}
		if matches, err := tools.store.FindByPropertyExact("EMAIL", "mallory@example.net"); err != nil || len(matches) != 0 {
			t.Errorf("mallory@ holders = %+v, %v", matches, err)
		}
		got, err := tools.store.Get(bob.ID)
		if err != nil || got.Nickname != "" || got.Org != "Acme" {
			t.Errorf("bob after merge = %+v, %v", got, err)
		}
	})

	t.Run("import counts an owner-name nickname it leaves off a merge", func(t *testing.T) {
		tools, _, _ := setup(t)
		bob := seedContactAt(t, tools.store, "Bob Known", ZoneKnown)
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob Known\r\nNICKNAME:Alice\r\nORG:Acme\r\nEND:VCARD\r\n"
		for _, extra := range []map[string]any{{"dry_run": true}, nil} {
			if out := importText(t, tools, vcf, extra); !strings.Contains(out, "1 name(s) were not imported") {
				t.Errorf("import %v = %q, want the dropped nickname counted", extra, out)
			}
		}
		got, err := tools.store.Get(bob.ID)
		if err != nil || got.Nickname != "" || got.Org != "Acme" {
			t.Errorf("bob after merge = %+v, %v", got, err)
		}
	})
}

// TestLegacyOperatorPin pins that custody follows the pinned legacy
// operator, the record the channel resolver cached at startup, even
// after an operator write makes the owner name resolve elsewhere.
func TestLegacyOperatorPin(t *testing.T) {
	tools, _ := newCountingTools(t)
	operator, err := tools.store.UpsertWithProperties(&Contact{FormattedName: "Alice Operator", Nickname: "Alice", Kind: "individual", TrustZone: ZoneKnown}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools.SetOwnerContactName("Alice")
	tools.ConfigureLegacyOperatorContactID(operator.ID)
	seedContactAt(t, tools.store, "Alice", ZoneKnown)

	_, err = modelSave(tools, `{"name":"Alice Operator","facts":{"phone":"+15559990000"}}`, false)
	requireContains(t, err, "Alice Operator is the operator's own contact")
	if _, err := modelSave(tools, `{"name":"Alice","facts":{"phone":"+15559990001"}}`, false); err != nil {
		t.Errorf("the unpinned exact-name record is not the operator: %v", err)
	}
	// contact_owner is where the custody refusal sends the model, so it
	// must name the same pinned record.
	if out, err := tools.OwnerContact(""); err != nil || !strings.Contains(out, "Alice Operator") {
		t.Errorf("contact_owner = %q, %v, want the pinned Alice Operator", out, err)
	}

	t.Run("a pin to no record finds no operator", func(t *testing.T) {
		tools := newTestTools(t)
		tools.SetOwnerContactName("Alice")
		tools.ConfigureLegacyOperatorContactID(uuid.Nil)
		seedContactAt(t, tools.store, "Alice", ZoneKnown)
		if out, err := tools.OwnerContact(""); err == nil || !strings.Contains(err.Error(), `"Alice" not found`) {
			t.Errorf("contact_owner = %q, %v, want not found", out, err)
		}
	})
}

// TestSaveContact_RefusalNamesEveryAliasKey pins that two fact keys
// producing the same row are each named in the refusal, so retrying
// without the refused facts, as the refusal teaches, saves.
func TestSaveContact_RefusalNamesEveryAliasKey(t *testing.T) {
	tools := newTestTools(t)
	seedContactAt(t, tools.store, "Hana Household", ZoneHousehold)
	_, err := modelSave(tools, `{"name":"Hana Household","facts":{"email":"hana@example.com","EMAIL":"hana@example.com","timezone":"UTC"}}`, false)
	requireContains(t, err, "refused 2 fact(s)", `"EMAIL"="hana@example.com"`, `"email"="hana@example.com"`)
	if _, err := modelSave(tools, `{"name":"Hana Household","facts":{"timezone":"UTC"}}`, false); err != nil {
		t.Errorf("retry without the refused facts = %v", err)
	}
}

// TestSaveContact_RefusesControlCharactersInArguments pins that no
// contact_save argument carries a line break the vCard encoder would
// emit raw, and that a refusal names every offender and saves nothing.
func TestSaveContact_RefusesControlCharactersInArguments(t *testing.T) {
	tools := newTestTools(t)
	bob := seedContactAt(t, tools.store, "Bob Household", ZoneHousehold)
	_, err := tools.SaveContact(custodyArgs(t, map[string]any{
		"name":        "Bob Household",
		"note":        "hi\rEMAIL:mallory@example.net",
		"title":       "Chief\nEMAIL:mallory@example.net",
		"ai_summary":  "summary\u2028TEL:+15550000000",
		"origin_tags": []string{"ok", "bad\r"},
	}))
	requireContains(t, err, "refused 4 argument value(s); nothing was saved", "note cannot be saved", "title cannot be saved", "ai_summary cannot be saved", "origin_tags cannot be saved")
	if got, err := tools.store.Get(bob.ID); err != nil || got.Note != "" || got.Title != "" {
		t.Errorf("a refused save wrote: %+v, %v", got, err)
	}

	_, err = tools.SaveContact(custodyArgs(t, map[string]any{"name": "Mallory\rEMAIL:mallory@example.net", "kind": "individual"}))
	requireContains(t, err, "name cannot be saved")

	if _, err := tools.SaveContact(custodyArgs(t, map[string]any{"name": "Bob Household", "note": "line one\nline two\tindented"})); err != nil {
		t.Errorf("plain line breaks and tabs in a note must pass: %v", err)
	}
}

// TestSaveContact_RefusesCodecOwnedFactKeys pins that a fact cannot
// shadow a field the contact record owns, which ContactToCard withholds
// and the operator's next CardDAV PUT would delete.
func TestSaveContact_RefusesCodecOwnedFactKeys(t *testing.T) {
	tools := newTestTools(t)
	_, err := tools.SaveContact(custodyArgs(t, map[string]any{
		"name": "Codec Holder",
		"kind": "individual",
		"facts": map[string]string{
			"note":             "x",
			"Title":            "y",
			"BDAY":             "2000-01-01",
			"fn":               "Someone Else",
			"ha_companion_app": "mobile_app_x",
		},
	}))
	requireContains(t, err, "refused 4 fact(s); nothing was saved",
		`"note"`, "the note argument", `"Title"`, "the title argument", `"fn"`, "the name argument", `"BDAY"`, "ask the operator")
	if _, err := tools.store.FindByName("Codec Holder"); !errors.Is(err, sql.ErrNoRows) {
		t.Error("a refused save must not create the contact")
	}

	_, err = tools.SaveContact(`{"name":"Codec Holder","kind":"individual","gender":"F"}`)
	requireContains(t, err, `"gender"`, "field of the contact record itself")
}

// TestContactToCardScrubsUnescapedLineBreaks pins the emission backstop:
// go-vcard escapes only LF, so a CR stored in any value would reach the
// wire raw and a client that reads it as a line ending would decode the
// rest as a new property. The encoded card carries CR only in its line
// terminators and no planted line.
func TestContactToCardScrubsUnescapedLineBreaks(t *testing.T) {
	text, err := EncodeVCard(&Contact{
		FormattedName: "Bob\rX-THANE-TRUST-ZONE:admin",
		Kind:          "individual",
		TrustZone:     ZoneHousehold,
		Note:          "hi\rEMAIL:mallory@example.net",
		Nickname:      "b\u2028TEL:+15550000000",
		Properties: []Property{
			{Property: "timezone", Value: "America/Chicago\r\nEMAIL:mallory@example.net"},
			{Property: "TEL", Value: "+15551110000", Label: "home\rX-THANE-HA-PERSON:person.mallory"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			if i+1 >= len(text) || text[i+1] != '\n' {
				t.Fatalf("bare CR at byte %d:\n%q", i, text)
			}
		case '\n':
			if i == 0 || text[i-1] != '\r' {
				t.Fatalf("bare LF at byte %d:\n%q", i, text)
			}
		}
	}
	if strings.ContainsRune(text, '\u2028') {
		t.Errorf("a line separator reached the wire:\n%q", text)
	}
	for _, line := range strings.Split(text, "\r\n") {
		for _, planted := range []string{"EMAIL:", "TEL:+15550000000", "X-THANE-TRUST-ZONE:admin", "X-THANE-HA-PERSON"} {
			if strings.HasPrefix(line, planted) {
				t.Errorf("planted line %q reached the wire:\n%q", line, text)
			}
		}
	}
}

// TestImportVCF_ValuesAndGroups pins what the import keeps: a value
// carrying a control character is dropped and counted, and a single
// group such as item1.EMAIL imports as EMAIL, which the talent and
// description promise.
func TestImportVCF_ValuesAndGroups(t *testing.T) {
	t.Run("control characters in values are dropped", func(t *testing.T) {
		tools := newTestTools(t)
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Carriage Return\r\nNOTE:hi\rEMAIL:mallory@example.net\r\nX-CUSTOM:ok\rX-THANE-TRUST-ZONE:admin\r\nTEL:+15550007777\r\nEND:VCARD\r\n", map[string]any{"merge": false})
		if !strings.Contains(out, "2 value(s) were not imported") {
			t.Errorf("import = %q", out)
		}
		c, err := tools.store.FindByName("Carriage Return")
		if err != nil {
			t.Fatal(err)
		}
		full, err := tools.store.GetWithProperties(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if full.Note != "" || len(full.Properties) != 1 || full.Properties[0].Property != "TEL" {
			t.Errorf("imported note %q properties %+v", full.Note, full.Properties)
		}
	})

	t.Run("a single group imports under its property", func(t *testing.T) {
		tools := newTestTools(t)
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Grouped Address\r\nitem1.EMAIL:grouped@example.com\r\nEND:VCARD\r\n", map[string]any{"merge": false})
		if strings.Contains(out, "were not imported") {
			t.Errorf("import = %q", out)
		}
		if matches, err := tools.store.FindByPropertyExact("EMAIL", "grouped@example.com"); err != nil || len(matches) != 1 {
			t.Errorf("grouped@ holders = %+v, %v", matches, err)
		}
	})
}

// captureDefaultLog routes the default logger into a buffer for one
// test. No test in this package runs in parallel.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestIdentityCustodyRefusalLogs pins the forensic line each model-facing
// writer logs on a custody refusal, keyed to the turn that asked for it.
func TestIdentityCustodyRefusalLogs(t *testing.T) {
	requireLogged := func(t *testing.T, logs *bytes.Buffer, wants ...string) {
		t.Helper()
		out := logs.String()
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("log lacks %q:\n%s", want, out)
			}
		}
	}

	t.Run("contact_save", func(t *testing.T) {
		tools := newTestTools(t)
		carol := seedContactAt(t, tools.store, "Carol Holder", ZoneTrusted, Property{Property: "EMAIL", Value: "carol@example.com"})
		dan := seedContactAt(t, tools.store, "Dan Known", ZoneKnown)
		logs := captureDefaultLog(t)
		_, err := modelSave(tools, `{"name":"Dan Known","facts":{"email":"carol@example.com"}}`, false)
		requireContains(t, err, "already held by Carol Holder")
		requireLogged(t, logs, "contact identity custody refused", "tool=contact_save", "contact_id="+dan.ID.String(),
			"rule=holder", "properties=EMAIL", "holder_id="+carol.ID.String(), "dry_run=false", "request_id=r_custody")
	})

	t.Run("dry-run import", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin, Property{Property: "EMAIL", Value: "ada@example.com"})
		logs := captureDefaultLog(t)
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada Clone\r\nEMAIL:ada@example.com\r\nEND:VCARD\r\n"
		provenance := &PropertyProvenance{Source: "contact_import_vcf", RequestID: "r_import"}
		if _, err := tools.ImportVCFFromModel(context.Background(), custodyArgs(t, map[string]any{"text": vcf, "merge": false, "dry_run": true}), provenance); err != nil {
			t.Fatal(err)
		}
		requireLogged(t, logs, "tool=contact_import_vcf", "rule=holder", "dry_run=true", "request_id=r_import")
	})

	t.Run("contact_forget", func(t *testing.T) {
		tools := newTestTools(t)
		hal := seedContactAt(t, tools.store, "Hal Household", ZoneHousehold)
		logs := captureDefaultLog(t)
		provenance := &PropertyProvenance{Source: "contact_forget", RequestID: "r_forget", ConversationID: "conv-1", LoopID: "loop-1"}
		_, err := tools.ForgetContactFromModel(context.Background(), `{"name":"Hal Household"}`, provenance)
		requireContains(t, err, "Nothing was removed")
		requireLogged(t, logs, "tool=contact_forget", "contact_id="+hal.ID.String(), "rule=zone",
			"request_id=r_forget", "conversation_id=conv-1", "loop_id=loop-1")
	})

	t.Run("routing fact", func(t *testing.T) {
		tools := newTestTools(t)
		bob := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold)
		logs := captureDefaultLog(t)
		_, err := modelSave(tools, `{"name":"Bob Smith","facts":{"HA_COMPANION_APP":"mobile_app_mallory"}}`, false)
		requireContains(t, err, "Bob Smith is household")
		requireLogged(t, logs, "tool=contact_save", "contact_id="+bob.ID.String(), "rule=zone", "properties=ha_companion_app", "request_id=r_custody")
	})

	t.Run("name claim", func(t *testing.T) {
		tools := newTestTools(t)
		jane := seedNicknameAt(t, tools.store, "Jane Doe", "Mom", ZoneHousehold)
		logs := captureDefaultLog(t)
		_, err := modelSave(tools, `{"name":"Mom","kind":"individual"}`, true)
		requireContains(t, err, "Jane Doe already goes by")
		requireLogged(t, logs, "tool=contact_save", `contact_id=""`, "rule=holder", "properties=FN", "holder_id="+jane.ID.String())
	})
}

// TestCustodyRefusalsStayBounded pins that a contact_save refusal stays
// inside the tool-result budget however many facts or values arrive and
// however long they are: each caller-supplied key and value is quoted
// clipped, and the list stops at its budget and counts the rest.
func TestCustodyRefusalsStayBounded(t *testing.T) {
	const maxRefusalBytes = refusalListMaxBytes + 2<<10
	long := strings.Repeat("\u00e9", 4<<10)

	check := func(t *testing.T, err error, header string, total int) {
		t.Helper()
		requireContains(t, err, header, "nothing was saved", "more refused, not listed here", "retry to see the rest")
		if err == nil {
			return
		}
		msg := err.Error()
		if len(msg) > maxRefusalBytes {
			t.Errorf("refusal is %d bytes, want at most %d", len(msg), maxRefusalBytes)
		}
		if !utf8.ValidString(msg) {
			t.Error("refusal is not valid UTF-8")
		}
		// Every item and the omission marker is one "\n- " line.
		listed := strings.Count(msg, "\n- ") - 1
		var omitted int
		marker := strings.Index(msg, "...and ")
		if marker < 0 {
			t.Fatalf("no omission count in:\n%s", msg)
		}
		if _, err := fmt.Sscanf(msg[marker:], "...and %d more refused", &omitted); err != nil {
			t.Fatalf("parse the omission count: %v", err)
		}
		if listed < 1 || listed+omitted != total {
			t.Errorf("listed %d and counted %d more, want %d in all", listed, omitted, total)
		}
	}

	t.Run("fact key grammar", func(t *testing.T) {
		tools := newTestTools(t)
		facts := map[string]string{}
		for i := range 50 {
			facts[fmt.Sprintf("bad key %d %s", i, long)] = "x"
		}
		_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Bob Known", "facts": facts}), false)
		check(t, err, "contact_save refused 50 fact(s)", 50)
		requireContains(t, err, fmt.Sprintf("(%d bytes)", len("bad key 0 ")+len(long)))
	})

	t.Run("argument values", func(t *testing.T) {
		tools := newTestTools(t)
		tags := make([]string, 0, 500)
		for i := range 500 {
			tags = append(tags, fmt.Sprintf("tag %d\nEMAIL:mallory@example.net", i))
		}
		_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Bob Known", "origin_tags": tags}), false)
		check(t, err, "contact_save refused 500 argument value(s)", 500)
	})

	t.Run("identity custody", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Hana Household", ZoneHousehold)
		facts := map[string]string{}
		// Every case spelling of "email" is its own fact key.
		for mask := range 32 {
			key := []byte("email")
			for i := range key {
				if mask&(1<<i) != 0 {
					key[i] -= 'a' - 'A'
				}
			}
			facts[string(key)] = fmt.Sprintf("%d%s@example.com", mask, long)
		}
		_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Hana Household", "facts": facts}), false)
		check(t, err, "contact_save refused 32 fact(s)", 32)
		requireContains(t, err, "Hana Household is household", "ask the operator to add these")
	})

	t.Run("routing custody", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Hana Household", ZoneHousehold)
		facts := map[string]string{}
		// Every case spelling of the letters in "ha_co" is its own key.
		letters := []int{0, 1, 3, 4, 5}
		for mask := range 32 {
			key := []byte(PropertyHACompanionApp)
			for bit, i := range letters {
				if mask&(1<<bit) != 0 {
					key[i] -= 'a' - 'A'
				}
			}
			facts[string(key)] = fmt.Sprintf("%d%s", mask, long)
		}
		_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Hana Household", "facts": facts}), false)
		check(t, err, "contact_save refused 32 fact(s)", 32)
		requireContains(t, err, "Hana Household is household", "ask the operator to add these")
	})

	t.Run("a long nickname", func(t *testing.T) {
		tools := newTestTools(t)
		seedNicknameAt(t, tools.store, "Hana Household", "Hana", ZoneHousehold)
		nickname := strings.Repeat("é", 5<<10)
		_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Hana Household", "nickname": nickname}), false)
		requireContains(t, err, "contact_save refused 1 change(s)", "nothing was saved", fmt.Sprintf("(%d bytes)", len(nickname)))
		if err != nil && (len(err.Error()) > maxRefusalBytes || !utf8.ValidString(err.Error())) {
			t.Errorf("refusal is %d bytes (valid UTF-8 %v), want at most %d", len(err.Error()), utf8.ValidString(err.Error()), maxRefusalBytes)
		}
	})
}

// TestForgetContact_HonorsContext pins that contact_forget removes
// nothing once the turn's context has ended, down to the guarded delete.
func TestForgetContact_HonorsContext(t *testing.T) {
	tools := newTestTools(t)
	c := seedContactAt(t, tools.store, "Grace Known", ZoneKnown)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := tools.ForgetContactFromModel(ctx, `{"name":"Grace Known"}`, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("forget under an ended context = %v, want context.Canceled", err)
	}
	deleted, err := tools.store.deleteIfUncustodied(ctx, c.ID)
	if deleted || !errors.Is(err, context.Canceled) {
		t.Fatalf("guarded delete under an ended context = %v, %v", deleted, err)
	}
	if _, err := tools.store.Get(c.ID); err != nil {
		t.Errorf("an ended turn removed the contact: %v", err)
	}
}

// cancelOnEmbed ends the import's context the first time a contact is
// embedded, that is right after the first card is written.
type cancelOnEmbed struct{ cancel context.CancelFunc }

func (e cancelOnEmbed) Generate(ctx context.Context, _ string) ([]float32, error) {
	e.cancel()
	return nil, ctx.Err()
}

// TestImportVCF_HonorsContext pins that contact_import_vcf writes
// nothing once the turn's context has ended, and that a turn ending
// mid-import stops it before the next card and reports what it wrote.
func TestImportVCF_HonorsContext(t *testing.T) {
	vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ivy One\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ivy Two\r\nEND:VCARD\r\n"
	args := custodyArgs(t, map[string]any{"text": vcf})

	t.Run("an ended turn imports nothing", func(t *testing.T) {
		tools := newTestTools(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := tools.ImportVCFFromModel(ctx, args, &PropertyProvenance{Source: importVCFSource}); !errors.Is(err, context.Canceled) {
			t.Fatalf("import under an ended context = %v, want context.Canceled", err)
		}
		for _, name := range []string{"Ivy One", "Ivy Two"} {
			if _, err := tools.store.FindByName(name); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%s was imported by an ended turn: %v", name, err)
			}
		}
	})

	t.Run("a turn ending mid-import stops before the next card", func(t *testing.T) {
		tools := newTestTools(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tools.SetEmbeddingClient(cancelOnEmbed{cancel: cancel})
		_, err := tools.ImportVCFFromModel(ctx, args, &PropertyProvenance{Source: importVCFSource})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("import = %v, want context.Canceled", err)
		}
		requireContains(t, err, "stopped before card 2 of 2", "1 contact(s) were created and 0 merged")
		if _, err := tools.store.FindByName("Ivy One"); err != nil {
			t.Errorf("the card written before the turn ended is missing: %v", err)
		}
		if _, err := tools.store.FindByName("Ivy Two"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("a card was written after the turn ended: %v", err)
		}
	})

	t.Run("a turn ending inside a card stops before writing it", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Hana Household", ZoneHousehold, Property{Property: "EMAIL", Value: "hana@example.com"})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		previous := slog.Default()
		slog.SetDefault(slog.New(cancelOnRefusal{cancel: cancel}))
		t.Cleanup(func() { slog.SetDefault(previous) })

		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ivy One\r\nKEY:https://example.com/ivy.asc\r\nEND:VCARD\r\n" +
			"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ivy Two\r\nEMAIL:hana@example.com\r\nEND:VCARD\r\n"
		_, err := tools.ImportVCFFromModel(ctx, custodyArgs(t, map[string]any{"text": vcf, "merge": false}), &PropertyProvenance{Source: importVCFSource})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("import = %v, want context.Canceled", err)
		}
		requireContains(t, err, "stopped before card 2 of 2", "1 contact(s) were created and 0 merged",
			"1 key propert(ies) were not imported", "import the cards from card 2 on")
		if strings.Contains(err.Error(), "address(es)/number(s)") {
			t.Errorf("the stop report counted a drop from the card it did not write:\n%v", err)
		}
		if _, err := tools.store.FindByName("Ivy One"); err != nil {
			t.Errorf("the card written before the turn ended is missing: %v", err)
		}
		if _, err := tools.store.FindByName("Ivy Two"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("the card the turn ended inside was written: %v", err)
		}
	})
}

// cancelOnRefusal ends a context when the import logs a custody
// refusal, that is inside a card, after its reads and before its
// writes.
type cancelOnRefusal struct{ cancel context.CancelFunc }

func (h cancelOnRefusal) Enabled(context.Context, slog.Level) bool { return true }

func (h cancelOnRefusal) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "contact identity custody refused" {
		h.cancel()
	}
	return nil
}

func (h cancelOnRefusal) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h cancelOnRefusal) WithGroup(string) slog.Handler { return h }

// TestImportVCF_StopReportsWhatItWrote pins that an error inside a card
// still tells the model what the cards before it wrote.
func TestImportVCF_StopReportsWhatItWrote(t *testing.T) {
	tools := newTestTools(t)
	// The legacy owner name matches two records, so resolving the
	// operator for the identity check fails closed.
	seedContactAt(t, tools.store, "Alice Smith", ZoneKnown)
	seedContactAt(t, tools.store, "Alice Jones", ZoneKnown)
	tools.SetOwnerContactName("Alice")

	vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ivy One\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ivy Two\r\nEMAIL:ivy@example.com\r\nEND:VCARD\r\n"
	_, err := tools.ImportVCF(custodyArgs(t, map[string]any{"text": vcf, "merge": false}))
	requireContains(t, err, "stopped before card 2 of 2", "check identity custody", "ambiguous contact",
		"1 contact(s) were created and 0 merged", "import the cards from card 2 on")
	if _, err := tools.store.FindByName("Ivy Two"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the card the import stopped at was written: %v", err)
	}
}

// operatorWriteOnRefusal performs one operator write the first time an
// import logs a custody refusal, that is inside a card, after the
// import's own custody check and before the card's write transaction.
type operatorWriteOnRefusal struct {
	done  *bool
	write func()
}

func (h operatorWriteOnRefusal) Enabled(context.Context, slog.Level) bool { return true }

func (h operatorWriteOnRefusal) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "contact identity custody refused" && !*h.done {
		*h.done = true
		h.write()
	}
	return nil
}

func (h operatorWriteOnRefusal) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h operatorWriteOnRefusal) WithGroup(string) slog.Handler { return h }

// onImportRefusal routes the default logger to an operatorWriteOnRefusal
// for one test.
func onImportRefusal(t *testing.T, write func()) {
	t.Helper()
	done := false
	previous := slog.Default()
	slog.SetDefault(slog.New(operatorWriteOnRefusal{done: &done, write: write}))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// TestImportVCF_RechecksCustodyInItsWrite pins that each card's custody
// check and its writes are one transaction: an operator write that
// lands after the import's own check still decides what the card
// writes.
func TestImportVCF_RechecksCustodyInItsWrite(t *testing.T) {
	t.Run("a holder that appears mid-card keeps its value", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin, Property{Property: "EMAIL", Value: "ada@example.com"})
		hal := seedContactAt(t, tools.store, "Hal Household", ZoneHousehold)
		onImportRefusal(t, func() {
			if err := tools.store.AddProperty(hal.ID, &Property{Property: "EMAIL", Value: "bob@example.com"}); err != nil {
				t.Errorf("operator write: %v", err)
			}
		})

		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob New\r\nEMAIL:ada@example.com\r\nEMAIL:bob@example.com\r\nTEL:+15550004444\r\nEND:VCARD\r\n"
		out := importText(t, tools, vcf, map[string]any{"merge": false})
		if !strings.Contains(out, "1 created") || !strings.Contains(out, "2 address(es)/number(s) were not imported") {
			t.Errorf("import = %q, want the raced address counted as a drop", out)
		}
		holders, err := tools.store.FindAllByPropertyExact(context.Background(), "EMAIL", "bob@example.com")
		if err != nil || len(holders) != 1 || holders[0].ID != hal.ID {
			t.Errorf("bob@ holders = %+v, %v, want Hal Household alone", holders, err)
		}
		bob, err := tools.store.FindByName("Bob New")
		if err != nil {
			t.Fatal(err)
		}
		if props, _ := tools.store.GetProperties(bob.ID); len(props) != 1 || props[0].Property != "TEL" {
			t.Errorf("Bob New properties = %+v, want the rest of the card", props)
		}
	})

	t.Run("a merge target promoted mid-card writes nothing", func(t *testing.T) {
		tools := newTestTools(t)
		// Ada holds a number rather than an address, so the card merges
		// into Kim by name, not into Ada by email.
		seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin, Property{Property: "TEL", Value: "+15550009999"})
		kim := seedContactAt(t, tools.store, "Kim Known", ZoneKnown)
		onImportRefusal(t, func() {
			promoted, err := tools.store.Get(kim.ID)
			if err != nil {
				t.Errorf("read Kim: %v", err)
				return
			}
			promoted.TrustZone = ZoneHousehold
			if _, err := tools.store.Upsert(promoted); err != nil {
				t.Errorf("operator promotion: %v", err)
			}
		})

		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Kim Known\r\nTEL:+15550009999\r\nTEL:+15550005555\r\nORG:Acme\r\nEND:VCARD\r\n"
		out := importText(t, tools, vcf, nil)
		if !strings.Contains(out, "0 merged, 1 skipped") || !strings.Contains(out, "1 card(s) were skipped, not merged") || !strings.Contains(out, "card 1.") {
			t.Errorf("import = %q, want card 1 skipped and named with its cause", out)
		}
		got, err := tools.store.GetWithProperties(kim.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.TrustZone != ZoneHousehold || got.Org != "" || len(got.Properties) != 0 {
			t.Errorf("Kim after import = zone %q org %q props %+v, want the promotion kept and nothing written", got.TrustZone, got.Org, got.Properties)
		}
	})

	t.Run("a name an authority contact takes mid-card writes nothing", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin, Property{Property: "EMAIL", Value: "ada@example.com"})
		hal := seedContactAt(t, tools.store, "Hal Household", ZoneHousehold)
		onImportRefusal(t, func() {
			renamed, err := tools.store.Get(hal.ID)
			if err != nil {
				t.Errorf("read Hal: %v", err)
				return
			}
			renamed.Nickname = "Newt"
			if _, err := tools.store.Upsert(renamed); err != nil {
				t.Errorf("operator nickname edit: %v", err)
			}
		})

		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Nora New\r\nNICKNAME:Newt\r\nEMAIL:ada@example.com\r\nTEL:+15550004444\r\nEND:VCARD\r\n"
		out := importText(t, tools, vcf, map[string]any{"merge": false})
		if !strings.Contains(out, "0 created, 0 merged, 1 skipped") || !strings.Contains(out, "1 card(s) were skipped, not merged or created") || !strings.Contains(out, "card 1.") {
			t.Errorf("import = %q, want card 1 skipped and named with its cause", out)
		}
		if _, err := tools.store.FindByName("Nora New"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("the card was written: %v", err)
		}
		if got, err := tools.store.FindByNickname("Newt"); err != nil || got.ID != hal.ID {
			t.Errorf("Newt = %+v, %v, want Hal Household", got, err)
		}
	})
}

// TestImportVCF_NamelessCardDryRunParity pins that a card whose name a
// control character cleared is skipped by the dry run as by the import.
func TestImportVCF_NamelessCardDryRunParity(t *testing.T) {
	tools := newTestTools(t)
	vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Nameless\rX-THANE-TRUST-ZONE:admin\r\nTEL:+15550008888\r\nEND:VCARD\r\n"
	dry := importText(t, tools, vcf, map[string]any{"dry_run": true, "merge": false})
	if !strings.Contains(dry, "0 would be created, 0 would be merged, 1 would be skipped") || !strings.Contains(dry, "Would skip card 1") || strings.Contains(dry, "card(s) were skipped") {
		t.Errorf("dry run = %q, want the nameless card skipped and named once", dry)
	}
	out := importText(t, tools, vcf, map[string]any{"merge": false})
	if !strings.Contains(out, "0 created, 0 merged, 1 skipped") || !strings.Contains(out, "no usable name (FN): card 1.") {
		t.Errorf("import = %q, want card 1 skipped and named with its cause", out)
	}
}
