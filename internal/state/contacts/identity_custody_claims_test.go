package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// seedNicknameAt seeds a contact with a nickname through the operator
// path, the way CardDAV and /v1/contacts write.
func seedNicknameAt(t *testing.T, store *Store, name, nickname, zone string, props ...Property) *Contact {
	t.Helper()
	c, err := store.UpsertWithProperties(&Contact{FormattedName: name, Nickname: nickname, Kind: "individual", TrustZone: zone}, props)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return c
}

// routingValue is a plausible value for each routing key spelling.
func routingValue(key string) string {
	if strings.EqualFold(key, PropertyNotificationPreference) {
		return "signal"
	}
	return "mobile_app_mallory"
}

var routingKeys = []string{"ha_companion_app", "HA_COMPANION_APP", "notification_preference", "Notification_Preference"}

// TestSaveContact_RoutingFactCustody pins the routing target rule: the
// device and channel that carry a contact's notifications are not added
// to a contact above known, or to the operator's own, outside the
// operator's own message, in any case of the key.
func TestSaveContact_RoutingFactCustody(t *testing.T) {
	for _, zone := range []string{ZoneAdmin, ZoneHousehold, ZoneTrusted} {
		for _, key := range routingKeys {
			t.Run(zone+"/"+key, func(t *testing.T) {
				tools, calls := newCountingTools(t)
				target := seedContactAt(t, tools.store, "Bob Smith", zone)
				_, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Bob Smith", "facts": map[string]string{key: routingValue(key)}}), false)
				requireContains(t, err, "operator-custodied", fmt.Sprintf("%q", key), "Bob Smith is "+zone, "nothing was saved", "CardDAV", "in their own message")
				props, err := tools.store.GetProperties(target.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(props) != 0 || *calls != 0 {
					t.Errorf("refused save wrote %+v and signaled %d", props, *calls)
				}
			})
		}
	}

	for _, tc := range []struct {
		name      string
		configure func(*Tools, *Contact)
	}{
		{"operator_contact_id", func(tools *Tools, c *Contact) { tools.ConfigureOperatorContactID(c.ID) }},
		{"legacy owner name", func(tools *Tools, c *Contact) { tools.SetOwnerContactName(c.FormattedName) }},
	} {
		t.Run("operator at known/"+tc.name, func(t *testing.T) {
			tools, calls := newCountingTools(t)
			operator := seedContactAt(t, tools.store, "Alice Operator", ZoneKnown)
			tc.configure(tools, operator)
			_, err := modelSave(tools, `{"name":"Alice Operator","facts":{"ha_companion_app":"mobile_app_mallory"}}`, false)
			requireContains(t, err, "operator-custodied", "Alice Operator is the operator's own contact")
			if props, _ := tools.store.GetProperties(operator.ID); len(props) != 0 || *calls != 0 {
				t.Errorf("refused save wrote %+v and signaled %d", props, *calls)
			}
		})
	}

	t.Run("a known contact takes every key", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		target := seedContactAt(t, tools.store, "Kim Known", ZoneKnown)
		// Distinct values: one value in two spellings of a key is one fact.
		for i, key := range routingKeys {
			if _, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Kim Known", "facts": map[string]string{key: fmt.Sprintf("%s_%d", routingValue(key), i)}}), false); err != nil {
				t.Errorf("%s on a known contact: %v", key, err)
			}
		}
		if props, _ := tools.store.GetProperties(target.ID); len(props) != len(routingKeys) {
			t.Errorf("known contact properties = %+v", props)
		}
	})

	t.Run("the operator's own message lifts it", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		target := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold)
		if _, err := modelSave(tools, `{"name":"Bob Smith","facts":{"ha_companion_app":"mobile_app_bob"}}`, true); err != nil {
			t.Fatalf("attended device save: %v", err)
		}
		got, err := tools.store.GetPropertiesMap(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got["ha_companion_app"], []string{"mobile_app_bob"}) {
			t.Errorf("properties = %v", got)
		}
	})

	t.Run("re-saving a held device in another spelling is a no-op", func(t *testing.T) {
		tools, calls := newCountingTools(t)
		target := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold, Property{Property: "HA_COMPANION_APP", Value: "mobile_app_bob"})
		result, err := modelSave(tools, `{"name":"Bob Smith","facts":{"ha_companion_app":"MOBILE_APP_BOB"}}`, false)
		if err != nil || !strings.Contains(result, "Contact unchanged") {
			t.Fatalf("re-save = %q, %v", result, err)
		}
		if props, _ := tools.store.GetProperties(target.ID); len(props) != 1 || *calls != 0 {
			t.Errorf("no-op wrote %+v and signaled %d", props, *calls)
		}
	})
}

