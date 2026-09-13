package email

import (
	"context"
	"errors"
	"testing"

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
