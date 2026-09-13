package email

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestResolveContactNormalizesResolverAnswers pins the contract every
// caller relies on: the status and effective zone are always populated,
// an error is a lookup failure and never a stranger, and only a match
// carries a binding.
func TestResolveContactNormalizesResolverAnswers(t *testing.T) {
	ctx := context.Background()

	if got := resolveContact(ctx, nil, "a@example.com"); got.Status != ContactUnmatched || got.TrustZone != ZoneUnknown {
		t.Errorf("nil resolver = %+v, want unmatched/unknown", got)
	}

	failing := &stubContacts{failing: map[string]error{"a@example.com": errors.New("locked")}}
	got := resolveContact(ctx, failing, "a@example.com")
	if got.Status != ContactLookupFailed || got.TrustZone != ZoneUnknown || got.Err == nil || got.Binding != nil {
		t.Errorf("failing resolver = %+v, want lookup_failed with the error and no binding", got)
	}

	bare := resolverFunc(func(context.Context, string) (ContactMatch, error) {
		return ContactMatch{Binding: &memory.ChannelBinding{ContactID: "x", TrustZone: "trusted"}}, nil
	})
	got = resolveContact(ctx, bare, "a@example.com")
	if got.Status != ContactUnmatched || got.Binding != nil || got.TrustZone != ZoneUnknown {
		t.Errorf("an empty status is unmatched and drops the binding: %+v", got)
	}

	matchedNoZone := resolverFunc(func(context.Context, string) (ContactMatch, error) {
		return ContactMatch{Status: ContactMatched, Binding: &memory.ChannelBinding{ContactID: "x", TrustZone: "household"}}, nil
	})
	got = resolveContact(ctx, matchedNoZone, "a@example.com")
	if got.TrustZone != "household" || got.Binding == nil {
		t.Errorf("a match without an explicit zone takes the binding's: %+v", got)
	}
}

// resolverFunc adapts a function to ContactResolver.
type resolverFunc func(context.Context, string) (ContactMatch, error)

func (f resolverFunc) ResolveEmailContact(ctx context.Context, address string) (ContactMatch, error) {
	return f(ctx, address)
}

func TestIdentityLookupMemoizesByAddressKey(t *testing.T) {
	resolver := &stubContacts{zones: map[string]string{"alice@example.com": "trusted"}}
	lookup := newIdentityLookup(context.Background(), resolver, nil)

	first := lookup.resolve(Address{Name: "Alice", Address: "Alice@Example.com"})
	second := lookup.resolve(Address{Address: "alice@example.com"})
	if first.Status != ContactMatched || second.Status != ContactMatched {
		t.Fatalf("expected matches, got %+v and %+v", first, second)
	}
	if resolver.calls != 1 {
		t.Errorf("resolver calls = %d, want 1", resolver.calls)
	}
	if got := lookup.resolve(Address{}); got.Status != ContactUnmatched || resolver.calls != 1 {
		t.Errorf("an empty address is unmatched without a lookup: %+v (calls %d)", got, resolver.calls)
	}
}