// TestRoutingPropertyFor pins routing key canonicalization. It is a
// sibling class of identityPropertyFor: routing keys are not addresses.
func TestRoutingPropertyFor(t *testing.T) {
	for name, want := range map[string]string{
		"ha_companion_app": PropertyHACompanionApp, "HA_COMPANION_APP": PropertyHACompanionApp, " Ha_Companion_App ": PropertyHACompanionApp,
		"notification_preference": PropertyNotificationPreference, "NOTIFICATION_PREFERENCE": PropertyNotificationPreference,
		"ha-companion-app": "", "ha_companion_apps": "", "email": "", "TEL": "",
	} {
		got, ok := routingPropertyFor(name)
		if got != want || ok != (want != "") {
			t.Errorf("routingPropertyFor(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
}

// TestFactValues pins the order delivery reads a fact in: the exact key
// first, then every other case spelling in sorted order, and nothing
// under a renamed key.
func TestFactValues(t *testing.T) {
	tests := []struct {
		name  string
		props map[string][]string
		want  []string
	}{
		{"exact key only", map[string][]string{"ha_companion_app": {"a", "b"}}, []string{"a", "b"}},
		{"exact key first", map[string][]string{"HA_COMPANION_APP": {"upper"}, "ha_companion_app": {"lower"}}, []string{"lower", "upper"}},
		{"other spellings sorted", map[string][]string{"Ha_Companion_App": {"mixed"}, "HA_COMPANION_APP": {"upper"}}, []string{"upper", "mixed"}},
		{"renamed key ignored", map[string][]string{"ha-companion-app": {"hyphen"}, "ha_companion_apps": {"plural"}}, nil},
		{"missing", map[string][]string{"timezone": {"UTC"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FactValues(tt.props, PropertyHACompanionApp); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FactValues = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("within a spelling the store's pref and insertion order holds", func(t *testing.T) {
		store := newTestStore(t)
		c := seedContactAt(t, store, "Bob Smith", ZoneKnown,
			Property{Property: "Ha_Companion_App", Value: "mixed"},
			Property{Property: "ha_companion_app", Value: "second"},
			Property{Property: "HA_COMPANION_APP", Value: "upper"},
			Property{Property: "ha_companion_app", Value: "preferred", Pref: 1},
			Property{Property: "ha-companion-app", Value: "hyphen"},
		)
		props, err := store.GetPropertiesMap(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := FactValues(props, PropertyHACompanionApp), []string{"preferred", "second", "upper", "mixed"}; !reflect.DeepEqual(got, want) {
			t.Errorf("FactValues = %v, want %v", got, want)
		}
	})
}

// TestSaveContact_RoutingShadowNote pins the success-path teaching: a
// routing fact recorded behind the value delivery uses says so, computed
// the way delivery computes it.
func TestSaveContact_RoutingShadowNote(t *testing.T) {
	first := func(t *testing.T, store *Store, c *Contact, key string) string {
		t.Helper()
		props, err := store.GetPropertiesMap(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		values := FactValues(props, key)
		if len(values) == 0 {
			t.Fatalf("%s has no %s", c.FormattedName, key)
		}
		return values[0]
	}

	t.Run("behind an existing lowercase device", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		bob := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold, Property{Property: "ha_companion_app", Value: "mobile_app_old"})
		result, err := modelSave(tools, `{"name":"Bob Smith","facts":{"ha_companion_app":"mobile_app_new"}}`, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Updated contact", `ha_companion_app "mobile_app_new" was recorded`, `Home Assistant push still goes to "mobile_app_old"`, "CardDAV or the contacts API"} {
			if !strings.Contains(result, want) {
				t.Errorf("result = %q, want %q", result, want)
			}
		}
		if got := first(t, tools.store, bob, PropertyHACompanionApp); got != "mobile_app_old" {
			t.Errorf("delivery uses %q, the note says mobile_app_old", got)
		}
	})

	t.Run("over an upper-case device the new lowercase value wins", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		bob := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold, Property{Property: "HA_COMPANION_APP", Value: "mobile_app_old"})
		result, err := modelSave(tools, `{"name":"Bob Smith","facts":{"ha_companion_app":"mobile_app_new"}}`, true)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "still use") {
			t.Errorf("result = %q, want no shadow note", result)
		}
		if got := first(t, tools.store, bob, PropertyHACompanionApp); got != "mobile_app_new" {
			t.Errorf("delivery uses %q, want mobile_app_new", got)
		}
	})

	t.Run("a first value gets no note", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold)
		result, err := modelSave(tools, `{"name":"Bob Smith","facts":{"ha_companion_app":"mobile_app_bob"}}`, true)
		if err != nil || strings.Contains(result, "still use") {
			t.Errorf("result = %q, %v", result, err)
		}
	})

	t.Run("a known contact's second channel is noted too", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		kim := seedContactAt(t, tools.store, "Kim Known", ZoneKnown, Property{Property: "notification_preference", Value: "signal"})
		result, err := modelSave(tools, `{"name":"Kim Known","facts":{"notification_preference":"ha_push"}}`, false)
		if err != nil || !strings.Contains(result, `notifications still use "signal"`) {
			t.Errorf("result = %q, %v", result, err)
		}
		if got := first(t, tools.store, kim, PropertyNotificationPreference); got != "signal" {
			t.Errorf("delivery uses %q, the note says signal", got)
		}
	})

	t.Run("a value another writer committed first is the one noted", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		bob := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold)
		// The save reads Bob with no device. This trigger stands in for
		// a writer whose lowercase device commits after that read and
		// before the save's own insert, so only the save's transaction
		// sees it.
		if _, err := tools.store.db.Exec(`
			CREATE TRIGGER concurrent_first_device BEFORE INSERT ON contact_properties
			WHEN NEW.value = 'mobile_app_new'
			BEGIN
				INSERT INTO contact_properties (contact_id, property, value, created_at, updated_at)
				VALUES (NEW.contact_id, 'ha_companion_app', 'mobile_app_first', NEW.created_at, NEW.updated_at);
			END`); err != nil {
			t.Fatal(err)
		}
		result, err := modelSave(tools, `{"name":"Bob Smith","facts":{"ha_companion_app":"mobile_app_new"}}`, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := first(t, tools.store, bob, PropertyHACompanionApp); got != "mobile_app_first" {
			t.Fatalf("delivery uses %q, want the concurrent writer's mobile_app_first", got)
		}
		for _, want := range []string{`ha_companion_app "mobile_app_new" was recorded`, `Home Assistant push still goes to "mobile_app_first"`} {
			if !strings.Contains(result, want) {
				t.Errorf("result = %q, want %q", result, want)
			}
		}
	})
}

