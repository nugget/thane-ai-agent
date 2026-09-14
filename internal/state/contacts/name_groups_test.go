package contacts

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestContactForks_NameGroups pins how a name key's records are read as
// people: a thin copy, at known or above it, joins the person it copies
// and never ties two people into one, a copy that could be either of two
// people is listed with each, and people who share a name with no
// evidence between them are one shared-name finding naming a record per
// person, where a copy of several people stands for itself only while
// one of them is not named. Each row lists every finding the audit
// reports, as kind:key:members with the members sorted, in any order.
func TestContactForks_NameGroups(t *testing.T) {
	tests := []struct {
		name  string
		pin   string
		seeds []forkSeed
		want  []string
	}{
		{
			name: "an unrelated person who answers to the name is not in the duplicate's finding",
			seeds: []forkSeed{
				{name: "Carol Rivera", nickname: "Carol", zone: ZoneHousehold, props: identity("TEL", "+15551120001")},
				{name: "Carol", zone: ZoneKnown},
				{name: "Carol Park", nickname: "Carol", zone: ZoneKnown, props: identity("TEL", "+15551120002")},
			},
			want: []string{"name:carol:Carol,Carol Rivera", "shared_name:carol:Carol Park,Carol Rivera"},
		},
		{
			name: "a thin copy beside two people with authority is listed with each and ties neither to the other",
			seeds: []forkSeed{
				{name: "Heidi Rivera", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551120003")},
				{name: "Frank Smith", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551120004")},
				{name: "Mom", zone: ZoneKnown},
			},
			want: []string{"name:mom:Heidi Rivera,Mom", "name:mom:Frank Smith,Mom", "shared_name:mom:Frank Smith,Heidi Rivera"},
		},
		{
			name: "a thin copy above known ties neither of two people to the other",
			seeds: []forkSeed{
				{name: "Carol Rivera", nickname: "Carol", zone: ZoneHousehold, props: identity("TEL", "+15551120011")},
				{name: "Carol", zone: ZoneTrusted},
				{name: "Carol Park", nickname: "Carol", zone: ZoneTrusted, props: identity("TEL", "+15551120012")},
			},
			want: []string{"name:carol:Carol,Carol Rivera", "name:carol:Carol,Carol Park", "shared_name:carol:Carol Park,Carol Rivera"},
		},
		{
			name: "a placeholder copy of the operator ties no one else to the operator",
			pin:  "Bob Operator",
			seeds: []forkSeed{
				{name: "Bob Operator", nickname: "Bob", zone: ZoneAdmin, props: identity("EMAIL", "bob@bobmail.net")},
				{name: "Bob", zone: ZoneAdmin, props: identity("EMAIL", "bob@example.com")},
				{name: "Bob Smith", nickname: "Bob", zone: ZoneTrusted, props: identity("TEL", "+15551120013")},
			},
			want: []string{"name:bob:Bob,Bob Operator", "name:bob:Bob,Bob Smith", "reserved_domain:bob@example.com:Bob", "shared_name:bob:Bob Operator,Bob Smith"},
		},
		{
			name: "two thin records above known are one person",
			seeds: []forkSeed{
				{name: "Erin", zone: ZoneHousehold},
				{name: "Erin Household", nickname: "Erin", zone: ZoneHousehold},
			},
			want: []string{"name:erin:Erin,Erin Household"},
		},
		{
			name: "a copy of two people who are both named is not a third",
			seeds: []forkSeed{
				{name: "Alice Rivera", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551120014")},
				{name: "Bob Smith", nickname: "Mom", zone: ZoneTrusted, props: identity("EMAIL", "bob@smithmail.net")},
				{name: "Mom", zone: ZoneKnown, props: []Property{{Property: "TEL", Value: "+15551120014"}, {Property: "EMAIL", Value: "bob@smithmail.net"}}},
				{name: "Carol Park", nickname: "Mom", zone: ZoneTrusted, props: identity("TEL", "+15551120015")},
			},
			want: []string{
				"name:mom:Alice Rivera,Mom", "name:mom:Bob Smith,Mom", "shared_name:mom:Alice Rivera,Bob Smith,Carol Park",
				"email:bob@smithmail.net:Bob Smith,Mom", "phone:15551120014:Alice Rivera,Mom",
			},
		},
		{
			name: "a copy of two people who are not named stands for itself",
			seeds: []forkSeed{
				{name: "Dave Rivera", zone: ZoneHousehold, props: identity("TEL", "+15551120016")},
				{name: "Dave Smith", zone: ZoneTrusted, props: identity("EMAIL", "dave@smithmail.net")},
				{name: "Dave", zone: ZoneKnown, props: []Property{{Property: "TEL", Value: "+15551120016"}, {Property: "EMAIL", Value: "dave@smithmail.net"}}},
				{name: "David Park", nickname: "Dave", zone: ZoneTrusted, props: identity("TEL", "+15551120017")},
			},
			want: []string{
				"name:dave:Dave,Dave Rivera", "name:dave:Dave,Dave Smith", "shared_name:dave:Dave,David Park",
				"email:dave@smithmail.net:Dave,Dave Smith", "phone:15551120016:Dave,Dave Rivera",
			},
		},
		{
			name: "a thin first name beside two people whose first word it is is listed with each",
			seeds: []forkSeed{
				{name: "Dave Rivera", zone: ZoneHousehold, props: identity("TEL", "+15551120005")},
				{name: "Dave Smith", zone: ZoneTrusted, props: identity("TEL", "+15551120006")},
				{name: "Dave", zone: ZoneKnown},
			},
			want: []string{"name:dave:Dave,Dave Rivera", "name:dave:Dave,Dave Smith"},
		},
		{
			name: "one person on two records with authority is one representative of a shared name",
			seeds: []forkSeed{
				{name: "Erin Rivera", nickname: "Erin", zone: ZoneHousehold, props: identity("TEL", "+15551120007")},
				{name: "Erin", zone: ZoneTrusted, props: identity("TEL", "+15551120007")},
				{name: "Erin Park", nickname: "Erin", zone: ZoneKnown, props: identity("TEL", "+15551120008")},
			},
			want: []string{"name:erin:Erin,Erin Rivera", "phone:15551120007:Erin,Erin Rivera", "shared_name:erin:Erin,Erin Park"},
		},
		{
			name: "a copy tied by a shared number counts as the person it copies in a shared name",
			seeds: []forkSeed{
				{name: "Gail Rivera", nickname: "Gail", zone: ZoneHousehold, props: identity("TEL", "+15551120009")},
				{name: "Gail", zone: ZoneKnown, props: identity("TEL", "+15551120009")},
				{name: "Gail Smith", nickname: "Gail", zone: ZoneTrusted, props: identity("TEL", "+15551120010")},
			},
			want: []string{"name:gail:Gail,Gail Rivera", "phone:15551120009:Gail,Gail Rivera", "shared_name:gail:Gail Rivera,Gail Smith"},
		},
		{
			name:  "a thin known record named with a household nickname is one finding",
			seeds: []forkSeed{{name: "Carol Household", nickname: "Carol", zone: ZoneHousehold}, {name: "Carol", zone: ZoneKnown}},
			want:  []string{"name:carol:Carol,Carol Household"},
		},
		{
			name:  "a thin known record named with a household first word is one finding",
			seeds: []forkSeed{{name: "Dave Household", zone: ZoneHousehold}, {name: "Dave", zone: ZoneKnown}},
			want:  []string{"name:dave:Dave,Dave Household"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			records := seedForkRecords(t, store, tt.seeds)
			if tt.pin != "" {
				store.pinOperatorContactID(records[tt.pin].ID)
			}
			audit := auditForks(t, store, 100)
			got := make([]string, 0, len(audit.Findings))
			for _, f := range audit.Findings {
				if f.MemberCount != len(f.Members) {
					t.Errorf("%s %q: member count %d, want %d", f.Kind, f.Key, f.MemberCount, len(f.Members))
				}
				names := forkMemberNames(f)
				slices.Sort(names)
				got = append(got, f.Kind+":"+f.Key+":"+strings.Join(names, ","))
			}
			slices.Sort(got)
			want := slices.Sorted(slices.Values(tt.want))
			if audit.Total != len(want) || !reflect.DeepEqual(got, want) {
				t.Errorf("findings = %q (total %d), want %q", got, audit.Total, want)
			}
		})
	}
}
