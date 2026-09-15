package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newCustodyRegistry(t *testing.T) (*Registry, *contacts.Store, map[string]*contacts.Contact) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(name, zone string, props ...contacts.Property) *contacts.Contact {
		c, err := store.UpsertWithProperties(&contacts.Contact{FormattedName: name, Kind: "individual", TrustZone: zone}, props)
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return c
	}
	seeded := map[string]*contacts.Contact{
		"alice": seed("Alice Operator", contacts.ZoneAdmin),
		"bob":   seed("Bob Household", contacts.ZoneHousehold),
		"carol": seed("Carol Holder", contacts.ZoneTrusted, contacts.Property{Property: "EMAIL", Value: "carol@example.com"}),
		"dan":   seed("Dan Known", contacts.ZoneKnown),
	}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureOperatorContactID(seeded["alice"].ID)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)
	return registry, store, seeded
}

// TestContactSaveOperatorAttendedCarveOut pins how the contact_save
// handler wires OperatorAttended: only the operator's own message may
// add an address to a contact above known or to the operator's own
// contact, and no turn may give an authority holder's value a second
// holder.
func TestContactSaveOperatorAttendedCarveOut(t *testing.T) {
	bg := context.Background()
	owner := &memory.ChannelBinding{Channel: "signal", Address: "+15551110000", IsOwner: true}
	cases := []struct {
		name     string
		ctx      context.Context
		attended bool
	}{
		{"native API message", WithHints(WithMessageOrigin(bg, memory.OriginAPI), map[string]string{"channel": "api"}), true},
		{"operator's own channel message", WithMessageOrigin(WithChannelBinding(bg, owner), memory.OriginChannel), true},
		{"wake", WithMessageOrigin(bg, memory.OriginWake), false},
		{"owner-bound loop wake", WithMessageOrigin(WithChannelBinding(bg, owner), memory.OriginWake), false},
		{"API origin without the api hint", WithMessageOrigin(bg, memory.OriginAPI), false},
	}
	targets := []struct {
		key, name, fact, value, property string
	}{
		{"bob", "Bob Household", "email", "bob.new@example.com", "EMAIL"},
		{"alice", "Alice Operator", "phone", "+15559990000", "TEL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, store, seeded := newCustodyRegistry(t)
			save := registry.Get("contact_save").Handler
			for _, target := range targets {
				_, err := save(tc.ctx, map[string]any{"name": target.name, "facts": map[string]any{target.fact: target.value}})
				props, perr := store.GetProperties(seeded[target.key].ID)
				if perr != nil {
					t.Fatal(perr)
				}
				if tc.attended {
					if err != nil {
						t.Errorf("%s: attended save refused: %v", target.name, err)
					}
					if len(props) != 1 || props[0].Property != target.property {
						t.Errorf("%s: properties = %+v", target.name, props)
					}
					continue
				}
				if err == nil || !strings.Contains(err.Error(), "operator-custodied") {
					t.Errorf("%s: unattended save must be refused: %v", target.name, err)
				}
				if len(props) != 0 {
					t.Errorf("%s: refused save wrote %+v", target.name, props)
				}
			}

			_, err := save(tc.ctx, map[string]any{"name": "Dan Known", "facts": map[string]any{"email": "carol@example.com"}})
			if err == nil || !strings.Contains(err.Error(), "already held by Carol Holder") {
				t.Errorf("the holder rule must hold in every turn: %v", err)
			}

			// A household contact's notification device and nickname
			// follow the same lift.
			bob := seeded["bob"].ID
			_, err = save(tc.ctx, map[string]any{"name": "Bob Household", "facts": map[string]any{"ha_companion_app": "mobile_app_bob"}})
			routing, perr := store.GetPropertiesMap(bob)
			if perr != nil {
				t.Fatal(perr)
			}
			_, nerr := save(tc.ctx, map[string]any{"name": "Bob Household", "nickname": "Bobby"})
			got, gerr := store.Get(bob)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if tc.attended {
				if err != nil || len(routing["ha_companion_app"]) != 1 {
					t.Errorf("attended device save = %v, properties %v", err, routing)
				}
				if nerr != nil || got.Nickname != "Bobby" {
					t.Errorf("attended nickname save = %v, nickname %q", nerr, got.Nickname)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "operator-custodied") || len(routing["ha_companion_app"]) != 0 {
					t.Errorf("unattended device save must be refused: %v, properties %v", err, routing)
				}
				if nerr == nil || !strings.Contains(nerr.Error(), "operator-custodied") || got.Nickname != "" {
					t.Errorf("unattended nickname save must be refused: %v, nickname %q", nerr, got.Nickname)
				}
			}

			// The name-holder rule holds in every turn and never claims
			// the turn is unattended.
			_, err = save(tc.ctx, map[string]any{"name": "Dan Known", "nickname": "Carol Holder"})
			if err == nil || !strings.Contains(err.Error(), `Carol Holder already goes by "Carol Holder"`) {
				t.Errorf("the name-holder rule must hold in every turn: %v", err)
			} else if strings.Contains(err.Error(), "not the operator's own message") {
				t.Errorf("a name refusal must not claim the turn is unattended: %v", err)
			}
		})
	}
}