// TestSaveContact_NicknameCustody pins the nickname target rule: a
// changed nickname on a contact above known, or on the operator's own,
// needs the operator's own message. A case-only edit is not a change,
// compared the way SQLite LOWER compares.
func TestSaveContact_NicknameCustody(t *testing.T) {
	for _, zone := range []string{ZoneHousehold, ZoneTrusted} {
		t.Run(zone, func(t *testing.T) {
			tools, calls := newCountingTools(t)
			bob := seedNicknameAt(t, tools.store, "Bob Smith", "Bobby", zone)
			_, err := modelSave(tools, `{"name":"Bob Smith","nickname":"Robert"}`, false)
			requireContains(t, err, "operator-custodied", `"nickname"="Robert" (NICKNAME)`, "Bob Smith is "+zone, "refused 1 change(s)", "ask the operator to change it")
			if got, _ := tools.store.Get(bob.ID); got.Nickname != "Bobby" || *calls != 0 {
				t.Errorf("refused save left nickname %q and signaled %d", got.Nickname, *calls)
			}
			if _, err := modelSave(tools, `{"name":"Bob Smith","nickname":"Robert"}`, true); err != nil {
				t.Fatalf("attended nickname change: %v", err)
			}
			if got, _ := tools.store.Get(bob.ID); got.Nickname != "Robert" {
				t.Errorf("nickname after attended save = %q", got.Nickname)
			}
		})
	}

	t.Run("a known contact's nickname is open", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		kim := seedNicknameAt(t, tools.store, "Kim Known", "Kimmy", ZoneKnown)
		if _, err := modelSave(tools, `{"name":"Kim Known","nickname":"Kim"}`, false); err != nil {
			t.Fatal(err)
		}
		if got, _ := tools.store.Get(kim.ID); got.Nickname != "Kim" {
			t.Errorf("nickname = %q", got.Nickname)
		}
	})

	t.Run("the operator's own contact at known", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		operator := seedNicknameAt(t, tools.store, "Alice Operator", "nugget", ZoneKnown)
		tools.ConfigureOperatorContactID(operator.ID)
		_, err := modelSave(tools, `{"name":"Alice Operator","nickname":"Ally"}`, false)
		requireContains(t, err, "Alice Operator is the operator's own contact")
		if got, _ := tools.store.Get(operator.ID); got.Nickname != "nugget" {
			t.Errorf("nickname = %q", got.Nickname)
		}
	})

	t.Run("a case-only edit is not a change", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		bob := seedNicknameAt(t, tools.store, "Bob Smith", "Bobby", ZoneHousehold)
		if _, err := modelSave(tools, `{"name":"Bob Smith","nickname":"BOBBY"}`, false); err != nil {
			t.Fatal(err)
		}
		if got, _ := tools.store.Get(bob.ID); got.Nickname != "BOBBY" {
			t.Errorf("nickname = %q", got.Nickname)
		}
	})

	t.Run("a non-ASCII case edit is a change", func(t *testing.T) {
		tools, _ := newCountingTools(t)
		seedNicknameAt(t, tools.store, "Emile Household", "Émile", ZoneHousehold)
		_, err := modelSave(tools, `{"name":"Emile Household","nickname":"émile"}`, false)
		requireContains(t, err, "Emile Household is household")
	})
}

