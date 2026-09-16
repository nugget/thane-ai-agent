package contacts

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type forkSeed struct {
	name, nickname, given, zone string
	props                       []Property
	// ha binds the record to this Home Assistant person when set.
	ha string
}

func seedForkRecords(t *testing.T, store *Store, seeds []forkSeed) map[string]*Contact {
	t.Helper()
	out := make(map[string]*Contact, len(seeds))
	for _, s := range seeds {
		c, err := store.UpsertWithProperties(&Contact{
			FormattedName: s.name, Nickname: s.nickname, GivenName: s.given,
			Kind: "individual", TrustZone: s.zone,
		}, s.props)
		if err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
		if s.ha != "" {
			if err := store.SetHAPersonEntity(c.ID, s.ha); err != nil {
				t.Fatalf("bind %s: %v", s.name, err)
			}
		}
		out[s.name] = c
	}
	return out
}

func tel(value, typ string) Property {
	return Property{Property: "TEL", Value: value, Type: typ}
}

func auditForks(t *testing.T, store *Store, limit int) ContactForkAudit {
	t.Helper()
	audit, err := store.ContactForks(context.Background(), limit)
	if err != nil {
		t.Fatalf("ContactForks: %v", err)
	}
	return audit
}

func forkMemberNames(f ContactForkFinding) []string {
	names := make([]string, 0, len(f.Members))
	for _, m := range f.Members {
		names = append(names, m.Name)
	}
	return names
}

func identity(property, value string) []Property {
	return []Property{{Property: property, Value: value}}
}

