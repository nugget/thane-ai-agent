package contacts

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// pinUnresolvedLegacyOwner pins the legacy owner name as the app does at
// startup: the channel resolver's answer, uuid.Nil with the error
// ResolveContact returned when the name named no single record.
func pinUnresolvedLegacyOwner(t *testing.T, tools *Tools, owner string, wantExactTie bool) {
	t.Helper()
	tools.SetOwnerContactName(owner)
	_, err := tools.store.ResolveContact(owner)
	var amb *AmbiguousNameError
	if !errors.As(err, &amb) || amb.ExactTie != wantExactTie {
		t.Fatalf("ResolveContact(%q) = %v, want an ambiguity with ExactTie=%v", owner, err, wantExactTie)
	}
	tools.ConfigureLegacyOperatorContactID(uuid.Nil, err)
}

// TestLegacyOperatorTie_CustodyFailsClosed pins custody when the legacy
// owner name named no single record at startup because several answer
// to it. The operator is one of them and custody cannot tell which, so
// it refuses every write it guards instead of protecting none: an
// unattended write could otherwise plant an address on one holder, then
// break the tie by changing the other's nickname or forgetting it, and
// the next start would make the first one the operator, planted address
// and all. Writes custody does not guard still land.
func TestLegacyOperatorTie_CustodyFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exactTie  bool
		seed      func(t *testing.T, store *Store) (a, b *Contact)
		breakTies []string // saves that would leave b the only holder
	}{
		{
			name:     "exact tie on a nickname",
			exactTie: true,
			seed: func(t *testing.T, store *Store) (*Contact, *Contact) {
				return seedNicknameAt(t, store, "Alice Adams", "Boss", ZoneKnown), seedNicknameAt(t, store, "Alice Baker", "Boss", ZoneKnown)
			},
			breakTies: []string{`{"name":"Alice Adams","nickname":"Bee"}`},
		},
		{
			name:     "shared first word",
			exactTie: false,
			seed: func(t *testing.T, store *Store) (*Contact, *Contact) {
				return seedContactAt(t, store, "Boss Adams", ZoneKnown), seedContactAt(t, store, "Boss Baker", ZoneKnown)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, calls := newCountingTools(t)
			a, b := tc.seed(t, tools.store)
			bystander := seedContactAt(t, tools.store, "Dave Known", ZoneKnown)
			pinUnresolvedLegacyOwner(t, tools, "Boss", tc.exactTie)
			wants := []string{"check identity custody: nothing was changed", `identity.owner_contact_name "Boss"`, "named no single contact when Thane started", a.ID.String(), b.ID.String(), "identity.operator_contact_id", "restart Thane"}

			// Planting an address on either holder is refused, in and out
			// of the operator's own message: the operator's message lifts
			// the target rule, not the need to know whose record it is.
			for _, attended := range []bool{false, true} {
				_, err := modelSave(tools, `{"name":"`+b.FormattedName+`","facts":{"email":"mallory@example.com"}}`, attended)
				requireContains(t, err, wants...)
			}
			for _, args := range tc.breakTies {
				for _, attended := range []bool{false, true} {
					_, err := modelSave(tools, args, attended)
					requireContains(t, err, wants...)
				}
			}
			for _, target := range []*Contact{a, bystander} {
				_, err := tools.ForgetContact(`{"contact_id":"` + target.ID.String() + `"}`)
				requireContains(t, err, wants...)
			}
			_, err := modelSave(tools, `{"name":"Carol New"}`, false)
			requireContains(t, err, wants...)

			// A write custody does not guard is unaffected.
			if _, err := modelSave(tools, `{"name":"`+a.FormattedName+`","note":"met at the fair"}`, false); err != nil {
				t.Errorf("a note is not custody: %v", err)
			}

			got, err := tools.store.GetWithProperties(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range got.Properties {
				if strings.EqualFold(p.Property, identityEmail) {
					t.Errorf("%s gained %s=%s", b.FormattedName, p.Property, p.Value)
				}
			}
			// The tie stands, so the next start pins no one either.
			var amb *AmbiguousNameError
			if _, err := tools.store.ResolveContact("Boss"); !errors.As(err, &amb) || amb.Total != 2 {
				t.Errorf("ResolveContact(Boss) = %v, want the same two-way ambiguity", err)
			}
			if *calls != 1 {
				t.Errorf("mutations = %d, want only the note", *calls)
			}
		})
	}
}

// TestLegacyOperatorPin_OwnerReportsWhyNoneWasPinned pins contact_owner
// under a pinned legacy owner name that named no record: absence is
// reported as absence, and a name several contacts answer to as that,
// with each candidate's contact_id and the operator's fix, never as a
// missing contact.
func TestLegacyOperatorPin_OwnerReportsWhyNoneWasPinned(t *testing.T) {
	t.Run("a tie is not a missing contact", func(t *testing.T) {
		tools := newTestTools(t)
		a := seedNicknameAt(t, tools.store, "Alice Adams", "Boss", ZoneKnown)
		b := seedNicknameAt(t, tools.store, "Alice Baker", "Boss", ZoneKnown)
		pinUnresolvedLegacyOwner(t, tools, "Boss", true)
		_, err := tools.OwnerContact("")
		requireContains(t, err, `identity.owner_contact_name "Boss"`, "named no single contact when Thane started", "at the same standing (known) hold it exactly",
			a.ID.String(), b.ID.String(), "identity.operator_contact_id")
		if err != nil && (strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "Retry with the contact_id")) {
			t.Errorf("contact_owner = %v, want the tie without absence or a tool retry", err)
		}
	})

	t.Run("no holder is a missing contact", func(t *testing.T) {
		tools := newTestTools(t)
		tools.SetOwnerContactName("Boss")
		tools.ConfigureLegacyOperatorContactID(uuid.Nil, sql.ErrNoRows)
		_, err := tools.OwnerContact("")
		requireContains(t, err, `"Boss" not found`, "when Thane started", "identity.operator_contact_id")
		// No operator is configured then, so custody protects no record as
		// the operator's and a known contact takes an address.
		seedContactAt(t, tools.store, "Dave Known", ZoneKnown)
		if _, err := modelSave(tools, `{"name":"Dave Known","facts":{"email":"dave@example.com"}}`, false); err != nil {
			t.Errorf("save with no operator = %v", err)
		}
	})
}