// TestSaveContact_AuthorityNameClaim pins the name-holder rule: in every
// turn, the operator's own included, no contact is created under, or
// given as a nickname, a name an authority contact goes by.
func TestSaveContact_AuthorityNameClaim(t *testing.T) {
	setup := func(t *testing.T) (*Tools, *int, map[string]*Contact) {
		t.Helper()
		tools, calls := newCountingTools(t)
		seeded := map[string]*Contact{
			"alice": seedNicknameAt(t, tools.store, "Alice Operator", "nugget", ZoneKnown),
			"jane":  seedNicknameAt(t, tools.store, "Jane Doe", "Mom", ZoneHousehold),
			"kim":   seedNicknameAt(t, tools.store, "Kim Known", "Kimmy", ZoneKnown),
			"dan":   seedContactAt(t, tools.store, "Dan Known", ZoneKnown),
		}
		tools.ConfigureOperatorContactID(seeded["alice"].ID)
		return tools, calls, seeded
	}
	// A new contact recovers under a fuller name; on an existing one a
	// different name would create a second contact, so it retries
	// without the nickname instead.
	const createRecovery, updateRecovery = "fuller name", "retry without the nickname"
	cases := []struct {
		name, args string
		want       []string
		avoid      string
	}{
		{"create under the operator's nickname", `{"name":"nugget","kind":"individual"}`,
			[]string{`"name"="nugget" (FN)`, `Alice Operator already goes by "nugget" (known`, "the operator's own contact", createRecovery}, updateRecovery},
		{"create under it padded and upper-cased, with facts", `{"name":" NUGGET ","kind":"individual","facts":{"signal":"+15550001234","notification_preference":"signal"}}`,
			[]string{`Alice Operator already goes by "nugget"`, createRecovery}, updateRecovery},
		{"a known contact takes the operator's nickname", `{"name":"Dan Known","nickname":"nugget"}`,
			[]string{`"nickname"="nugget" (NICKNAME)`, `Alice Operator already goes by "nugget"`, updateRecovery}, createRecovery},
		{"a known contact takes a household nickname", `{"name":"Dan Known","nickname":"mom"}`,
			[]string{`Jane Doe already goes by "Mom" (household`, updateRecovery}, createRecovery},
		{"a known contact takes an authority name as nickname", `{"name":"Dan Known","nickname":"jane doe"}`,
			[]string{`Jane Doe already goes by "Jane Doe" (household`, updateRecovery}, createRecovery},
		{"create with a household nickname", `{"name":"Mallory","nickname":"Mom","kind":"individual"}`,
			[]string{`"nickname"="Mom" (NICKNAME)`, `Jane Doe already goes by "Mom"`, createRecovery}, updateRecovery},
	}
	for _, tc := range cases {
		for _, attended := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/attended=%v", tc.name, attended), func(t *testing.T) {
				tools, calls, seeded := setup(t)
				_, err := modelSave(tools, tc.args, attended)
				requireContains(t, err, append([]string{"nothing was saved", "operator-custodied", "stays with that contact"}, tc.want...)...)
				if err != nil && strings.Contains(err.Error(), "not the operator's own message") {
					t.Errorf("a name refusal must not claim the turn is unattended: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), tc.avoid) {
					t.Errorf("the recovery offers %q, which does not fit this save: %v", tc.avoid, err)
				}
				for _, name := range []string{"nugget", " NUGGET ", "Mallory"} {
					if _, err := tools.store.FindByName(name); !errors.Is(err, sql.ErrNoRows) {
						t.Errorf("a refused save created %q: %v", name, err)
					}
				}
				if got, _ := tools.store.Get(seeded["dan"].ID); got.Nickname != "" {
					t.Errorf("Dan Known nickname = %q", got.Nickname)
				}
				if *calls != 0 {
					t.Errorf("refused save signaled %d mutations", *calls)
				}
			})
		}
	}

	t.Run("negative controls", func(t *testing.T) {
		tools, _, seeded := setup(t)
		if _, err := modelSave(tools, `{"name":"Dan Known","nickname":"Kimmy"}`, false); err != nil {
			t.Errorf("a nickname only a known contact goes by must be allowed: %v", err)
		}
		for _, nickname := range []string{"nugget", "NUGGET"} {
			if _, err := modelSave(tools, custodyArgs(t, map[string]any{"name": "Alice Operator", "nickname": nickname}), false); err != nil {
				t.Errorf("the operator re-saving her own nickname %q: %v", nickname, err)
			}
		}
		got, err := tools.store.ResolveContact("nugget")
		if err != nil || got.ID != seeded["alice"].ID {
			t.Errorf("ResolveContact(nugget) = %+v, %v, want Alice Operator", got, err)
		}
	})
}

// TestFindByNickname_AuthorityFirst pins the resolver's order for a
// nickname two active contacts already share: the one with authority
// wins, whichever was written first.
func TestFindByNickname_AuthorityFirst(t *testing.T) {
	for _, householdFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("household first=%v", householdFirst), func(t *testing.T) {
			store := newTestStore(t)
			var household *Contact
			if householdFirst {
				household = seedNicknameAt(t, store, "Hana Household", "sis", ZoneHousehold)
				seedNicknameAt(t, store, "Kim Known", "Sis", ZoneKnown)
			} else {
				seedNicknameAt(t, store, "Kim Known", "Sis", ZoneKnown)
				household = seedNicknameAt(t, store, "Hana Household", "sis", ZoneHousehold)
			}
			for _, lookup := range []func(string) (*Contact, error){store.FindByNickname, store.ResolveContact} {
				got, err := lookup("SIS")
				if err != nil || got.ID != household.ID {
					t.Errorf("lookup = %+v, %v, want Hana Household", got, err)
				}
			}
		})
	}

	t.Run("two known holders resolve by id", func(t *testing.T) {
		store := newTestStore(t)
		a := seedNicknameAt(t, store, "Kim One", "twin", ZoneKnown)
		b := seedNicknameAt(t, store, "Kim Two", "twin", ZoneKnown)
		want := a.ID
		if b.ID.String() < a.ID.String() {
			want = b.ID
		}
		got, err := store.FindByNickname("twin")
		if err != nil || got.ID != want {
			t.Errorf("FindByNickname = %+v, %v, want %s", got, err, want)
		}
	})
}

