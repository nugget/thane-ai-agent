package contacts

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSaveContact_GivenNameIsTargetCustody pins the given name of a
// contact above known, or of the operator's own at any zone, under the
// target rule. The short-form claim rule protects a name such a contact
// answers to only by its given name, so without this an unattended turn
// takes the name in two saves: rewrite the given name, and the record
// no longer answers to it, then give a known contact the name as its
// nickname, and resolution finds that exact holder. The operator's own
// message lifts the rule, and an edit the name key folds away is not a
// change.
func TestSaveContact_GivenNameIsTargetCustody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		zone     string
		operator bool // pin the holder as the operator's own record
		reason   string
	}{
		{name: "household holder", zone: ZoneHousehold, reason: "Dr. A. Jones is household"},
		{name: "operator's own record at known", zone: ZoneKnown, operator: true, reason: "Dr. A. Jones is the operator's own contact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, calls := newCountingTools(t)
			jones := seedGivenAt(t, tools.store, "Dr. A. Jones", "Alice", tc.zone)
			seedContactAt(t, tools.store, "Mallory Known", ZoneKnown)
			if tc.operator {
				tools.ConfigureOperatorContactID(jones.ID)
			} else {
				tools.ConfigureOperatorContactID(seedContactAt(t, tools.store, "Carol Operator", ZoneKnown).ID)
			}
			const claim = `{"name":"Mallory Known","nickname":"Alice"}`
			// Control: the short-form rule refuses the claim while Dr. A.
			// Jones answers to Alice by its given name.
			_, err := modelSave(tools, claim, false)
			requireContains(t, err, `answers to "Alice" by its given name`)

			// Step one of the chain: rewriting or blanking the given name.
			for _, given := range []string{"Alicia", " "} {
				_, err := modelSave(tools, `{"name":"Dr. A. Jones","given_name":"`+given+`"}`, false)
				requireContains(t, err, `"given_name"="`+given+`" (GIVEN_NAME)`, tc.reason, "nothing was saved", "given name is what", "This turn is not the operator's own message")
			}
			if got, err := tools.store.Get(jones.ID); err != nil || got.GivenName != "Alice" {
				t.Fatalf("given name = %+v, %v, want Alice kept", got, err)
			}
			// Step two stays refused, and Alice still reaches Dr. A. Jones.
			_, err = modelSave(tools, claim, false)
			requireContains(t, err, `answers to "Alice" by its given name`)
			if got, err := tools.store.ResolveContact("Alice"); err != nil || got.ID != jones.ID {
				t.Errorf("ResolveContact(Alice) = %+v, %v, want Dr. A. Jones", got, err)
			}

			// An edit the name key folds away changes no name it answers to.
			if _, err := modelSave(tools, `{"name":"Dr. A. Jones","given_name":"ALICE"}`, false); err != nil {
				t.Errorf("a case-only given name edit = %v, want it allowed", err)
			}
			// The operator's own message lifts the rule.
			if _, err := modelSave(tools, `{"name":"Dr. A. Jones","given_name":"Alicia"}`, true); err != nil {
				t.Errorf("the operator's own given name change = %v, want it allowed", err)
			}
			if got, err := tools.store.Get(jones.ID); err != nil || got.GivenName != "Alicia" {
				t.Errorf("given name = %+v, %v, want Alicia", got, err)
			}
			if *calls != 2 {
				t.Errorf("mutations = %d, want the case edit and the attended change", *calls)
			}
		})
	}

	t.Run("a known contact's given name is not custody", func(t *testing.T) {
		tools := newTestTools(t)
		seedGivenAt(t, tools.store, "Mallory Known", "Mal", ZoneKnown)
		if _, err := modelSave(tools, `{"name":"Mallory Known","given_name":"Molly"}`, false); err != nil {
			t.Errorf("save = %v, want a known contact's given name to change", err)
		}
	})
}

// TestApplyContactSave_StaleGivenNameAborts pins the given name in the
// save's snapshot recheck: an operator given-name edit between the read
// and the write aborts the save, so the snapshot re-upsert never reverts
// it and the target rule never judges a change against a stale value.
func TestApplyContactSave_StaleGivenNameAborts(t *testing.T) {
	store := newTestStore(t)
	c, err := store.Upsert(&Contact{FormattedName: "Dr. A. Jones", GivenName: "Alice", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.GetWithProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := store.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	edited.GivenName = "Alicia"
	if _, err := store.Upsert(edited); err != nil {
		t.Fatal(err)
	}
	stale.Note = "stale note"
	guard := identityGuard{snapshotZone: ZoneKnown, snapshotNickname: stale.Nickname, snapshotGiven: stale.GivenName}
	if _, _, err := store.applyContactSave(stale, true, nil, nil, guard); !errors.Is(err, errContactChangedConcurrently) {
		t.Fatalf("stale given name save error = %v, want errContactChangedConcurrently", err)
	}
	if got, err := store.Get(c.ID); err != nil || got.GivenName != "Alicia" || got.Note != "" {
		t.Errorf("aborted save changed the record: %+v, %v", got, err)
	}
}

// TestImportVCF_MergeIntoGivenName pins the import's snapshot: a card
// merged into a contact that has a given name passes the recheck in its
// write, so the given name is read into the guard as the save reads it.
func TestImportVCF_MergeIntoGivenName(t *testing.T) {
	tools := newTestTools(t)
	jones := seedGivenAt(t, tools.store, "Dr. A. Jones", "Alice", ZoneKnown)
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Dr. A. Jones\r\nEMAIL:alice.jones@example.org\r\nEND:VCARD\r\n"
	args, err := json.Marshal(map[string]string{"text": card})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.ImportVCF(string(args)); err != nil {
		t.Fatalf("import = %v", err)
	}
	got, err := tools.store.GetWithProperties(jones.ID)
	if err != nil {
		t.Fatal(err)
	}
	merged := false
	for _, p := range got.Properties {
		if p.Property == identityEmail && strings.EqualFold(p.Value, "alice.jones@example.org") {
			merged = true
		}
	}
	if !merged || got.GivenName != "Alice" {
		t.Errorf("merged record = given %q, properties %+v; want the email merged and Alice kept", got.GivenName, got.Properties)
	}
}
