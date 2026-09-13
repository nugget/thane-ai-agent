package tools

import (
	"context"
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
		}},
		{"contact_save facts", parameterDescription("contact_save", "facts"), []string{
			"letters, digits", "KEY and X-THANE-* keys are refused", "control characters", "above known", "already holds",
		}},
		{"contact_forget", registry.Get("contact_forget").Description, []string{
			"Forgot contact:", "above known", "operator's own contact", "Home Assistant person", "in every turn", "CardDAV",
		}},
		{"contact_forget name", parameterDescription("contact_forget", "name"), []string{
			"exactly one contact", "contact_lookup",
		}},
		{"contact_import_vcf", registry.Get("contact_import_vcf").Description, []string{
			"above known", "already holds", "no turn lifts", "CardDAV", "dry_run reports the same counts",
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