// TestFindByNickname_OperatorFirst pins the operator's claim on a
// nickname several records already share: the operator's own record
// wins at any zone, over an ordinary known record with a lower id and
// over a household record, under either operator selector. The store
// learns the operator from the tools' own selector, so custody and
// resolution agree on who it is.
func TestFindByNickname_OperatorFirst(t *testing.T) {
	selectors := []struct {
		name string
		pin  func(*Tools, *Contact)
	}{
		{"operator_contact_id", func(tools *Tools, operator *Contact) { tools.ConfigureOperatorContactID(operator.ID) }},
		{"pinned legacy owner", func(tools *Tools, operator *Contact) {
			tools.SetOwnerContactName(operator.FormattedName)
			tools.ConfigureLegacyOperatorContactID(operator.ID)
		}},
	}
	for _, sel := range selectors {
		for _, rival := range []string{ZoneKnown, ZoneHousehold} {
			t.Run(sel.name+"/operator at known over a "+rival+" record", func(t *testing.T) {
				tools := newTestTools(t)
				a := seedNicknameAt(t, tools.store, "Alice Operator", "boss", ZoneKnown)
				b := seedNicknameAt(t, tools.store, "Kim Rival", "Boss", rival)
				operator, other := a, b
				if rival == ZoneKnown && a.ID.String() < b.ID.String() {
					// A known rival must hold the lower id, so id order
					// alone would pick it.
					operator, other = b, a
				}
				if got, err := tools.store.ResolveContact("BOSS"); err != nil || got.ID != other.ID {
					t.Fatalf("control: before the pin ResolveContact = %+v, %v, want %s", got, err, other.FormattedName)
				}
				sel.pin(tools, operator)
				for _, lookup := range []func(string) (*Contact, error){tools.store.FindByNickname, tools.store.ResolveContact} {
					if got, err := lookup("BOSS"); err != nil || got.ID != operator.ID {
						t.Errorf("lookup = %+v, %v, want the operator %s", got, err, operator.FormattedName)
					}
				}
				if id, err := tools.custodyOperatorID(context.Background()); err != nil || id != operator.ID {
					t.Errorf("custody operator = %s, %v, want %s", id, err, operator.ID)
				}
			})
		}
	}

	t.Run("operator_contact_id outranks a legacy pin, as in custody", func(t *testing.T) {
		tools := newTestTools(t)
		household := seedNicknameAt(t, tools.store, "Hana Household", "boss", ZoneHousehold)
		operator := seedNicknameAt(t, tools.store, "Alice Operator", "boss", ZoneKnown)
		tools.ConfigureOperatorContactID(operator.ID)
		tools.ConfigureLegacyOperatorContactID(household.ID)
		if got, err := tools.store.ResolveContact("boss"); err != nil || got.ID != operator.ID {
			t.Errorf("ResolveContact = %+v, %v, want Alice Operator", got, err)
		}
	})

	t.Run("a legacy name that resolved to no one keeps authority first", func(t *testing.T) {
		tools := newTestTools(t)
		household := seedNicknameAt(t, tools.store, "Hana Household", "boss", ZoneHousehold)
		seedNicknameAt(t, tools.store, "Kim Known", "boss", ZoneKnown)
		tools.SetOwnerContactName("Nobody")
		tools.ConfigureLegacyOperatorContactID(uuid.Nil)
		if got, err := tools.store.ResolveContact("boss"); err != nil || got.ID != household.ID {
			t.Errorf("ResolveContact = %+v, %v, want Hana Household", got, err)
		}
	})
}

// TestLegacyOwnerName_OperatorKeepsItsNickname pins the legacy selector:
// the operator's own contact, which answers to the owner name only
// through its nickname, may not replace that nickname in any turn, or
// nobody would be the operator at the next start. A case-only edit is
// allowed.
func TestLegacyOwnerName_OperatorKeepsItsNickname(t *testing.T) {
	tools, calls := newCountingTools(t)
	operator := seedNicknameAt(t, tools.store, "Alice Operator", "Alice", ZoneKnown)
	tools.SetOwnerContactName("Alice")
	for _, attended := range []bool{false, true} {
		_, err := modelSave(tools, `{"name":"Alice Operator","nickname":"Ally"}`, attended)
		requireContains(t, err, `refused nickname "Ally" for Alice Operator`, "the name Thane recognizes the operator by", "Nothing was saved")
	}
	if got, _ := tools.store.Get(operator.ID); got.Nickname != "Alice" || *calls != 0 {
		t.Errorf("operator nickname = %q, mutations %d", got.Nickname, *calls)
	}
	if _, err := modelSave(tools, `{"name":"Alice Operator","nickname":"ALICE"}`, false); err != nil {
		t.Errorf("a case-only edit must be allowed: %v", err)
	}
	if got, err := tools.store.ResolveContact("Alice"); err != nil || got.ID != operator.ID {
		t.Errorf("ResolveContact(Alice) = %+v, %v", got, err)
	}
}