// TestContactForks_Shapes pins which groups the audit reports: the two
// production shapes (a known record named with a household record's
// nickname, and one named with a household record's first word), the
// given-name short form, the operator at known, shared email addresses
// and phone equivalents, and negative controls for groups with no
// authority and for keys the audit does not treat as identity.
func TestContactForks_Shapes(t *testing.T) {
	tests := []struct {
		name        string
		seeds       []forkSeed
		pin         string
		wantKind    string // "" when nothing is reported
		wantKey     string
		wantMembers []string
		// wantTotal is the audit's total when it is more than the one
		// finding the row checks; the checked finding sorts first.
		wantTotal int
	}{
		{
			name:     "a known record named with a household record's nickname",
			seeds:    []forkSeed{{name: "Carol", zone: ZoneKnown}, {name: "Carol Household", nickname: "Carol", zone: ZoneHousehold}},
			wantKind: ForkKindName, wantKey: "carol", wantMembers: []string{"Carol Household", "Carol"},
		},
		{
			name:     "a known record named with the first word of a household record's name",
			seeds:    []forkSeed{{name: "Dave", zone: ZoneKnown}, {name: "Dave Household", zone: ZoneHousehold}},
			wantKind: ForkKindName, wantKey: "dave", wantMembers: []string{"Dave Household", "Dave"},
		},
		{
			name:     "a known record named with a household record's given name",
			seeds:    []forkSeed{{name: "Erin", zone: ZoneKnown}, {name: "Ms Erin Household", given: "Erin", zone: ZoneHousehold}},
			wantKind: ForkKindName, wantKey: "erin", wantMembers: []string{"Ms Erin Household", "Erin"},
		},
		{
			name:     "the operator's record counts as authority at known",
			seeds:    []forkSeed{{name: "Heidi", zone: ZoneKnown}, {name: "Heidi Operator", nickname: "Heidi", zone: ZoneKnown}},
			pin:      "Heidi Operator",
			wantKind: ForkKindName, wantKey: "heidi", wantMembers: []string{"Heidi Operator", "Heidi"},
		},
		{
			name: "an email address shared in any case",
			seeds: []forkSeed{
				{name: "Ivan Household", zone: ZoneHousehold, props: identity("EMAIL", "Ivan@IvanMail.net")},
				{name: "Judy Known", zone: ZoneKnown, props: identity("EMAIL", "ivan@ivanmail.net")},
			},
			wantKind: ForkKindEmail, wantKey: "ivan@ivanmail.net", wantMembers: []string{"Ivan Household", "Judy Known"},
		},
		{
			name: "a TEL with '+' and an IMPP signal: without",
			seeds: []forkSeed{
				{name: "Ken Household", zone: ZoneHousehold, props: identity("TEL", "+15551230001")},
				{name: "Leo Known", zone: ZoneKnown, props: identity("IMPP", "signal:15551230001")},
			},
			wantKind: ForkKindPhone, wantKey: "15551230001", wantMembers: []string{"Ken Household", "Leo Known"},
		},
		{
			name: "a TEL without '+' and an IMPP signal: with",
			seeds: []forkSeed{
				{name: "Mia Trusted", zone: ZoneTrusted, props: identity("TEL", "15551230002")},
				{name: "Ned Known", zone: ZoneKnown, props: identity("IMPP", "signal:+15551230002")},
			},
			wantKind: ForkKindPhone, wantKey: "15551230002", wantMembers: []string{"Mia Trusted", "Ned Known"},
		},
		{
			name:  "two known records sharing a name are not reported",
			seeds: []forkSeed{{name: "Frank", zone: ZoneKnown}, {name: "Frank Known", nickname: "Frank", zone: ZoneKnown}},
		},
		{
			name: "two known records sharing an email address are not reported",
			seeds: []forkSeed{
				{name: "Olga Known", zone: ZoneKnown, props: identity("EMAIL", "olga@olgamail.net")},
				{name: "Pat Known", zone: ZoneKnown, props: identity("EMAIL", "olga@olgamail.net")},
			},
		},
		{
			name:  "a shared first word is not a fork without a whole formatted name",
			seeds: []forkSeed{{name: "Grace One", zone: ZoneHousehold}, {name: "Grace Two", zone: ZoneKnown}},
		},
		{
			name: "a non-signal IMPP is not a phone number",
			seeds: []forkSeed{
				{name: "Quinn Household", zone: ZoneHousehold, props: identity("IMPP", "matrix:@quinn:chat.net")},
				{name: "Rita Known", zone: ZoneKnown, props: identity("IMPP", "matrix:@quinn:chat.net")},
			},
		},

		// Corroboration: a shared name key is a fork only when the
		// records suggest one person.
		{
			name: "a first-name-only known contact with its own number is not a household record's first word",
			seeds: []forkSeed{
				{name: "Dave", zone: ZoneKnown, props: identity("TEL", "+15551110001")},
				{name: "Dave Household", zone: ZoneHousehold},
			},
		},
		{
			name: "a first-name-only known contact with its own address is not the operator's given name",
			seeds: []forkSeed{
				{name: "Alice Rivera", given: "Alice", zone: ZoneAdmin, props: identity("EMAIL", "alice@riveramail.net")},
				{name: "Alice", zone: ZoneKnown, props: identity("EMAIL", "alice@coworker.net")},
			},
			pin: "Alice Rivera",
		},
		{
			name: "a shared number corroborates a first-word name",
			seeds: []forkSeed{
				{name: "Dave", zone: ZoneKnown, props: identity("TEL", "+15551110002")},
				{name: "Dave Household", zone: ZoneHousehold, props: identity("IMPP", "signal:+15551110002")},
			},
			// The shared number is its own phone finding too; the name
			// finding sorts first.
			wantKind: ForkKindName, wantKey: "dave", wantMembers: []string{"Dave Household", "Dave"}, wantTotal: 2,
		},
		{
			name: "a known contact bound to a Home Assistant person corroborates a first-word name",
			seeds: []forkSeed{
				{name: "Dave", zone: ZoneKnown, props: identity("TEL", "+15551110003"), ha: "person.dave"},
				{name: "Dave Household", zone: ZoneHousehold, props: identity("TEL", "+15551110004")},
			},
			wantKind: ForkKindName, wantKey: "dave", wantMembers: []string{"Dave Household", "Dave"},
		},
		{
			name: "two people with authority sharing a nickname, each with a number, are a shared name",
			seeds: []forkSeed{
				{name: "Jane Rivera", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551110005")},
				{name: "Mary Smith", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551110006")},
			},
			wantKind: ForkKindSharedName, wantKey: "mom", wantMembers: []string{"Jane Rivera", "Mary Smith"},
		},
		{
			name: "a known contact with its own number named with a household nickname is a shared name",
			seeds: []forkSeed{
				{name: "Carol", zone: ZoneKnown, props: identity("TEL", "+15551110007")},
				{name: "Carol Household", nickname: "Carol", zone: ZoneHousehold, props: identity("TEL", "+15551110008")},
			},
			wantKind: ForkKindSharedName, wantKey: "carol", wantMembers: []string{"Carol Household", "Carol"},
		},

		// Email: a mailbox shared by records with authority that each
		// hold their own addresses is a shared mailbox, not a fork.
		{
			name: "a family mailbox of the operator and a household record with its own number is not reported",
			seeds: []forkSeed{
				{name: "Alice Rivera", zone: ZoneAdmin, props: identity("EMAIL", "family@riveramail.net")},
				{name: "Bob Rivera", zone: ZoneHousehold, props: []Property{{Property: "EMAIL", Value: "family@riveramail.net"}, tel("+15551110009", "cell")}},
			},
			pin: "Alice Rivera",
		},
		{
			name: "a record above known holding only the operator's address is reported",
			seeds: []forkSeed{
				{name: "Alice Rivera", zone: ZoneAdmin, props: []Property{{Property: "EMAIL", Value: "alice@riveramail.net"}, tel("+15551110010", "cell")}},
				{name: "Erin Copy", zone: ZoneTrusted, props: identity("EMAIL", "alice@riveramail.net")},
			},
			pin:      "Alice Rivera",
			wantKind: ForkKindEmail, wantKey: "alice@riveramail.net", wantMembers: []string{"Alice Rivera", "Erin Copy"},
		},

		// Phone: shared lines are not identity.
		{
			name: "a landline typed home on three household records is not reported",
			seeds: []forkSeed{
				{name: "Alice Rivera", zone: ZoneAdmin, props: []Property{tel("+15559990000", "home,voice")}},
				{name: "Bob Rivera", zone: ZoneHousehold, props: []Property{tel("15559990000", "HOME")}},
				{name: "Carol Rivera", zone: ZoneHousehold, props: []Property{tel("+15559990000", "home")}},
			},
		},
		{
			name: "an office main number shared by an employee and the office is not reported",
			seeds: []forkSeed{
				{name: "Erin Park", zone: ZoneTrusted, props: []Property{tel("+15554440000", "work")}},
				{name: "Rivera Dental", zone: ZoneKnown, props: []Property{tel("+15554440000", "main")}},
			},
		},
		{
			name: "a voice-only TEL is a shared line",
			seeds: []forkSeed{
				{name: "Frank Household", zone: ZoneHousehold, props: []Property{tel("+15554440001", "voice,pref")}},
				{name: "Gina Known", zone: ZoneKnown, props: []Property{tel("+15554440001", "VOICE")}},
			},
		},
		{
			name: "a home TEL counts once another holder has the number as signal:",
			seeds: []forkSeed{
				{name: "Frank Household", zone: ZoneHousehold, props: []Property{tel("+15554440002", "home")}},
				{name: "Gina Known", zone: ZoneKnown, props: identity("IMPP", "signal:+15554440002")},
			},
			wantKind: ForkKindPhone, wantKey: "15554440002", wantMembers: []string{"Frank Household", "Gina Known"},
		},
		{
			name: "a TEL typed cell among other types counts",
			seeds: []forkSeed{
				{name: "Hal Household", zone: ZoneHousehold, props: []Property{tel("+15554440003", "CELL,VOICE,pref")}},
				{name: "Ian Known", zone: ZoneKnown, props: []Property{tel("15554440003", "iphone")}},
			},
			wantKind: ForkKindPhone, wantKey: "15554440003", wantMembers: []string{"Hal Household", "Ian Known"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			records := seedForkRecords(t, store, tt.seeds)
			if tt.pin != "" {
				store.pinOperatorContactID(records[tt.pin].ID)
			}
			audit := auditForks(t, store, 10)
			if tt.wantKind == "" {
				if audit.Total != 0 || len(audit.Findings) != 0 {
					t.Fatalf("audit = %+v, want no findings", audit)
				}
				return
			}
			wantTotal := max(tt.wantTotal, 1)
			if audit.Total != wantTotal || len(audit.Findings) != wantTotal {
				t.Fatalf("audit = %+v, want %d findings", audit, wantTotal)
			}
			f := audit.Findings[0]
			if f.Kind != tt.wantKind || f.Key != tt.wantKey {
				t.Errorf("finding = %s %q, want %s %q", f.Kind, f.Key, tt.wantKind, tt.wantKey)
			}
			if got := forkMemberNames(f); !reflect.DeepEqual(got, tt.wantMembers) || f.MemberCount != len(tt.wantMembers) {
				t.Errorf("members = %q (count %d), want %q", got, f.MemberCount, tt.wantMembers)
			}
			for _, m := range f.Members {
				if m.ContactID != records[m.Name].ID || m.TrustZone != records[m.Name].TrustZone {
					t.Errorf("member %s = %+v, want id %s at %s", m.Name, m, records[m.Name].ID, records[m.Name].TrustZone)
				}
				if m.Operator != (m.Name == tt.pin) {
					t.Errorf("member %s operator = %v, want %v", m.Name, m.Operator, m.Name == tt.pin)
				}
			}
		})
	}
}

// TestContactForks_MemberDetail pins what a finding says about each
// record, the Home Assistant person binding included, and that a
// forgotten duplicate no longer counts.
func TestContactForks_MemberDetail(t *testing.T) {
	store := newTestStore(t)
	household := seedNicknameAt(t, store, "Carol Household", "Carol", ZoneHousehold)
	if err := store.SetHAPersonEntity(household.ID, "person.carol"); err != nil {
		t.Fatal(err)
	}
	known := seedContactAt(t, store, "Carol", ZoneKnown)

	want := ContactForkFinding{Kind: ForkKindName, Key: "carol", MemberCount: 2, Members: []ContactForkMember{
		{ContactID: household.ID, Name: "Carol Household", TrustZone: ZoneHousehold, HAPersonEntity: "person.carol"},
		{ContactID: known.ID, Name: "Carol", TrustZone: ZoneKnown},
	}}
	audit := auditForks(t, store, 10)
	if audit.Total != 1 || !reflect.DeepEqual(audit.Findings[0], want) {
		t.Fatalf("audit = %+v, want %+v", audit, want)
	}

	if deleted, err := store.deleteIfUncustodied(context.Background(), known.ID); err != nil || !deleted {
		t.Fatalf("forget the duplicate: %v, %v", deleted, err)
	}
	if audit := auditForks(t, store, 10); audit.Total != 0 {
		t.Errorf("after the duplicate is forgotten, audit = %+v, want none", audit)
	}
}

// TestContactForks_PlaceholderDuplicateOfTheOperator pins the shape
// #1545 was filed on: a record above known whose formatted name is the
// operator's nickname and whose only address is a placeholder. Both
// carry authority, and the placeholder is no address of its own, so the
// name is a fork and the placeholder is its own finding.
func TestContactForks_PlaceholderDuplicateOfTheOperator(t *testing.T) {
	store := newTestStore(t)
	records := seedForkRecords(t, store, []forkSeed{
		{name: "Bob Operator", nickname: "Bob", zone: ZoneAdmin, props: identity("EMAIL", "bob@bobmail.net")},
		{name: "Bob", zone: ZoneAdmin, props: identity("EMAIL", "bob@example.com")},
	})
	store.pinOperatorContactID(records["Bob Operator"].ID)
	audit := auditForks(t, store, 10)
	var got []string
	for _, f := range audit.Findings {
		got = append(got, f.Kind+":"+f.Key)
	}
	if want := []string{"name:bob", "reserved_domain:bob@example.com"}; audit.Total != len(want) || !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %q (total %d), want %q", got, audit.Total, want)
	}
	if m := audit.Findings[0].Members; m[0].ContactID != records["Bob Operator"].ID || !m[0].Operator {
		t.Errorf("name finding members = %+v, want the operator's record first", m)
	}
}

// TestContactForks_ReservedDomains pins the placeholder guard: an EMAIL
// on a domain RFC 2606 or RFC 6761 reserves is reported on a record
// above known or the operator's own, and never on an ordinary known
// record or on a domain that only looks reserved.
func TestContactForks_ReservedDomains(t *testing.T) {
	tests := []struct {
		name, zone, address string
		operator            bool
		want                bool
	}{
		{name: "example.com on admin", zone: ZoneAdmin, address: "alice@example.com", want: true},
		{name: "example.net in any case on household", zone: ZoneHousehold, address: "bob@Example.NET", want: true},
		{name: "example.org on trusted", zone: ZoneTrusted, address: "carol@example.org", want: true},
		{name: "a name under example.com", zone: ZoneAdmin, address: "dave@mail.example.com", want: true},
		{name: "the .example TLD", zone: ZoneAdmin, address: "erin@corp.example", want: true},
		{name: "the .test TLD", zone: ZoneAdmin, address: "frank@host.test", want: true},
		{name: "the .invalid TLD", zone: ZoneAdmin, address: "grace@nowhere.invalid", want: true},
		{name: "localhost", zone: ZoneAdmin, address: "heidi@localhost", want: true},
		{name: "a trailing root dot", zone: ZoneAdmin, address: "ivan@example.com.", want: true},
		{name: "the operator at known", zone: ZoneKnown, address: "ken@example.com", operator: true, want: true},
		{name: "an ordinary known record", zone: ZoneKnown, address: "judy@example.com", want: false},
		{name: "a domain ending in example.com", zone: ZoneAdmin, address: "leo@notexample.com", want: false},
		{name: "example.co", zone: ZoneAdmin, address: "mia@example.co", want: false},
		{name: "a domain starting with test", zone: ZoneAdmin, address: "ned@testing.net", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			c := seedContactAt(t, store, "Mallory Placeholder", tt.zone, Property{Property: "EMAIL", Value: tt.address})
			if tt.operator {
				store.pinOperatorContactID(c.ID)
			}
			audit := auditForks(t, store, 10)
			if !tt.want {
				if audit.Total != 0 {
					t.Errorf("audit = %+v, want none", audit)
				}
				return
			}
			if audit.Total != 1 || len(audit.Findings) != 1 {
				t.Fatalf("audit = %+v, want one finding", audit)
			}
			f := audit.Findings[0]
			if f.Kind != ForkKindReservedDomain || f.Key != tt.address || f.MemberCount != 1 ||
				f.Members[0].ContactID != c.ID || f.Members[0].Operator != tt.operator {
				t.Errorf("finding = %+v, want a reserved-domain finding for %s on %s", f, tt.address, c.ID)
			}
		})
	}
}

