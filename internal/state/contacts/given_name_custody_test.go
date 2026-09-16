package contacts

import (
	"database/sql"
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

// loggedLine reports whether one line of logs carries every want.
func loggedLine(logs string, wants ...string) bool {
	for _, line := range strings.Split(logs, "\n") {
		all := true
		for _, want := range wants {
			if !strings.Contains(line, want) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestImportVCF_GivenNameFillIsTargetCustody pins the import's side of
// the given-name target rule. A merge fills an empty given name, and a
// given name is a name lookups find the record by, so a card merged
// into a contact above known, or into the operator's own, leaves the
// fill out, counts it and logs it, as it does a nickname fill, while the
// rest of the merge lands. A known target takes the fill, a refused
// nickname fill leaves a known target's given-name fill alone, and a
// card that creates a contact keeps its given name, which is no claim on
// a new record, as on contact_save.
func TestImportVCF_GivenNameFillIsTargetCustody(t *testing.T) {
	const (
		givenCard    = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dr. A. Jones\r\nN:Jones;Alice;;;\r\nORG:Acme\r\nEND:VCARD\r\n"
		bothCard     = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dr. A. Jones\r\nN:Jones;Alice;;;\r\nNICKNAME:Ally\r\nORG:Acme\r\nEND:VCARD\r\n"
		givenNote    = "1 given name(s) were not filled in on a merge"
		nicknameNote = "1 nickname(s) were not filled in on a merge"
	)
	for _, tc := range []struct {
		name     string
		zone     string
		operator bool // pin the target as the operator's own record
		card     string
		// rule is the custody rule the log names, or "" when the fill
		// lands.
		rule         string
		wantNickname string
		properties   string
		notes        []string
	}{
		{name: "household", zone: ZoneHousehold, card: givenCard, rule: IdentityReasonZone, properties: "GIVEN_NAME", notes: []string{givenNote}},
		{name: "trusted", zone: ZoneTrusted, card: givenCard, rule: IdentityReasonZone, properties: "GIVEN_NAME", notes: []string{givenNote}},
		{name: "admin", zone: ZoneAdmin, card: givenCard, rule: IdentityReasonZone, properties: "GIVEN_NAME", notes: []string{givenNote}},
		{name: "operator's own at known", zone: ZoneKnown, operator: true, card: givenCard, rule: IdentityReasonOperator, properties: "GIVEN_NAME", notes: []string{givenNote}},
		{name: "household with a nickname fill too", zone: ZoneHousehold, card: bothCard, rule: IdentityReasonZone, properties: "GIVEN_NAME,NICKNAME", notes: []string{givenNote, nicknameNote}},
		{name: "known takes both", zone: ZoneKnown, card: bothCard, wantNickname: "Ally"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := newTestTools(t)
			jones := seedContactAt(t, tools.store, "Dr. A. Jones", tc.zone)
			if tc.operator {
				tools.ConfigureOperatorContactID(jones.ID)
			} else {
				tools.ConfigureOperatorContactID(seedContactAt(t, tools.store, "Carol Operator", ZoneKnown).ID)
			}
			refused := tc.rule != ""
			logs := captureDefaultLog(t)

			dry := importText(t, tools, tc.card, map[string]any{"dry_run": true})
			if got, err := tools.store.Get(jones.ID); err != nil || got.GivenName != "" || got.Org != "" {
				t.Fatalf("dry run wrote %+v, %v", got, err)
			}
			out := importText(t, tools, tc.card, nil)
			for label, result := range map[string]string{"dry run": dry, "import": out} {
				for _, note := range []string{givenNote, nicknameNote} {
					want := false
					for _, n := range tc.notes {
						want = want || n == note
					}
					if strings.Contains(result, note) != want {
						t.Errorf("%s contains %q = %v, want %v:\n%s", label, note, !want, want, result)
					}
				}
			}
			if !strings.Contains(out, "1 merged, 0 skipped") {
				t.Errorf("import = %q, want the card merged", out)
			}

			got, err := tools.store.Get(jones.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantGiven := "Alice"
			if refused {
				wantGiven = ""
			}
			if got.GivenName != wantGiven || got.Nickname != tc.wantNickname || got.Org != "Acme" {
				t.Errorf("after merge = given %q nickname %q org %q, want given %q nickname %q and the org merged",
					got.GivenName, got.Nickname, got.Org, wantGiven, tc.wantNickname)
			}
			resolved, err := tools.store.ResolveContact("Alice")
			switch {
			case refused && !errors.Is(err, sql.ErrNoRows):
				t.Errorf("ResolveContact(Alice) = %+v, %v, want no contact to answer to it", resolved, err)
			case !refused && (err != nil || resolved.ID != jones.ID):
				t.Errorf("ResolveContact(Alice) = %+v, %v, want Dr. A. Jones", resolved, err)
			}

			applied := loggedLine(logs.String(), "tool=contact_import_vcf", "contact_id="+jones.ID.String(), "properties="+tc.properties, "dry_run=false")
			if refused && (!applied || !loggedLine(logs.String(), "properties="+tc.properties, "rule="+tc.rule, "dry_run=true")) {
				t.Errorf("log lacks the refusal (rule %s, properties %s) for the dry run and the import:\n%s", tc.rule, tc.properties, logs)
			}
			if !refused && strings.Contains(logs.String(), "contact identity custody refused") {
				t.Errorf("a known merge logged a refusal:\n%s", logs)
			}
		})
	}

	t.Run("a refused nickname fill leaves a known target's given-name fill", func(t *testing.T) {
		tools := newTestTools(t)
		seedNicknameAt(t, tools.store, "Jane Doe", "Mom", ZoneHousehold)
		dan := seedContactAt(t, tools.store, "Dan Known", ZoneKnown)
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dan Known\r\nN:Known;Daniel;;;\r\nNICKNAME:Mom\r\nORG:Acme\r\nEND:VCARD\r\n", nil)
		if !strings.Contains(out, nicknameNote) || strings.Contains(out, givenNote) {
			t.Errorf("import = %q, want only the nickname fill counted", out)
		}
		if got, err := tools.store.Get(dan.ID); err != nil || got.GivenName != "Daniel" || got.Nickname != "" || got.Org != "Acme" {
			t.Errorf("Dan Known after merge = %+v, %v, want given name Daniel, no nickname, org Acme", got, err)
		}
	})

	t.Run("a new contact keeps its given name", func(t *testing.T) {
		tools := newTestTools(t)
		seedGivenAt(t, tools.store, "Dr. A. Jones", "Alice", ZoneHousehold)
		out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Mallory New\r\nN:New;Alice;;;\r\nEND:VCARD\r\n", map[string]any{"merge": false})
		if !strings.Contains(out, "1 created") || strings.Contains(out, "not filled in") {
			t.Errorf("import = %q, want the card created with nothing left out", out)
		}
		got, err := tools.store.FindByName("Mallory New")
		if err != nil || got.GivenName != "Alice" {
			t.Errorf("Mallory New = %+v, %v, want given name Alice", got, err)
		}
	})
}

// TestImportVCF_GivenNameFillRace pins the recheck inside the card's
// write: a known merge target the operator promotes after the import
// judged its given-name fill, and before the card's write, writes
// nothing, so the fill never lands on a contact now above known.
func TestImportVCF_GivenNameFillRace(t *testing.T) {
	tools := newTestTools(t)
	// Ada holds a number rather than an address, so the card merges into
	// Kim by name, and the number's refusal is the log line the operator
	// write rides on.
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

	out := importText(t, tools, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Kim Known\r\nN:Known;Kimberly;;;\r\nTEL:+15550009999\r\nORG:Acme\r\nEND:VCARD\r\n", nil)
	if !strings.Contains(out, "0 merged, 1 skipped") || !strings.Contains(out, "1 card(s) were skipped, not merged") {
		t.Errorf("import = %q, want card 1 skipped with its cause", out)
	}
	got, err := tools.store.Get(kim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustZone != ZoneHousehold || got.GivenName != "" || got.Org != "" {
		t.Errorf("Kim after import = zone %q given %q org %q, want the promotion kept and nothing written", got.TrustZone, got.GivenName, got.Org)
	}
}