// TestSaveContact_SignalOnTelOnlyContact pins #1566's no-code decision
// on a Signal IMPP added to a contact that holds only a TEL: the target
// rule already covers a contact above known, and a known contact carries
// no authority to protect.
func TestSaveContact_SignalOnTelOnlyContact(t *testing.T) {
	tools, _ := newCountingTools(t)
	hana := seedContactAt(t, tools.store, "Hana Household", ZoneHousehold, Property{Property: "TEL", Value: "+15551230009"})
	_, err := modelSave(tools, `{"name":"Hana Household","facts":{"signal":"+15551230009"}}`, false)
	requireContains(t, err, "operator-custodied", "Hana Household is household")
	if _, err := modelSave(tools, `{"name":"Hana Household","facts":{"signal":"+15551230009"}}`, true); err != nil {
		t.Fatalf("attended signal on household: %v", err)
	}
	if got, _ := tools.store.GetPropertiesMap(hana.ID); !reflect.DeepEqual(got["IMPP"], []string{"signal:+15551230009"}) {
		t.Errorf("household properties = %v", got)
	}
	seedContactAt(t, tools.store, "Kim Known", ZoneKnown, Property{Property: "TEL", Value: "+15551230010"})
	if _, err := modelSave(tools, `{"name":"Kim Known","facts":{"signal":"+15551230010"}}`, false); err != nil {
		t.Errorf("signal on a known TEL-only contact: %v", err)
	}
}

// TestImportVCF_RoutingAndNameCustody pins the import path: routing
// facts and a nickname fill stay off a contact above known, and a card
// under a name an authority contact goes by is left out, each counted.
func TestImportVCF_RoutingAndNameCustody(t *testing.T) {
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:%s\r\nNICKNAME:Bobby\r\nHA_COMPANION_APP:mobile_app_mallory\r\nNOTIFICATION_PREFERENCE:ha_push\r\nORG:Acme\r\nEND:VCARD\r\n"

	t.Run("a household merge drops routing and the nickname fill", func(t *testing.T) {
		tools := newTestTools(t)
		bob := seedContactAt(t, tools.store, "Bob Smith", ZoneHousehold)
		vcf := fmt.Sprintf(card, "Bob Smith")
		wants := []string{"2 notification routing fact(s) were not imported", "1 nickname(s) were not filled in on a merge"}
		dry := importText(t, tools, vcf, map[string]any{"dry_run": true})
		for _, want := range append(wants, "Would merge") {
			if !strings.Contains(dry, want) {
				t.Errorf("dry run = %q, want %q", dry, want)
			}
		}
		if got, _ := tools.store.GetWithProperties(bob.ID); got.Org != "" || got.Nickname != "" || len(got.Properties) != 0 {
			t.Errorf("dry run wrote %+v", got)
		}
		out := importText(t, tools, vcf, nil)
		for _, want := range append(wants, "1 merged") {
			if !strings.Contains(out, want) {
				t.Errorf("import = %q, want %q", out, want)
			}
		}
		got, err := tools.store.GetWithProperties(bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Org != "Acme" || got.Nickname != "" || len(got.Properties) != 0 {
			t.Errorf("Bob after merge = org %q nickname %q props %+v", got.Org, got.Nickname, got.Properties)
		}
	})

	t.Run("a known merge keeps them", func(t *testing.T) {
		tools := newTestTools(t)
		kim := seedContactAt(t, tools.store, "Kim Known", ZoneKnown)
		out := importText(t, tools, fmt.Sprintf(card, "Kim Known"), nil)
		if strings.Contains(out, "were not imported") {
			t.Errorf("import = %q", out)
		}
		got, err := tools.store.GetWithProperties(kim.ID)
		if err != nil {
			t.Fatal(err)
		}
		props, _ := tools.store.GetPropertiesMap(kim.ID)
		if got.Nickname != "Bobby" || len(FactValues(props, PropertyHACompanionApp)) != 1 || len(FactValues(props, PropertyNotificationPreference)) != 1 {
			t.Errorf("Kim after merge = nickname %q props %v", got.Nickname, props)
		}
	})

	t.Run("a card under an authority contact's name or nickname is skipped", func(t *testing.T) {
		tools := newTestTools(t)
		seedNicknameAt(t, tools.store, "Jane Doe", "Mom", ZoneHousehold)
		seedContactAt(t, tools.store, "Ada Admin", ZoneAdmin)
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:mom\r\nEND:VCARD\r\n" +
			"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Mallory\r\nNICKNAME:MOM\r\nEND:VCARD\r\n" +
			"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada Admin\r\nEND:VCARD\r\n" +
			"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Fine Person\r\nEND:VCARD\r\n"
		dry := importText(t, tools, vcf, map[string]any{"merge": false, "dry_run": true})
		for _, want := range []string{"1 would be created, 0 would be merged, 3 would be skipped", "Would skip card 1: an admin, household, trusted or operator contact already goes by its name or nickname", "Would skip card 2", "Would skip card 3", "fuller name"} {
			if !strings.Contains(dry, want) {
				t.Errorf("dry run = %q, want %q", dry, want)
			}
		}
		out := importText(t, tools, vcf, map[string]any{"merge": false})
		for _, want := range []string{"1 created, 0 merged, 3 skipped", "3 card(s) were skipped because an admin, household, trusted or operator contact already goes by their name or nickname: cards 1, 2, 3.", "fuller name"} {
			if !strings.Contains(out, want) {
				t.Errorf("import = %q, want %q", out, want)
			}
		}
		for _, name := range []string{"mom", "Mallory"} {
			if _, err := tools.store.FindByName(name); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%q was created: %v", name, err)
			}
		}
		if all, err := tools.store.ListAll(); err != nil || len(all) != 3 {
			t.Errorf("contacts after import = %d, %v, want Jane, Ada and Fine Person", len(all), err)
		}
	})
}

