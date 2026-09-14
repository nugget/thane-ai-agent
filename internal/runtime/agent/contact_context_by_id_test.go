package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestChannelProvider_ResolvesBoundContactByID pins that channel context
// follows the contact the channel bound, by ID, so another contact that
// now answers to the sender's name cannot take the turn's context.
func TestChannelProvider_ResolvesBoundContactByID(t *testing.T) {
	lookup := &mockContactLookup{
		byID:     map[string]*ContactContext{"contact-alice": {ID: "contact-alice", Name: "Alice Smith", TrustZone: "household"}},
		contacts: map[string]*ContactContext{"Alice Smith": {ID: "contact-mallory", Name: "Alice Smith", TrustZone: "known"}},
	}
	p := NewChannelProvider(lookup)
	bound := func(contactID string) context.Context {
		ctx := tools.WithChannelBinding(context.Background(), &memory.ChannelBinding{
			Channel:     "signal",
			Address:     "+15551234567",
			ContactID:   contactID,
			ContactName: "Alice Smith",
			TrustZone:   "household",
		})
		return tools.WithHints(ctx, map[string]string{"source": "signal", "sender_name": "Alice Smith"})
	}

	got, err := p.TagContext(bound("contact-alice"), agentctx.ContextRequest{UserMessage: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if contact := parseContactJSON(t, got); contact.ID != "contact-alice" || contact.TrustZone != "household" {
		t.Errorf("bound contact context = %+v, want contact-alice", contact)
	}

	// The negative control proves the ID did the work: with no bound ID
	// the same name reaches the other contact.
	got, err = p.TagContext(bound(""), agentctx.ContextRequest{UserMessage: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if contact := parseContactJSON(t, got); contact.ID != "contact-mallory" {
		t.Errorf("name-resolved contact context = %+v, want contact-mallory", contact)
	}
}

// TestOriginContactContext_IDMissDoesNotFallBackToName pins that a
// session origin naming its contact by ID is that contact or none.
func TestOriginContactContext_IDMissDoesNotFallBackToName(t *testing.T) {
	lookup := &mockContactLookup{
		byID:     map[string]*ContactContext{"contact-alice": {ID: "contact-alice", Name: "Alice Smith"}},
		contacts: map[string]*ContactContext{"Alice Smith": {ID: "contact-mallory", Name: "Alice Smith"}},
	}
	l := &Loop{contactLookup: lookup}
	origin := func(id string) *SessionOriginPolicyResult {
		return &SessionOriginPolicyResult{Origin: SessionOrigin{Source: "signal", ContactID: id, ContactName: "Alice Smith"}}
	}

	if got := l.originContactContext(context.Background(), origin("contact-alice")); got == nil || got.ID != "contact-alice" {
		t.Errorf("origin by ID = %+v, want contact-alice", got)
	}
	if got := l.originContactContext(context.Background(), origin("contact-gone")); got != nil {
		t.Errorf("origin whose ID misses = %+v, want no contact", got)
	}
	if got := l.originContactContext(context.Background(), origin("")); got == nil || got.ID != "contact-mallory" {
		t.Errorf("origin by name only = %+v, want the name match", got)
	}
}
