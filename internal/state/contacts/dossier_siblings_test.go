package contacts

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeDossierPresence answers [Tools.ConfigureDossierPresence] from a
// set of refs.
type fakeDossierPresence struct {
	docs map[string]bool
	err  error
}

func (p *fakeDossierPresence) exists(_ context.Context, ref string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.docs[ref], nil
}

func validDossierArgs(c *Contact) DossierWriteArgs {
	return DossierWriteArgs{
		ContactID:  c.ID.String(),
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversation sharpened the collaboration picture.",
		Digest:     "The contact prefers direct technical collaboration and explicit boundaries.",
		Full:       "### Working style\n\nCurrent synthesis with cited evidence.",
	}
}

// TestWriteDossier_SecondDossierRefusal pins the refusal: the first
// write of a dossier is refused when a name sibling that is likely the
// same person already has one, and the advice follows authority (merge
// into a holder with as much authority, fold a known duplicate's
// dossier into the record above known, report a holder only the
// operator can remove). Name siblings with no sign of being one person
// each get their own dossier, and an update is never refused.
func TestWriteDossier_SecondDossierRefusal(t *testing.T) {
	carolPair := []forkSeed{{name: "Carol", zone: ZoneKnown}, {name: "Carol Household", nickname: "Carol", zone: ZoneHousehold}}
	mergeInto := func(holder string) []string {
		return []string{"If they are one person, write what you meant into the existing dossier with contact_dossier_write contact_id " + holder}
	}
	tests := []struct {
		name     string
		seeds    []forkSeed
		pin      string
		target   string
		dossiers []string
		// wantHeld names the sibling whose dossier refuses the create,
		// or is "" when the create is allowed.
		wantHeld   string
		wantAdvice func(held *Contact) []string
	}{
		{
			name: "an empty known duplicate of a household nickname merges into the household dossier", seeds: carolPair,
			target: "Carol", dossiers: []string{"Carol Household"}, wantHeld: "Carol Household",
			wantAdvice: func(held *Contact) []string {
				return append(mergeInto(held.ID.String()), "Carol holds no address or number of its own", "report the duplicate record to the operator")
			},
		},
		{
			name:   "an empty known duplicate of a household first word merges into the household dossier",
			seeds:  []forkSeed{{name: "Dave", zone: ZoneKnown}, {name: "Dave Household", zone: ZoneHousehold}},
			target: "Dave", dossiers: []string{"Dave Household"}, wantHeld: "Dave Household",
			wantAdvice: func(held *Contact) []string { return mergeInto(held.ID.String()) },
		},
		{
			name:   "a shared number is evidence even when both records hold numbers",
			seeds:  []forkSeed{{name: "Dave", zone: ZoneKnown, props: identity("TEL", "+15551110002")}, {name: "Dave Household", zone: ZoneHousehold, props: identity("IMPP", "signal:15551110002")}},
			target: "Dave", dossiers: []string{"Dave Household"}, wantHeld: "Dave Household",
			wantAdvice: func(held *Contact) []string { return append(mergeInto(held.ID.String()), "both hold +15551110002") },
		},
		{
			name: "the household record folds in a known duplicate's dossier rather than merging into it", seeds: carolPair,
			target: "Carol Household", dossiers: []string{"Carol"}, wantHeld: "Carol",
			wantAdvice: func(held *Contact) []string {
				id := held.ID.String()
				return []string{"this contact carries more authority and should keep the dossier",
					"contact_dossier_read contact_id " + id, "contact_forget contact_id " + id, "then write this contact's first dossier"}
			},
		},
		{
			name:   "a known duplicate bound to a Home Assistant person is reported, not forgotten",
			seeds:  []forkSeed{{name: "Carol", zone: ZoneKnown, ha: "person.carol"}, {name: "Carol Household", nickname: "Carol", zone: ZoneHousehold}},
			target: "Carol Household", dossiers: []string{"Carol"}, wantHeld: "Carol",
			wantAdvice: func(*Contact) []string {
				return []string{"report the duplicate to the operator: Carol is bound to Home Assistant person person.carol", "only the operator can remove it"}
			},
		},
		{
			name:   "a duplicate above known holding the dossier beside the operator is reported",
			seeds:  []forkSeed{{name: "Bob Operator", nickname: "Bob", zone: ZoneAdmin, props: identity("EMAIL", "bob@bobmail.net")}, {name: "Bob", zone: ZoneAdmin, props: identity("EMAIL", "bob@example.com")}},
			pin:    "Bob Operator",
			target: "Bob Operator", dossiers: []string{"Bob"}, wantHeld: "Bob",
			wantAdvice: func(*Contact) []string {
				return []string{"Bob holds no address or number of its own", "report the duplicate to the operator: Bob is above known"}
			},
		},
		{
			name:   "two empty known name siblings are refused as well",
			seeds:  []forkSeed{{name: "Frank", zone: ZoneKnown}, {name: "Frank Known", nickname: "Frank", zone: ZoneKnown}},
			target: "Frank", dossiers: []string{"Frank Known"}, wantHeld: "Frank Known",
			wantAdvice: func(held *Contact) []string { return mergeInto(held.ID.String()) },
		},

		// Negative controls: different people who share a name.
		{
			name:   "a first-name-only known contact with its own number gets its own dossier beside a trusted first name",
			seeds:  []forkSeed{{name: "Oscar", zone: ZoneKnown, props: identity("TEL", "+15551110011")}, {name: "Oscar Brown", zone: ZoneTrusted}},
			target: "Oscar", dossiers: []string{"Oscar Brown"},
		},
		{
			name:   "a sparse trusted record gets its own dossier beside a known contact with its own number",
			seeds:  []forkSeed{{name: "Oscar", zone: ZoneKnown, props: identity("TEL", "+15551110011")}, {name: "Oscar Brown", zone: ZoneTrusted}},
			target: "Oscar Brown", dossiers: []string{"Oscar"},
		},
		{
			name:   "a coworker saved by first name gets a dossier beside the operator's",
			seeds:  []forkSeed{{name: "Alice Rivera", given: "Alice", zone: ZoneAdmin, props: identity("EMAIL", "alice@riveramail.net")}, {name: "Alice", zone: ZoneKnown, props: identity("EMAIL", "alice@coworker.net")}},
			pin:    "Alice Rivera",
			target: "Alice", dossiers: []string{"Alice Rivera"},
		},
		{
			name: "two people with authority sharing a nickname each get a dossier",
			seeds: []forkSeed{
				{name: "Jane Rivera", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551110005")},
				{name: "Mary Smith", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551110006")},
			},
			target: "Jane Rivera", dossiers: []string{"Mary Smith"},
		},
		{
			name: "no sibling dossier allows the create", seeds: carolPair,
			target: "Carol",
		},
		{
			name: "an update is never refused", seeds: carolPair,
			target: "Carol", dossiers: []string{"Carol", "Carol Household"},
		},
		{
			name:   "an unrelated record's dossier does not refuse",
			seeds:  []forkSeed{{name: "Carol", zone: ZoneKnown}, {name: "Grace Household", zone: ZoneHousehold}},
			target: "Carol", dossiers: []string{"Grace Household"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := newTestTools(t)
			records := seedForkRecords(t, tools.store, tt.seeds)
			if tt.pin != "" {
				tools.ConfigureOperatorContactID(records[tt.pin].ID)
			}
			presence := &fakeDossierPresence{docs: map[string]bool{}}
			for _, name := range tt.dossiers {
				presence.docs[DossierRef(records[name].ID)] = true
			}
			writer := &recordingDossierWriter{}
			tools.ConfigureDossierRoot(true, true)
			tools.ConfigureDossierDocuments(nil, writer.Write)
			tools.ConfigureDossierPresence(presence.exists)

			target := records[tt.target]
			_, err := tools.WriteDossier(context.Background(), validDossierArgs(target))
			if tt.wantHeld == "" {
				if err != nil || writer.calls != 1 {
					t.Fatalf("WriteDossier() = %v, calls %d; want one write", err, writer.calls)
				}
				return
			}
			held := records[tt.wantHeld]
			wants := append([]string{
				"contact_dossier_write refused to start a second dossier",
				target.ID.String(), held.ID.String(), DossierRef(held.ID),
				"so they are likely one person saved twice",
				"If they are different people, write nothing and report both records to the operator",
				"Nothing was written",
			}, tt.wantAdvice(held)...)
			requireContains(t, err, wants...)
			if !errors.Is(err, ErrDossierTargetRefused) {
				t.Errorf("the refusal of a second dossier does not read as a refusal of the contact named: %v", err)
			}
			if tt.wantHeld != tt.target && strings.Contains(err.Error(), "write what you meant into the existing dossier") &&
				records[tt.wantHeld].TrustZone == ZoneKnown && target.TrustZone != ZoneKnown {
				t.Errorf("a record above known was told to merge into a known duplicate's dossier: %v", err)
			}
			if writer.calls != 0 {
				t.Errorf("a refused create reached the writer %d times", writer.calls)
			}
		})
	}

	t.Run("without a presence check nothing is refused", func(t *testing.T) {
		tools := newTestTools(t)
		records := seedForkRecords(t, tools.store, carolPair)
		writer := &recordingDossierWriter{}
		tools.ConfigureDossierRoot(true, true)
		tools.ConfigureDossierDocuments(nil, writer.Write)
		if _, err := tools.WriteDossier(context.Background(), validDossierArgs(records["Carol"])); err != nil || writer.calls != 1 {
			t.Errorf("WriteDossier() = %v, calls %d; want one write", err, writer.calls)
		}
	})

	t.Run("a failed presence check writes nothing", func(t *testing.T) {
		tools := newTestTools(t)
		records := seedForkRecords(t, tools.store, carolPair)
		writer := &recordingDossierWriter{}
		tools.ConfigureDossierRoot(true, true)
		tools.ConfigureDossierDocuments(nil, writer.Write)
		tools.ConfigureDossierPresence((&fakeDossierPresence{err: errors.New("document index offline")}).exists)
		_, err := tools.WriteDossier(context.Background(), validDossierArgs(records["Carol"]))
		requireContains(t, err, "already has a dossier", "document index offline")
		if errors.Is(err, ErrDossierTargetRefused) {
			t.Errorf("a failed check reads as a refusal of the contact named: %v", err)
		}
		if writer.calls != 0 {
			t.Errorf("a failed check reached the writer %d times", writer.calls)
		}
	})
}
