package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestForgetContact_ByContactID pins contact_forget's contact_id door:
// once a name resolves to the record above known, the known duplicate
// that shares it is forgotten by its UUID, a refused forget by name
// names that duplicate, custody refuses a record by id exactly as it
// does by name, and exactly one of name and contact_id is accepted.
func TestForgetContact_ByContactID(t *testing.T) {
	setup := func(t *testing.T) (*Tools, *Contact, *Contact) {
		t.Helper()
		tools := newTestTools(t)
		household := seedNicknameAt(t, tools.store, "Carol Household", "Carol", ZoneHousehold)
		duplicate := seedContactAt(t, tools.store, "Carol", ZoneKnown)
		return tools, household, duplicate
	}

	t.Run("a known duplicate is forgotten by id while its name resolves to the household record", func(t *testing.T) {
		tools, household, duplicate := setup(t)
		if got, err := tools.store.ResolveContact("Carol"); err != nil || got.ID != household.ID {
			t.Fatalf("ResolveContact(Carol) = %+v, %v, want the household record", got, err)
		}
		_, err := tools.ForgetContact(`{"name":"Carol"}`)
		requireContains(t, err,
			fmt.Sprintf("contact_forget refused Carol Household (household, %s)", household.ID),
			"it is household", "Nothing was removed",
			fmt.Sprintf(`The name "Carol" also fits Carol (known, %s)`, duplicate.ID), "call contact_forget with its contact_id")

		result, err := tools.ForgetContact(fmt.Sprintf(`{"contact_id":%q}`, duplicate.ID))
		if err != nil {
			t.Fatalf("forget by id: %v", err)
		}
		if want := fmt.Sprintf("Forgot contact: Carol (known, %s)", duplicate.ID); result != want {
			t.Errorf("result = %q, want %q", result, want)
		}
		if _, err := tools.store.Get(duplicate.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("the duplicate is still active: %v", err)
		}
		if got, err := tools.store.ResolveContact("Carol"); err != nil || got.ID != household.ID {
			t.Errorf("after the forget ResolveContact(Carol) = %+v, %v, want the household record", got, err)
		}
		_, err = tools.ForgetContact(fmt.Sprintf(`{"contact_id":%q}`, duplicate.ID))
		requireContains(t, err, "is not an active contact", "nothing was removed")
	})

	t.Run("custody refuses by id as it does by name", func(t *testing.T) {
		cases := []struct {
			name       string
			target     func(*testing.T, *Tools, *Contact) *Contact
			wantReason string
		}{
			{name: "a household record", wantReason: "it is household",
				target: func(_ *testing.T, _ *Tools, household *Contact) *Contact { return household }},
			{name: "the operator's record at known", wantReason: "it is the operator's own contact",
				target: func(t *testing.T, tools *Tools, _ *Contact) *Contact {
					operator := seedContactAt(t, tools.store, "Alice Operator", ZoneKnown)
					tools.ConfigureOperatorContactID(operator.ID)
					return operator
				}},
			{name: "a known record bound to a Home Assistant person", wantReason: "bound to Home Assistant person person.erin",
				target: func(t *testing.T, tools *Tools, _ *Contact) *Contact {
					bound := seedContactAt(t, tools.store, "Erin Bound", ZoneKnown)
					if err := tools.store.SetHAPersonEntity(bound.ID, "person.erin"); err != nil {
						t.Fatal(err)
					}
					return bound
				}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				tools, household, _ := setup(t)
				target := tc.target(t, tools, household)
				_, err := tools.ForgetContact(fmt.Sprintf(`{"contact_id":%q}`, target.ID))
				requireContains(t, err, "contact_forget refused "+target.FormattedName, tc.wantReason, "Nothing was removed")
				if err != nil && strings.Contains(err.Error(), "The name also fits") {
					t.Errorf("a forget by id must not suggest siblings: %v", err)
				}
				if _, err := tools.store.Get(target.ID); err != nil {
					t.Errorf("a refused forget removed the record: %v", err)
				}
			})
		}
	})

	t.Run("a refused forget by name with no removable sibling adds no hint", func(t *testing.T) {
		tools := newTestTools(t)
		seedContactAt(t, tools.store, "Hal Household", ZoneHousehold)
		_, err := tools.ForgetContact(`{"name":"Hal Household"}`)
		requireContains(t, err, "contact_forget refused Hal Household", "it is household")
		if err != nil && strings.Contains(err.Error(), "also fits") {
			t.Errorf("no sibling exists, so no hint: %v", err)
		}
	})

	// The hint names only records the passed name fits: forgetting the
	// household record by its own full name must not offer a sibling
	// that answers only to a shorter name.
	t.Run("a forget by the refused record's full name names no sibling the name does not fit", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			seeds []forkSeed
			args  string
		}{
			{name: "a nickname sibling", args: `{"name":"Carol Household"}`,
				seeds: []forkSeed{{name: "Carol Household", nickname: "Carol", zone: ZoneHousehold}, {name: "Carol", zone: ZoneKnown}}},
			{name: "a first-word sibling", args: `{"name":"Oscar Household"}`,
				seeds: []forkSeed{{name: "Oscar Household", zone: ZoneHousehold}, {name: "Oscar", zone: ZoneKnown}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tools := newTestTools(t)
				seedForkRecords(t, tools.store, tc.seeds)
				_, err := tools.ForgetContact(tc.args)
				requireContains(t, err, "contact_forget refused", "it is household")
				if err != nil && strings.Contains(err.Error(), "also fits") {
					t.Errorf("the full name fits no sibling, so no hint: %v", err)
				}
			})
		}
	})

	t.Run("exactly one of name or contact_id", func(t *testing.T) {
		tools, _, duplicate := setup(t)
		for _, tc := range []struct {
			name, args string
			want       []string
		}{
			{"neither", `{}`, []string{"pass exactly one of name or contact_id", "nothing was removed"}},
			{"a blank name", `{"name":"  "}`, []string{"pass exactly one of name or contact_id"}},
			{"both", fmt.Sprintf(`{"name":"Carol","contact_id":%q}`, duplicate.ID), []string{"not both", "nothing was removed"}},
			{"an uppercase UUID", fmt.Sprintf(`{"contact_id":%q}`, strings.ToUpper(duplicate.ID.String())), []string{"canonical non-zero UUID", "nothing was removed"}},
			{"the zero UUID", `{"contact_id":"00000000-0000-0000-0000-000000000000"}`, []string{"canonical non-zero UUID"}},
			{"not a UUID", `{"contact_id":"carol"}`, []string{"canonical non-zero UUID"}},
			{"an unknown UUID", `{"contact_id":"01a0a0ff-0000-7000-8000-000000000001"}`, []string{"is not an active contact", "nothing was removed"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := tools.ForgetContact(tc.args)
				requireContains(t, err, tc.want...)
			})
		}
		if _, err := tools.store.Get(duplicate.ID); err != nil {
			t.Errorf("a refused call removed the duplicate: %v", err)
		}
	})
}
