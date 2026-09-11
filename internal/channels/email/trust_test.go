package email

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// stubContacts implements ContactResolver from fixed tables: one
// address to one zone for matches, an address to several candidates
// for ambiguity, and an address whose lookup fails.
type stubContacts struct {
	zones     map[string]string             // address → zone (matched)
	owners    map[string]bool               // address → is_owner
	ambiguous map[string][]ContactCandidate // address → candidates
	failing   map[string]error              // address → store error
	calls     int
}

func (s *stubContacts) ResolveEmailContact(_ context.Context, addr string) (ContactMatch, error) {
	s.calls++
	addr = strings.ToLower(addr)
	if err, ok := s.failing[addr]; ok {
		return ContactMatch{}, err
	}
	if cands, ok := s.ambiguous[addr]; ok {
		lowest := cands[len(cands)-1].TrustZone
		return ContactMatch{Status: ContactAmbiguous, TrustZone: lowest, Candidates: cands}, nil
	}
	zone, ok := s.zones[addr]
	if !ok {
		return ContactMatch{Status: ContactUnmatched, TrustZone: ZoneUnknown}, nil
	}
	local := strings.SplitN(addr, "@", 2)[0]
	return ContactMatch{
		Status:    ContactMatched,
		TrustZone: zone,
		Binding: &memory.ChannelBinding{
			Channel:     "email",
			Address:     addr,
			ContactID:   "id-" + local,
			ContactName: strings.ToUpper(local[:1]) + local[1:],
			TrustZone:   zone,
			LinkSource:  "email",
			IsOwner:     s.owners[addr],
		},
	}, nil
}

func TestCheckRecipientTrust_NilResolver(t *testing.T) {
	result := CheckRecipientTrust(context.Background(), nil, []string{"a@example.com", "b@example.com"})
	if len(result.Allowed) != 2 || result.HasIssues() {
		t.Errorf("nil resolver should allow all without issues, got %+v", result)
	}
}

func TestCheckRecipientTrust(t *testing.T) {
	resolver := &stubContacts{
		zones: map[string]string{
			"admin@example.com":     "admin",
			"household@example.com": "household",
			"trusted@example.com":   "trusted",
			"known@example.com":     "known",
		},
		ambiguous: map[string][]ContactCandidate{
			"twins@example.com":  {{ID: "t1", Name: "Twin One", TrustZone: "trusted"}, {ID: "t2", Name: "Twin Two", TrustZone: "known"}},
			"triple@example.com": {{ID: "a", Name: "A", TrustZone: "admin"}, {ID: "b", Name: "B", TrustZone: "household"}},
		},
		failing: map[string]error{"broken@example.com": errors.New("database is locked")},
	}

	tests := []struct {
		name        string
		addresses   []string
		wantAllowed int
		wantBlocked int
		wantReason  string
		wantStatus  ContactStatus
	}{
		{name: "admin allowed", addresses: []string{"admin@example.com"}, wantAllowed: 1, wantStatus: ContactMatched},
		{name: "household allowed", addresses: []string{"household@example.com"}, wantAllowed: 1, wantStatus: ContactMatched},
		{name: "trusted allowed", addresses: []string{"trusted@example.com"}, wantAllowed: 1, wantStatus: ContactMatched},
		{name: "known blocked", addresses: []string{"known@example.com"}, wantBlocked: 1, wantReason: "known trust zone", wantStatus: ContactMatched},
		{name: "unknown blocked", addresses: []string{"stranger@example.com"}, wantBlocked: 1, wantReason: "no contact record", wantStatus: ContactUnmatched},
		{name: "ambiguous governed by least privileged", addresses: []string{"twins@example.com"}, wantBlocked: 1, wantReason: "2 contact records", wantStatus: ContactAmbiguous},
		{name: "ambiguous but every candidate eligible", addresses: []string{"triple@example.com"}, wantAllowed: 1, wantStatus: ContactAmbiguous},
		{name: "lookup failure is not a stranger", addresses: []string{"broken@example.com"}, wantBlocked: 1, wantReason: "could not be consulted (database is locked)", wantStatus: ContactLookupFailed},
		{name: "unparseable address blocked", addresses: []string{"not an address"}, wantBlocked: 1, wantReason: "not a valid email address"},
		{name: "mixed recipients", addresses: []string{"admin@example.com", "known@example.com", "stranger@example.com"}, wantAllowed: 1, wantBlocked: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckRecipientTrust(context.Background(), resolver, tt.addresses)
			if len(result.Allowed) != tt.wantAllowed {
				t.Errorf("Allowed = %d, want %d: %+v", len(result.Allowed), tt.wantAllowed, result)
			}
			if len(result.Blocked) != tt.wantBlocked {
				t.Errorf("Blocked = %d, want %d: %+v", len(result.Blocked), tt.wantBlocked, result)
			}
			if len(result.Assessments) != len(tt.addresses) {
				t.Fatalf("assessments = %d, want one per address", len(result.Assessments))
			}
			if tt.wantReason != "" && !strings.Contains(result.Assessments[0].Reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", result.Assessments[0].Reason, tt.wantReason)
			}
			if tt.wantStatus != "" && result.Assessments[0].ContactStatus != tt.wantStatus {
				t.Errorf("contact_status = %q, want %q", result.Assessments[0].ContactStatus, tt.wantStatus)
			}
		})
	}
}

func TestCheckRecipientTrust_ExtractsAddressAndMemoizes(t *testing.T) {
	resolver := &stubContacts{zones: map[string]string{"user@example.com": "trusted"}}
	result := CheckRecipientTrust(context.Background(), resolver, []string{"Alice <user@example.com>", "USER@example.com"})
	if len(result.Allowed) != 2 {
		t.Errorf("display-name and case variants should both match, got %+v", result)
	}
	if resolver.calls != 1 {
		t.Errorf("resolver calls = %d, want 1 (memoized by address key)", resolver.calls)
	}
	if result.Assessments[0].ContactID != "id-user" || result.Assessments[0].ContactName != "User" {
		t.Errorf("assessment should carry the matched contact: %+v", result.Assessments[0])
	}
}

func TestTrustResultFormatIssuesNamesEveryRecipient(t *testing.T) {
	resolver := &stubContacts{zones: map[string]string{"known@example.com": "known"}}
	result := CheckRecipientTrust(context.Background(), resolver, []string{"known@example.com", "nobody@example.com"})
	if !result.HasIssues() {
		t.Fatal("expected issues")
	}
	mustContain(t, result.FormatIssues(), "Cannot send to known@example.com", "Cannot send to nobody@example.com", "contact_save")
}