// TestResolveContactCapsAutomatedAddresses pins the automated cap at the
// one normalisation point every email consumer goes through: the
// address alone marks it, the effective zone drops to known at most,
// and a matched record stays bound so the sender is still recognised.
func TestResolveContactCapsAutomatedAddresses(t *testing.T) {
	ctx := context.Background()
	matched := func(zone string) ContactResolver {
		return resolverFunc(func(context.Context, string) (ContactMatch, error) {
			return ContactMatch{Status: ContactMatched, TrustZone: zone, Binding: &memory.ChannelBinding{ContactID: "id-record", ContactName: "Record", TrustZone: zone}}, nil
		})
	}
	ambiguous := resolverFunc(func(context.Context, string) (ContactMatch, error) {
		return ContactMatch{Status: ContactAmbiguous, TrustZone: contacts.ZoneTrusted, CandidatesTotal: 2,
			Candidates: []ContactCandidate{{ID: "h", Name: "H", TrustZone: contacts.ZoneHousehold}, {ID: "t", Name: "T", TrustZone: contacts.ZoneTrusted}}}, nil
	})
	unmatched := resolverFunc(func(context.Context, string) (ContactMatch, error) {
		return ContactMatch{Status: ContactUnmatched, TrustZone: ZoneUnknown}, nil
	})
	failing := resolverFunc(func(context.Context, string) (ContactMatch, error) {
		return ContactMatch{}, errors.New("database is locked")
	})

	tests := []struct {
		name          string
		resolver      ContactResolver
		address       string
		wantStatus    ContactStatus
		wantZone      string
		wantAutomated bool
		wantBinding   bool
	}{
		{name: "matched admin no-reply is capped and still bound", resolver: matched(contacts.ZoneAdmin), address: "noreply@forge.example", wantStatus: ContactMatched, wantZone: contacts.ZoneKnown, wantAutomated: true, wantBinding: true},
		{name: "matched household notifications is capped", resolver: matched(contacts.ZoneHousehold), address: "calendar-notification@forge.example", wantStatus: ContactMatched, wantZone: contacts.ZoneKnown, wantAutomated: true, wantBinding: true},
		{name: "ambiguous automated takes known", resolver: ambiguous, address: "no-reply@twins.example", wantStatus: ContactAmbiguous, wantZone: contacts.ZoneKnown, wantAutomated: true},
		{name: "known automated stays known", resolver: matched(contacts.ZoneKnown), address: "bounces@forge.example", wantStatus: ContactMatched, wantZone: contacts.ZoneKnown, wantAutomated: true, wantBinding: true},
		{name: "lookup_failed automated stays unknown", resolver: failing, address: "noreply@broken.example", wantStatus: ContactLookupFailed, wantZone: ZoneUnknown, wantAutomated: true},
		{name: "nil resolver automated stays unknown", resolver: nil, address: "noreply@nowhere.example", wantStatus: ContactUnmatched, wantZone: ZoneUnknown, wantAutomated: true},
		// Negative controls.
		{name: "matched household human keeps household", resolver: matched(contacts.ZoneHousehold), address: "alice@example.com", wantStatus: ContactMatched, wantZone: contacts.ZoneHousehold, wantBinding: true},
		{name: "unmatched automated is never raised to known", resolver: unmatched, address: "noreply@stranger.example", wantStatus: ContactUnmatched, wantZone: ZoneUnknown, wantAutomated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveContact(ctx, tt.resolver, tt.address)
			if got.Status != tt.wantStatus || got.TrustZone != tt.wantZone || got.Automated != tt.wantAutomated {
				t.Errorf("resolveContact = status %q zone %q automated %v, want %q %q %v", got.Status, got.TrustZone, got.Automated, tt.wantStatus, tt.wantZone, tt.wantAutomated)
			}
			if (got.Binding != nil) != tt.wantBinding {
				t.Fatalf("binding = %+v, want present=%v", got.Binding, tt.wantBinding)
			}
			if tt.wantBinding && (got.Binding.ContactID != "id-record" || got.Binding.ContactName != "Record") {
				t.Errorf("the cap must leave the matched record bound: %+v", got.Binding)
			}
		})
	}
}

// TestEffectiveZoneNeverExceedsDirectoryAnswer proves the cap only
// lowers: across every status, stored zone, and automated or human
// address, the effective zone is never more privileged than the
// directory's own answer, an automated address is known at most, and
// a human address is untouched.
func TestEffectiveZoneNeverExceedsDirectoryAnswer(t *testing.T) {
	rank := make(map[string]int)
	for i, p := range contacts.Policies() {
		rank[p.Zone] = i
	}
	// An unrecognised zone gets the unknown policy, so it ranks there.
	rankOf := func(zone string) int { return rank[contacts.Policy(zone).Zone] }

	ctx := context.Background()
	statuses := []ContactStatus{ContactMatched, ContactUnmatched, ContactAmbiguous, ContactLookupFailed, ""}
	zones := []string{contacts.ZoneAdmin, contacts.ZoneHousehold, contacts.ZoneTrusted, contacts.ZoneKnown, contacts.ZoneUnknown, "", "bogus"}
	addresses := []string{"noreply@example.com", "notifications@example.com", "alice@example.com"}
	resolvers := map[string]func(ContactStatus, string) ContactResolver{
		"resolver": func(status ContactStatus, zone string) ContactResolver {
			return resolverFunc(func(context.Context, string) (ContactMatch, error) {
				if status == ContactLookupFailed {
					return ContactMatch{}, errors.New("locked")
				}
				m := ContactMatch{Status: status, TrustZone: zone}
				if status == ContactMatched {
					m.Binding = &memory.ChannelBinding{ContactID: "id", TrustZone: zone}
				}
				return m, nil
			})
		},
		"nil": func(ContactStatus, string) ContactResolver { return nil },
	}
	for kind, build := range resolvers {
		for _, status := range statuses {
			for _, zone := range zones {
				for _, address := range addresses {
					resolver := build(status, zone)
					directory := directoryAnswer(ctx, resolver, address)
					got := resolveContact(ctx, resolver, address)
					_, automated := contacts.AutomatedAddress(address)
					where := fmt.Sprintf("%s status=%q zone=%q address=%s", kind, status, zone, address)
					if rankOf(got.TrustZone) < rankOf(directory.TrustZone) {
						t.Errorf("%s: effective %q outranks the directory's %q", where, got.TrustZone, directory.TrustZone)
					}
					if got.Automated != automated {
						t.Errorf("%s: automated = %v, want %v", where, got.Automated, automated)
					}
					if automated && rankOf(got.TrustZone) < rank[contacts.ZoneKnown] {
						t.Errorf("%s: automated address at %q, above known", where, got.TrustZone)
					}
					if !automated && got.TrustZone != directory.TrustZone {
						t.Errorf("%s: a human address changed zone %q -> %q", where, directory.TrustZone, got.TrustZone)
					}
					if (got.Binding == nil) != (directory.Binding == nil) || got.Status != directory.Status {
						t.Errorf("%s: the cap changed status or binding: %+v vs %+v", where, got, directory)
					}
				}
			}
		}
	}
}