// TestSaveContact_EdgeSpaceNickname pins resolver parity for edge
// space. SQLite LOWER folds ASCII case and trims nothing, so a padded
// nickname stops answering to the bare name. contact_save stores a name
// and nickname trimmed, counts trimming a stored nickname as a change,
// and keeps a name an authority record stores padded that record's own,
// so no two-step save hands a second record "nugget" or "Mom" and the
// notifications they route.
func TestSaveContact_EdgeSpaceNickname(t *testing.T) {
	setup := func(t *testing.T) (*Tools, map[string]*Contact) {
		t.Helper()
		tools := newTestTools(t)
		seeded := map[string]*Contact{
			"alice": seedNicknameAt(t, tools.store, "Alice Operator", "nugget", ZoneAdmin),
			"jane":  seedNicknameAt(t, tools.store, "Jane Doe", "Mom", ZoneHousehold),
			"dan":   seedContactAt(t, tools.store, "Dan Known", ZoneKnown),
		}
		tools.ConfigureOperatorContactID(seeded["alice"].ID)
		return tools, seeded
	}
	resolvesTo := func(t *testing.T, tools *Tools, name string, want *Contact) {
		t.Helper()
		if got, err := tools.store.ResolveContact(name); err != nil || got.ID != want.ID {
			t.Errorf("ResolveContact(%q) = %+v, %v, want %s", name, got, err, want.FormattedName)
		}
	}

	for _, attended := range []bool{false, true} {
		t.Run(fmt.Sprintf("a padded nickname is stored trimmed/attended=%v", attended), func(t *testing.T) {
			tools, seeded := setup(t)
			for _, tc := range []struct{ args, who, want string }{
				{`{"name":"Alice Operator","nickname":"nugget "}`, "alice", "nugget"},
				{`{"name":"Jane Doe","nickname":" Mom"}`, "jane", "Mom"},
				{`{"name":"Jane Doe","nickname":"Mom "}`, "jane", "Mom"},
				{`{"name":" Jane Doe ","note":"found by her trimmed name"}`, "jane", "Mom"},
			} {
				if _, err := modelSave(tools, tc.args, attended); err != nil {
					t.Errorf("save %s: %v", tc.args, err)
				}
				if got, _ := tools.store.Get(seeded[tc.who].ID); got.Nickname != tc.want {
					t.Errorf("after %s nickname = %q, want %q", tc.args, got.Nickname, tc.want)
				}
			}
			if all, err := tools.store.ListAll(); err != nil || len(all) != 3 {
				t.Errorf("contacts = %d, %v, want the three seeded", len(all), err)
			}
			// The second step of the capture stays refused.
			_, err := modelSave(tools, `{"name":"nugget","facts":{"ha_companion_app":"mobile_app_mallory"}}`, false)
			requireContains(t, err, `Alice Operator already goes by "nugget"`)
			_, err = modelSave(tools, `{"name":"Dan Known","nickname":"Mom"}`, false)
			requireContains(t, err, `Jane Doe already goes by "Mom"`)
			resolvesTo(t, tools, "nugget", seeded["alice"])
			resolvesTo(t, tools, "Mom", seeded["jane"])
		})
	}

	t.Run("trimming a stored padded nickname is a change", func(t *testing.T) {
		tools := newTestTools(t)
		jane := seedNicknameAt(t, tools.store, "Jane Doe", "Mom ", ZoneHousehold)
		_, err := modelSave(tools, `{"name":"Jane Doe","nickname":"Mom"}`, false)
		requireContains(t, err, `"nickname"="Mom" (NICKNAME)`, "Jane Doe is household")
		if got, _ := tools.store.Get(jane.ID); got.Nickname != "Mom " {
			t.Errorf("refused save left nickname %q", got.Nickname)
		}
		if _, err := modelSave(tools, `{"name":"Jane Doe","nickname":"Mom"}`, true); err != nil {
			t.Fatalf("attended trim: %v", err)
		}
		if got, _ := tools.store.Get(jane.ID); got.Nickname != "Mom" {
			t.Errorf("nickname after attended save = %q", got.Nickname)
		}
	})

	for _, attended := range []bool{false, true} {
		t.Run(fmt.Sprintf("a name an authority record stores padded stays its own/attended=%v", attended), func(t *testing.T) {
			tools := newTestTools(t)
			seedNicknameAt(t, tools.store, "Jane Doe", "Mom ", ZoneHousehold)
			ada := seedContactAt(t, tools.store, "Ada Admin ", ZoneAdmin)
			if ada.FormattedName != "Ada Admin " {
				t.Fatalf("seeded name = %q, want the padded spelling", ada.FormattedName)
			}
			dan := seedContactAt(t, tools.store, "Dan Known", ZoneKnown)
			for _, args := range []string{
				`{"name":"Mallory","nickname":"Mom","kind":"individual"}`,
				`{"name":"mom","kind":"individual"}`,
				`{"name":"Dan Known","nickname":"MOM"}`,
				`{"name":"Ada Admin","kind":"individual"}`,
				`{"name":"Dan Known","nickname":"ada admin"}`,
			} {
				_, err := modelSave(tools, args, attended)
				requireContains(t, err, "stays with that contact", "nothing was saved")
			}
			if all, err := tools.store.ListAll(); err != nil || len(all) != 3 {
				t.Errorf("contacts = %d, %v, want the three seeded", len(all), err)
			}
			if got, _ := tools.store.Get(dan.ID); got.Nickname != "" {
				t.Errorf("Dan Known nickname = %q", got.Nickname)
			}
		})
	}

	t.Run("import takes neither name", func(t *testing.T) {
		tools, seeded := setup(t)
		vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:nugget\r\nHA_COMPANION_APP:mobile_app_mallory\r\nEND:VCARD\r\n" +
			"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dan Known\r\nNICKNAME:Mom \r\nORG:Acme\r\nEND:VCARD\r\n"
		out := importText(t, tools, vcf, nil)
		for _, want := range []string{"0 created, 1 merged, 1 skipped", "already goes by their name or nickname: card 1.", "1 nickname(s) were not filled in on a merge"} {
			if !strings.Contains(out, want) {
				t.Errorf("import = %q, want %q", out, want)
			}
		}
		if got, _ := tools.store.Get(seeded["dan"].ID); got.Nickname != "" || got.Org != "Acme" {
			t.Errorf("Dan Known after merge = nickname %q org %q", got.Nickname, got.Org)
		}
		resolvesTo(t, tools, "nugget", seeded["alice"])
		resolvesTo(t, tools, "Mom", seeded["jane"])
	})
}