// TestContactForks_OrderAndBounds pins the report's order (name, email,
// phone, reserved domain, each by key), that the limit keeps a prefix
// while Total counts everything, and that one finding names at most
// maxForkMembers records, authority first, while counting them all.
func TestContactForks_OrderAndBounds(t *testing.T) {
	store := newTestStore(t)
	seedContactAt(t, store, "Ken Household", ZoneHousehold, Property{Property: "TEL", Value: "+15551230001"})
	seedContactAt(t, store, "Leo Known", ZoneKnown, Property{Property: "IMPP", Value: "signal:15551230001"})
	seedContactAt(t, store, "Ivan Household", ZoneHousehold, Property{Property: "EMAIL", Value: "ivan@ivanmail.net"})
	seedContactAt(t, store, "Judy Known", ZoneKnown, Property{Property: "EMAIL", Value: "ivan@ivanmail.net"})
	seedContactAt(t, store, "Mallory Admin", ZoneAdmin, Property{Property: "EMAIL", Value: "mallory@example.com"})
	seedNicknameAt(t, store, "Zed Household", "Zed", ZoneHousehold)
	seedContactAt(t, store, "Zed", ZoneKnown)
	seedNicknameAt(t, store, "Amy Household", "Amy", ZoneHousehold)
	seedContactAt(t, store, "Amy", ZoneKnown)
	seedNicknameAt(t, store, "Jane Rivera", "Mom", ZoneTrusted, Property{Property: "TEL", Value: "+15551110005"})
	seedNicknameAt(t, store, "Mary Smith", "Mom", ZoneTrusted, Property{Property: "TEL", Value: "+15551110006"})

	full := auditForks(t, store, 100)
	var order []string
	for _, f := range full.Findings {
		order = append(order, f.Kind+":"+f.Key)
	}
	wantOrder := []string{"name:amy", "name:zed", "shared_name:mom", "email:ivan@ivanmail.net", "phone:15551230001", "reserved_domain:mallory@example.com"}
	if full.Total != len(wantOrder) || !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %q (total %d), want %q", order, full.Total, wantOrder)
	}

	prefix := auditForks(t, store, 2)
	if prefix.Total != len(wantOrder) || len(prefix.Findings) != 2 || prefix.Findings[1].Key != "zed" {
		t.Errorf("limit 2 = %+v, want the first two findings and a total of %d", prefix, len(wantOrder))
	}
	for _, limit := range []int{0, -1} {
		if none := auditForks(t, store, limit); none.Total != len(wantOrder) || len(none.Findings) != 0 {
			t.Errorf("limit %d = %+v, want no findings and a total of %d", limit, none, len(wantOrder))
		}
	}

	t.Run("one crowded key names at most maxForkMembers records", func(t *testing.T) {
		store := newTestStore(t)
		household := seedNicknameAt(t, store, "Quinn Household", "Q", ZoneHousehold)
		for i := range 10 {
			seedNicknameAt(t, store, fmt.Sprintf("Quinn Known %02d", i), "q", ZoneKnown)
		}
		audit := auditForks(t, store, 10)
		if audit.Total != 1 {
			t.Fatalf("audit = %+v, want one finding", audit)
		}
		f := audit.Findings[0]
		if f.MemberCount != 11 || len(f.Members) != maxForkMembers || f.Members[0].ContactID != household.ID {
			t.Errorf("finding = count %d, %d members, first %s; want count 11, %d members, the household record first",
				f.MemberCount, len(f.Members), f.Members[0].Name, maxForkMembers)
		}
	})
}
