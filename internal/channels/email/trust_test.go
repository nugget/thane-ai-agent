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
	result := CheckRecipientTrust(context.Background(), nil, []string{"a@example.com", "B@Example.com", "not an address"})
	if len(result.Allowed) != 2 || len(result.Blocked) != 1 {
		t.Errorf("nil resolver should allow every parseable address and block the rest, got %+v", result)
	}
	if len(result.Assessments) != 3 || result.Assessments[1].Address != "b@example.com" || result.Assessments[1].ContactStatus != ContactUnmatched || !result.Assessments[1].Allowed || result.Assessments[1].Contact != nil {
		t.Errorf("nil resolver must still assess every recipient: %+v", result.Assessments)
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
	if c := result.Assessments[0].Contact; c == nil || c.ID != "id-user" || c.Name != "User" {
		t.Errorf("assessment should carry the matched contact: %+v", result.Assessments[0])
	}
}

func TestTrustResultHasIssuesFollowsAssessments(t *testing.T) {
	resolver := &stubContacts{zones: map[string]string{"known@example.com": "known", "ok@example.com": "admin"}}
	result := CheckRecipientTrust(context.Background(), resolver, []string{"ok@example.com", "known@example.com"})
	if !result.HasIssues() {
		t.Fatal("a refused recipient is an issue")
	}
	if clean := CheckRecipientTrust(context.Background(), resolver, []string{"ok@example.com"}); clean.HasIssues() {
		t.Errorf("an all-allowed result has no issues: %+v", clean)
	}
	if !strings.Contains(strings.Join(result.Blocked, "\n"), "Cannot send to known@example.com") {
		t.Errorf("Blocked should carry the refusal line: %v", result.Blocked)
	}
}

// TestAmbiguousRefusalCountsEveryRecord pins that the refusal reason
// names the full number of records, not only the rendered candidates.
func TestAmbiguousRefusalCountsEveryRecord(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string) (ContactMatch, error) {
		return ContactMatch{Status: ContactAmbiguous, TrustZone: "known", CandidatesTotal: 55,
			Candidates: []ContactCandidate{{ID: "a", Name: "A", TrustZone: "trusted"}, {ID: "b", Name: "B", TrustZone: "known"}}}, nil
	})
	result := CheckRecipientTrust(context.Background(), resolver, []string{"shared@example.com"})
	mustContain(t, result.Assessments[0].Reason, "belongs to 55 contact records", "and 53 more")
}