// TestLegacyOwnerName_ResolverParity pins the legacy selector's
// comparisons: whether a record answers to the owner name after a save
// is decided the way the resolver decides it, so edge space is trimmed
// before it is stored and a Unicode case variant is not the name, in
// every turn.
func TestLegacyOwnerName_ResolverParity(t *testing.T) {
	t.Run("a padded owner nickname is stored trimmed", func(t *testing.T) {
		tools := newTestTools(t)
		operator := seedNicknameAt(t, tools.store, "Alice Operator", "nugget", ZoneKnown)
		tools.SetOwnerContactName("nugget")
		for _, attended := range []bool{false, true} {
			if _, err := modelSave(tools, `{"name":"Alice Operator","nickname":"nugget "}`, attended); err != nil {
				t.Errorf("attended=%v: %v", attended, err)
			}
		}
		if got, err := tools.store.FindByNickname("nugget"); err != nil || got.ID != operator.ID {
			t.Errorf("FindByNickname(nugget) = %+v, %v, want the operator", got, err)
		}
	})

	t.Run("a Unicode case variant of the owner nickname is refused", func(t *testing.T) {
		tools := newTestTools(t)
		operator := seedNicknameAt(t, tools.store, "Alice Operator", "kris", ZoneKnown)
		tools.SetOwnerContactName("kris")
		for _, attended := range []bool{false, true} {
			// U+212A KELVIN SIGN folds to k under strings.EqualFold but
			// not under SQLite LOWER.
			_, err := modelSave(tools, "{\"name\":\"Alice Operator\",\"nickname\":\"\u212aris\"}", attended)
			requireContains(t, err, "the name Thane recognizes the operator by", "Nothing was saved")
		}
		if got, err := tools.store.FindByNickname("kris"); err != nil || got.ID != operator.ID {
			t.Errorf("FindByNickname(kris) = %+v, %v, want the operator", got, err)
		}
	})

	t.Run("a record answering only with edge space may not take the bare name", func(t *testing.T) {
		tools := newTestTools(t)
		operator := seedContactAt(t, tools.store, "Alice Operator", ZoneKnown)
		bob := seedNicknameAt(t, tools.store, "Bob Known", "nugget ", ZoneKnown)
		tools.SetOwnerContactName("nugget")
		tools.ConfigureLegacyOperatorContactID(operator.ID)
		_, err := modelSave(tools, `{"name":"Bob Known","nickname":"nugget"}`, true)
		requireContains(t, err, `refused nickname "nugget" for Bob Known`, "Nothing was saved")
		if got, _ := tools.store.Get(bob.ID); got.Nickname != "nugget " {
			t.Errorf("Bob Known nickname = %q", got.Nickname)
		}
	})
}