// TestContactToolDescriptionsTeachIdentityCustody pins the decision-time
// teaching: each model-facing contact writer's description names what
// identity custody refuses and routes refused values to the operator's
// CardDAV path, so a model learns the rule before it hits the refusal.
func TestContactToolDescriptionsTeachIdentityCustody(t *testing.T) {
	registry, _, _ := newCustodyRegistry(t)
	parameterDescription := func(tool, parameter string) string {
		t.Helper()
		properties, ok := registry.Get(tool).Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no properties schema", tool)
		}
		schema, ok := properties[parameter].(map[string]any)
		if !ok {
			t.Fatalf("%s has no %s parameter", tool, parameter)
		}
		description, _ := schema["description"].(string)
		return description
	}
	cases := []struct {
		surface string
		text    string
		want    []string
	}{
		{"contact_save", registry.Get("contact_save").Description, []string{
			"KEY and X-THANE-* fact keys", "starts at known", "above known", "operator's own contact",
			"operator's own message", "already holds", "nothing is saved", "CardDAV",
			"notification_preference or ha_companion_app", "changing the nickname", "only the first value",
			"already goes by", "what to do",
			"Outside the operator's own message it also refuses a new contact's name, or any contact's nickname, that one of those contacts answers to by its given name or the first word of its formatted name",
		}},
		{"contact_save facts", parameterDescription("contact_save", "facts"), []string{
			"letters, digits", "KEY and X-THANE-* keys are refused", "control characters", "above known", "already holds",
			"notification_preference and ha_companion_app", "only the first value",
		}},
		{"contact_save nickname", parameterDescription("contact_save", "nickname"), []string{
			"formatted name or nickname", "operator's own contact wins, then one above known", "in every turn", "already goes by", "operator's own message", "above known",
			"two or more at the same standing are a tie that reaches none of them", "makes the name reach neither",
			"also refused when one of those contacts answers to it by its given name or the first word of its formatted name",
		}},
		{"contact_lookup", registry.Get("contact_lookup").Description, []string{
			"formatted name or nickname", "operator's own contact wins, then one above known",
			"Two or more holders at the same standing, both above known or both known, are a tie: it returns none of them",
			"whichever holds the name as a formatted name and whichever as a nickname", "tries no given name or first word",
			"those holding it as a formatted name or nickname first", "only its contact_id tells it apart",
			"given name and the first word", "exactly one contact must fit", "whatever their zones", "contact_id, trust zone and the field it matched",
			"retry with the full formatted name", "never matches notes", "known duplicate", "contact_directory",
			"lists up to five of them", "query set to the same name lists every contact that fits ahead of any other match",
			fmt.Sprintf("lists up to %d matches, and says so when more match than it lists", contacts.SearchLimit),
			"Each query row carries the contact's contact_id and trust zone",
		}},
		{"contact_lookup name", parameterDescription("contact_lookup", "name"), []string{
			"formatted name or nickname", "given name or the first word", "exactly one contact must fit", "never used to resolve a name",
			"retry with the full formatted name", "Use query",
		}},
		{"contact_lookup query", parameterDescription("contact_lookup", "query"), []string{
			"given names", "notes", fmt.Sprintf("up to %d matching contacts", contacts.SearchLimit), "listed first", "when more match than it lists",
			"Each row carries the contact's contact_id and trust zone",
		}},
		{"contact_forget", registry.Get("contact_forget").Description, []string{
			"exactly one of name or contact_id", "as contact_lookup resolves it", "operator's own contact first, then one above known",
			"none when two or more at the same standing hold it", "A name that resolves to none removes nothing",
			"Forgot contact:", "above known", "operator's own contact", "Home Assistant person", "in every turn", "by name or by contact_id", "CardDAV",
			"known duplicate", "with their UUIDs", "no exact holder", "carry it as a nickname",
		}},
		{"contact_forget name", parameterDescription("contact_forget", "name"), []string{
			"exactly one contact", "contact_lookup", "never used to resolve a name", "retry with the contact_id",
		}},
		{"contact_export_vcf name", parameterDescription("contact_export_vcf", "name"), []string{
			"\"self\"", "given name or the first word", "never used to resolve a name", "retry with the full formatted name",
		}},
		{"contact_export_vcf_qr name", parameterDescription("contact_export_vcf_qr", "name"), []string{
			"\"self\"", "given name or the first word", "never used to resolve a name", "retry with the full formatted name",
		}},
		{"contact_forget contact_id", parameterDescription("contact_forget", "contact_id"), []string{
			"Canonical UUID", "exactly one of name or contact_id", "known duplicate", "contact_directory", "same contacts by contact_id as by name",
		}},
		{"contact_import_vcf", registry.Get("contact_import_vcf").Description, []string{
			"above known", "already holds", "no turn lifts", "CardDAV", "dry_run reports the same counts",
			"NOTIFICATION_PREFERENCE and HA_COMPANION_APP", "already goes by", "changes its nickname",
			"or answers to by its given name or the first word of its formatted name, is left out",
		}},
	}
	for _, tc := range cases {
		for _, want := range tc.want {
			if !strings.Contains(tc.text, want) {
				t.Errorf("%s description lacks %q: %s", tc.surface, want, tc.text)
			}
		}
	}
}
